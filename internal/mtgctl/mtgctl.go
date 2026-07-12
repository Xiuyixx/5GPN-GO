// Package mtgctl is a thin controller over the externally-installed
// 9seconds/mtg (https://github.com/9seconds/mtg) systemd service.
//
// The 5gpn panel used to ship its own MTProto relay under
// internal/proxy/mtproxy/, but that code had a subtle relay bug we
// couldn't fix in reasonable time. Both production VPS now run mtg 2.x
// as `mtg.service` bound to :2443 with a fake-TLS ee-prefix secret; the
// panel's "Telegram MTProxy" card is rewired to drive that service via
// this package.
//
// The Controller shells out to `systemctl` for lifecycle and to
// `/usr/local/bin/mtg` for secret generation. Every invocation runs
// under a 5s exec.CommandContext timeout so a stuck systemd cannot hang
// a panel request. When the mtg binary or the systemd unit file are
// missing (e.g. the panel is deployed to a host that has not installed
// mtg yet) methods return ErrNotInstalled so the API layer can surface
// a helpful message rather than a shell error.
package mtgctl

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Defaults for a stock deployment. Package-level so tests and API
// wiring can reference them without importing exec.
const (
	DefaultBinaryPath = "/usr/local/bin/mtg"
	DefaultUnitName   = "mtg.service"
	DefaultUnitFile   = "/etc/systemd/system/mtg.service"
	DefaultListen     = "0.0.0.0:2443"
	// FakeTLSSecretPrefix is the 0xee magic byte that marks an mtg 2.x
	// fake-TLS secret. Used by DecodeFrontingDomain to guard against
	// legacy dd-prefixed secrets.
	FakeTLSSecretPrefix = 0xee
	// shellTimeout is the ceiling for every shell-out. systemctl calls
	// normally return in <200ms; anything past 5s is a broken service
	// manager and we'd rather 5xx the panel than hang forever.
	shellTimeout = 5 * time.Second
)

// ErrNotInstalled is returned when the mtg binary or the systemd unit
// file are missing on the host. The API layer maps this to a 503 with
// a "install 9seconds/mtg first" hint.
var ErrNotInstalled = errors.New("mtgctl: 9seconds/mtg not installed")

// execCommandContext is the seam for tests to intercept shell calls.
// Production code always resolves to exec.CommandContext.
var execCommandContext = exec.CommandContext

// osStat is the seam for tests to fake filesystem probes without
// touching real /usr/local/bin/mtg.
var osStat = os.Stat

// Controller drives an installed mtg.service. Zero value is usable
// once New() has filled the defaults.
type Controller struct {
	// BinaryPath is the mtg CLI, used only by GenerateSecret.
	BinaryPath string
	// UnitName is the systemctl argument (e.g. "mtg.service").
	UnitName string
	// UnitFile is the filesystem path we read/write for ExecStart.
	UnitFile string
	// Logger, when non-nil, receives per-invocation debug lines so
	// operators can trace which systemctl calls fired.
	Logger *slog.Logger
}

// New returns a Controller with defaults filled in. Passing "" for any
// of the paths keeps the default; a non-empty value overrides it.
func New(binary, unitName, unitFile string, logger *slog.Logger) *Controller {
	c := &Controller{
		BinaryPath: binary,
		UnitName:   unitName,
		UnitFile:   unitFile,
		Logger:     logger,
	}
	if c.BinaryPath == "" {
		c.BinaryPath = DefaultBinaryPath
	}
	if c.UnitName == "" {
		c.UnitName = DefaultUnitName
	}
	if c.UnitFile == "" {
		c.UnitFile = DefaultUnitFile
	}
	return c
}

// IsActive returns true when `systemctl is-active <unit>` prints
// "active". Any other line (inactive/failed/activating) yields false;
// the raw string is available via Status().
func (c *Controller) IsActive(ctx context.Context) (bool, error) {
	s, err := c.Status(ctx)
	if err != nil {
		return false, err
	}
	return s == "active", nil
}

// Status returns the trimmed stdout of `systemctl is-active <unit>`.
// systemctl exits non-zero for anything but "active" — that's expected
// (inactive is a valid state), so a non-zero exit with recognisable
// stdout is not treated as an error.
func (c *Controller) Status(ctx context.Context) (string, error) {
	if err := c.checkInstalled(); err != nil {
		return "not-installed", err
	}
	out, runErr := c.run(ctx, "systemctl", "is-active", c.UnitName)
	trimmed := strings.TrimSpace(out)
	if runErr != nil {
		switch trimmed {
		case "inactive", "failed", "activating", "deactivating", "unknown":
			return trimmed, nil
		default:
			return "", fmt.Errorf("mtgctl: systemctl is-active %s: %w: %s", c.UnitName, runErr, trimmed)
		}
	}
	if trimmed == "" {
		return "", errors.New("mtgctl: systemctl is-active returned an empty status")
	}
	return trimmed, nil
}

// Enable runs `systemctl enable --now <unit>` so the service starts now
// and boots automatically thereafter.
func (c *Controller) Enable(ctx context.Context) error {
	if err := c.checkInstalled(); err != nil {
		return err
	}
	_, err := c.run(ctx, "systemctl", "enable", "--now", c.UnitName)
	return err
}

// Disable runs `systemctl disable --now <unit>` — stops now and skips
// on next boot.
func (c *Controller) Disable(ctx context.Context) error {
	if err := c.checkInstalled(); err != nil {
		return err
	}
	_, err := c.run(ctx, "systemctl", "disable", "--now", c.UnitName)
	return err
}

// Restart runs `systemctl restart <unit>`. Used after WriteUnit so the
// process picks up a new ExecStart line.
func (c *Controller) Restart(ctx context.Context) error {
	if err := c.checkInstalled(); err != nil {
		return err
	}
	_, err := c.run(ctx, "systemctl", "restart", c.UnitName)
	return err
}

// DaemonReload runs `systemctl daemon-reload`. Called after WriteUnit.
func (c *Controller) DaemonReload(ctx context.Context) error {
	_, err := c.run(ctx, "systemctl", "daemon-reload")
	return err
}

// GenerateSecret shells to `<mtg> generate-secret <domain>` and returns
// the trimmed stdout — the base64-url ee-prefix secret Telegram clients
// consume. When frontingDomain is empty we fall back to
// "www.cloudflare.com" so a lazy caller still ends up with a valid
// fake-TLS front.
func (c *Controller) GenerateSecret(ctx context.Context, frontingDomain string) (string, error) {
	if _, err := osStat(c.BinaryPath); err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotInstalled
		}
		return "", fmt.Errorf("mtgctl: stat %s: %w", c.BinaryPath, err)
	}
	domain := strings.TrimSpace(frontingDomain)
	if domain == "" {
		domain = "www.cloudflare.com"
	}
	out, err := c.run(ctx, c.BinaryPath, "generate-secret", domain)
	if err != nil {
		return "", fmt.Errorf("mtgctl: generate-secret: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// WriteUnit rewrites the systemd unit file to bind <listen> with
// <secret> and calls DaemonReload. It intentionally does NOT
// enable/restart — callers control lifecycle explicitly via Enable /
// Restart so an operator saving-while-disabled doesn't accidentally
// spin the service up.
func (c *Controller) WriteUnit(ctx context.Context, listen, secret string) error {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		listen = DefaultListen
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("mtgctl: WriteUnit: secret required")
	}
	if strings.ContainsAny(secret, " \t\r\n") {
		return errors.New("mtgctl: WriteUnit: secret must be one argument")
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return fmt.Errorf("mtgctl: WriteUnit: invalid listen address: %w", err)
	}
	unit := renderUnit(c.BinaryPath, listen, secret)
	old, readErr := os.ReadFile(c.UnitFile)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("mtgctl: read existing %s: %w", c.UnitFile, readErr)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(c.UnitFile); err == nil {
		mode = info.Mode().Perm()
	}
	if err := writeAtomic(c.UnitFile, []byte(unit), mode); err != nil {
		return fmt.Errorf("mtgctl: write %s: %w", c.UnitFile, err)
	}
	if err := c.DaemonReload(ctx); err != nil {
		var restoreErr error
		if existed {
			restoreErr = writeAtomic(c.UnitFile, old, mode)
		} else {
			restoreErr = os.Remove(c.UnitFile)
			if os.IsNotExist(restoreErr) {
				restoreErr = nil
			}
		}
		if restoreErr == nil {
			restoreErr = c.DaemonReload(ctx)
		}
		if restoreErr != nil {
			return fmt.Errorf("mtgctl: daemon-reload: %v; restore: %w", err, restoreErr)
		}
		return fmt.Errorf("mtgctl: daemon-reload: %w", err)
	}
	return nil
}

func writeAtomic(path string, body []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mtg-unit-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

// ReadUnit parses the current ExecStart line from the unit file and
// extracts the listen address + secret. Returns "" strings when the
// file is missing (fresh install), and (empty, empty, nil) when the
// ExecStart line does not look like our shape (operator hand-edited).
func (c *Controller) ReadUnit(_ context.Context) (listen, secret string, err error) {
	raw, err := os.ReadFile(c.UnitFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("mtgctl: read %s: %w", c.UnitFile, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		return parseExecStart(strings.TrimPrefix(line, "ExecStart="))
	}
	return "", "", nil
}

// DecodeFrontingDomain returns the fronting hostname embedded in an
// mtg 2.x fake-TLS secret. Layout:
//
//	byte 0     : 0xee magic
//	bytes 1-16 : the 16-byte relay secret
//	bytes 17+  : the raw ASCII hostname
//
// Encoded via base64-url without padding. Returns "" if the secret is
// unparseable or the ee-magic is missing — callers surface the empty
// string as "unknown fronting domain" rather than crashing.
func DecodeFrontingDomain(secret string) string {
	raw, err := decodeSecret(secret)
	if err != nil || len(raw) < 17 {
		return ""
	}
	if raw[0] != FakeTLSSecretPrefix {
		return ""
	}
	return string(raw[17:])
}

// decodeSecret handles both padded and unpadded base64-url encodings so
// pasting a copy from Telegram apps that add "=" pads still works.
func decodeSecret(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty secret")
	}
	if raw, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// renderUnit produces the canonical unit body. Kept as a plain string
// concat so operator diffs stay readable.
func renderUnit(binaryPath, listen, secret string) string {
	if binaryPath == "" {
		binaryPath = DefaultBinaryPath
	}
	return "[Unit]\n" +
		"Description=9seconds/mtg (managed by 5gpn panel)\n" +
		"After=network.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + binaryPath + " run --bind " + listen + " " + secret + "\n" +
		"Restart=on-failure\n" +
		"RestartSec=5\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

// parseExecStart pulls the `--bind <addr>` (or `-b <addr>`) flag and
// the trailing positional secret out of an ExecStart argument list.
// Everything except the flag pair and the trailing secret is ignored so
// operator-added flags (metrics port, etc.) don't confuse us.
func parseExecStart(cmd string) (listen, secret string, err error) {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return "", "", nil
	}
	// Trailing positional = secret (the only non-flag arg mtg's `run`
	// accepts).
	secret = fields[len(fields)-1]
	// Sweep for --bind or -b.
	for i := 1; i < len(fields)-1; i++ {
		f := fields[i]
		switch {
		case f == "--bind" || f == "-b":
			if i+1 < len(fields)-1 {
				listen = fields[i+1]
			}
		case strings.HasPrefix(f, "--bind="):
			listen = strings.TrimPrefix(f, "--bind=")
		}
	}
	return listen, secret, nil
}

// checkInstalled returns ErrNotInstalled when the unit file is
// missing. The mtg binary is only required for GenerateSecret so we
// don't gate the systemctl surface on it here.
func (c *Controller) checkInstalled() error {
	if _, err := osStat(c.UnitFile); err != nil {
		if os.IsNotExist(err) {
			return ErrNotInstalled
		}
		return fmt.Errorf("mtgctl: stat unit %s: %w", c.UnitFile, err)
	}
	return nil
}

// run executes cmd + args under a 5s ceiling. Returns stdout as a
// string so callers don't repeat the bytes-to-string dance. When
// systemctl exits non-zero we surface stdout unchanged for is-active
// (inactive/failed are valid states) but return the error otherwise so
// callers can decide.
func (c *Controller) run(ctx context.Context, cmd string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()
	if c.Logger != nil {
		c.Logger.Debug("mtgctl exec", "cmd", cmd, "args", args)
	}
	out, err := execCommandContext(cctx, cmd, args...).CombinedOutput()
	return string(out), err
}
