package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// InstallOptions carries user-tunable knobs. Zero-value is safe.
type InstallOptions struct {
	Force         bool   // rewrite config.yaml even if one exists
	SkipUnit      bool   // do not write systemd unit (useful for containers)
	SkipEnable    bool   // do not enable+start the unit
	SourceBinary  string // path to the compiled 5gpn binary to copy in (empty = assume BinaryPath is already populated)
	ConfigContent []byte // override the default config template
}

// Install lays down the whole daemon: user, dirs, config, binary, unit.
// Every step is idempotent so re-running is safe.
func Install(ctx context.Context, env Env, exec Executor, opts InstallOptions) error {
	steps := []func() error{
		func() error { return ensureUser(ctx, env, exec) },
		func() error { return ensureDirs(env, exec) },
		func() error { return ensureConfig(env, exec, opts) },
		func() error { return ensureBinary(env, exec, opts.SourceBinary) },
		// Config remains installer-owned and group-readable by the daemon.
		// Runtime state belongs to the daemon and is private to that user.
		func() error { return finalizeConfigOwnership(env, exec) },
		func() error { return finalizeStatePermissions(env, exec) },
		func() error {
			if opts.SkipUnit {
				return nil
			}
			return ensureUnit(env, exec)
		},
		func() error {
			if opts.SkipEnable || opts.SkipUnit {
				return nil
			}
			return enableUnit(ctx, env, exec)
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall removes the unit + binary and, when purge is set, wipes state
// too. Config is preserved unless purge is set — matches every other
// packaged daemon on Linux.
func Uninstall(ctx context.Context, env Env, ex Executor, purge bool) error {
	if runtime.GOOS == "linux" {
		_ = ex.Run(ctx, "systemctl", "stop", env.Unit)
		_ = ex.Run(ctx, "systemctl", "disable", env.Unit)
	}
	targets := []string{env.UnitPath(), env.BinaryPath()}
	if purge {
		targets = append(targets, env.ConfigDir, env.DataDir)
	}
	var firstErr error
	for _, t := range targets {
		if err := ex.Remove(t); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if runtime.GOOS == "linux" {
		_ = ex.Run(ctx, "systemctl", "daemon-reload")
	}
	return firstErr
}

// UpgradeOptions describes a binary swap.
type UpgradeOptions struct {
	NewBinary   string // path to the new binary artefact
	SkipRestart bool
}

// Upgrade puts a new binary in place with a .prev fallback and restarts the
// unit. Health-checking is handled by the daemon's own updater package;
// installer-side upgrade is the offline path.
func Upgrade(ctx context.Context, env Env, ex Executor, opts UpgradeOptions) error {
	if opts.NewBinary == "" {
		return errors.New("installer: NewBinary required")
	}
	backup := env.BinaryPath() + ".prev"
	if ex.Exists(env.BinaryPath()) {
		if err := copyOrRun(ctx, ex, env.BinaryPath(), backup); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	if err := copyOrRun(ctx, ex, opts.NewBinary, env.BinaryPath()); err != nil {
		return fmt.Errorf("install new: %w", err)
	}
	if !opts.SkipRestart && runtime.GOOS == "linux" {
		if err := ex.Run(ctx, "systemctl", "restart", env.Unit); err != nil {
			// best-effort rollback: swap the backup back
			_ = copyOrRun(ctx, ex, backup, env.BinaryPath())
			_ = ex.Run(ctx, "systemctl", "restart", env.Unit)
			return fmt.Errorf("restart: %w", err)
		}
	}
	return nil
}

// Status prints unit + probe output. In the recorder it just enqueues the
// systemctl calls so we can assert they were made.
func Status(ctx context.Context, env Env, ex Executor, out io.Writer) error {
	if runtime.GOOS != "linux" {
		_, _ = fmt.Fprintln(out, "status: not linux — nothing to report")
		return nil
	}
	if err := ex.Run(ctx, "systemctl", "is-active", env.Unit); err != nil {
		_, _ = fmt.Fprintln(out, "unit: inactive")
	} else {
		_, _ = fmt.Fprintln(out, "unit: active")
	}
	return nil
}

// DoctorReport summarizes doctor findings.
type DoctorReport struct {
	Checks []DoctorCheck
}

// DoctorCheck is one boolean assertion about the host.
type DoctorCheck struct {
	Name   string
	OK     bool
	Detail string
}

// Doctor inspects the host for the pieces install would need. Read-only.
// When distro is a zero value, Doctor skips distro-family assertions.
func Doctor(_ context.Context, env Env, ex Executor, distro Distro) DoctorReport {
	rep := DoctorReport{}
	add := func(name string, ok bool, detail string) {
		rep.Checks = append(rep.Checks, DoctorCheck{Name: name, OK: ok, Detail: detail})
	}
	add("bin dir writable", writable(env.BinDir), env.BinDir)
	add("config dir present or creatable", ex.Exists(env.ConfigDir) || writable(parent(env.ConfigDir)), env.ConfigDir)
	add("data dir present or creatable", ex.Exists(env.DataDir) || writable(parent(env.DataDir)), env.DataDir)
	if runtime.GOOS == "linux" {
		add("systemctl on PATH", onPath("systemctl"), "")
		add("groupadd on PATH", onPath("groupadd"), "")
		add("useradd on PATH", onPath("useradd"), "")
	}
	if distro.ID != "" {
		fam := distro.Family()
		add("distro recognized", fam != FamilyUnknown, distro.String())
	}
	return rep
}

func ensureUser(ctx context.Context, env Env, ex Executor) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	// Create a stable primary group first. Existing-account errors are ignored;
	// the ownership pass below remains the authoritative verification.
	_ = ex.Run(ctx, "groupadd", "--system", "--force", env.Group)
	_ = ex.Run(ctx, "useradd", "--system", "--no-create-home", "--gid", env.Group,
		"--shell", "/usr/sbin/nologin", env.User)
	return nil
}

func ensureDirs(env Env, ex Executor) error {
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{env.BinDir, 0o755},
		{env.ConfigDir, 0o750},
		{env.DataDir, 0o700},
		{env.UnitDir, 0o755},
	}
	for _, d := range dirs {
		if err := ex.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("mkdir %s: %w", d.path, err)
		}
	}
	// MkdirAll leaves an existing directory's mode untouched. Tighten the two
	// sensitive trees on reinstall as well as on a fresh install.
	if err := ex.Chmod(env.ConfigDir, 0o750); err != nil {
		return fmt.Errorf("chmod config dir: %w", err)
	}
	if err := ex.Chmod(env.DataDir, 0o700); err != nil {
		return fmt.Errorf("chmod data dir: %w", err)
	}
	// DataDir hosts the SQLite db, JWT key, iOS profiles — daemon writes
	// to it at runtime, must be owned by the daemon user.
	if err := ex.Chown(env.DataDir, env.User, env.Group); err != nil {
		return fmt.Errorf("chown data: %w", err)
	}
	// ConfigDir is deliberately root-owned. The daemon's primary group gets
	// read/traverse access, but a daemon compromise cannot rewrite boot config.
	if err := ex.Chown(env.ConfigDir, "root", env.Group); err != nil {
		return fmt.Errorf("chown config dir: %w", err)
	}
	return nil
}

// finalizeConfigOwnership keeps config installer-owned and daemon-readable.
func finalizeConfigOwnership(env Env, ex Executor) error {
	if err := ex.Chown(env.ConfigDir, "root", env.Group); err != nil {
		return fmt.Errorf("chown config tree: %w", err)
	}
	if err := ex.Chmod(env.ConfigDir, 0o750); err != nil {
		return fmt.Errorf("chmod config tree: %w", err)
	}
	if ex.Exists(env.ConfigPath()) {
		if err := ex.Chmod(env.ConfigPath(), 0o640); err != nil {
			return fmt.Errorf("chmod config: %w", err)
		}
	}
	return nil
}

// finalizeStatePermissions repairs sensitive files left by older installs.
// UMask=0077 in the unit covers new SQLite sidecars and keys; these explicit
// chmods cover a reinstall over files created before that unit hardening.
func finalizeStatePermissions(env Env, ex Executor) error {
	if err := ex.Chmod(env.DataDir, 0o700); err != nil {
		return fmt.Errorf("chmod data tree: %w", err)
	}
	for _, name := range []string{"5gpn.db", "5gpn.db-wal", "5gpn.db-shm", "jwt.key"} {
		path := filepath.Join(env.DataDir, name)
		if !ex.Exists(path) {
			continue
		}
		if err := ex.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("chmod state file %s: %w", path, err)
		}
	}
	return nil
}

func ensureConfig(env Env, ex Executor, opts InstallOptions) error {
	path := env.ConfigPath()
	if ex.Exists(path) && !opts.Force {
		return nil
	}
	body := opts.ConfigContent
	if body == nil {
		body = []byte(DefaultConfigTemplate)
	}
	return ex.WriteFile(path, body, 0o640)
}

func ensureBinary(env Env, ex Executor, source string) error {
	if source == "" {
		return nil
	}
	body, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read source binary: %w", err)
	}
	return ex.WriteFile(env.BinaryPath(), body, 0o755)
}

func ensureUnit(env Env, ex Executor) error {
	body, err := Render(UnitTemplate, env)
	if err != nil {
		return err
	}
	return ex.WriteFile(env.UnitPath(), body, 0o644)
}

func enableUnit(ctx context.Context, env Env, ex Executor) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if err := ex.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := ex.Run(ctx, "systemctl", "enable", env.Unit); err != nil {
		return err
	}
	return ex.Run(ctx, "systemctl", "restart", env.Unit)
}

func copyOrRun(_ context.Context, ex Executor, src, dst string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return ex.WriteFile(dst, body, 0o755)
}

func writable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o200 != 0
}

func parent(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "/"
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
