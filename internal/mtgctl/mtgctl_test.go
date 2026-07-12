// Tests for the mtgctl controller. exec.CommandContext is swapped for a
// test helper that captures argv (verifying the shell shape without
// actually invoking systemctl / mtg), and the filesystem probe used by
// checkInstalled is stubbed so the tests don't require a real unit
// file on the developer's box.
package mtgctl

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// captured holds the argv seen by the fake execCommandContext so each
// test can assert the shape.
type captured struct {
	name string
	args []string
}

// fakeExec replaces execCommandContext for a test. It emits stubOut on
// stdout and, when stubErr is set, non-zero exit. captureSink is
// appended for every call so table tests can inspect the sequence.
func fakeExec(t *testing.T, sink *[]captured, stubOut string, stubErr bool) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// Record what the controller asked for.
		*sink = append(*sink, captured{name: name, args: append([]string(nil), args...)})
		// Route through `sh -c` so we can shape stdout + exit code
		// without spawning a real target binary. shellTimeout ceiling
		// still applies via the parent context.
		script := "printf '%s' " + shellQuote(stubOut)
		if stubErr {
			script += "; exit 3"
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	t.Cleanup(func() { execCommandContext = prev })
}

// shellQuote is a tiny single-quote wrapper — the fake never receives
// operator-controlled data so escaping just apostrophes is enough.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// stubStat makes checkInstalled see the unit file as present without
// creating one. Individual tests that want ErrNotInstalled override
// again with a Not-Exist stub.
func stubStat(t *testing.T, exists bool) {
	t.Helper()
	prev := osStat
	osStat = func(_ string) (os.FileInfo, error) {
		if exists {
			return fakeFileInfo{}, nil
		}
		return nil, &os.PathError{Op: "stat", Path: "stub", Err: os.ErrNotExist}
	}
	t.Cleanup(func() { osStat = prev })
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "mtg" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

func TestController_ArgvShape(t *testing.T) {
	cases := []struct {
		name     string
		run      func(*Controller, context.Context) error
		stubOut  string
		wantName string
		wantArgs []string
	}{
		{
			name:     "Enable calls systemctl enable --now",
			run:      func(c *Controller, ctx context.Context) error { return c.Enable(ctx) },
			wantName: "systemctl",
			wantArgs: []string{"enable", "--now", "mtg.service"},
		},
		{
			name:     "Disable calls systemctl disable --now",
			run:      func(c *Controller, ctx context.Context) error { return c.Disable(ctx) },
			wantName: "systemctl",
			wantArgs: []string{"disable", "--now", "mtg.service"},
		},
		{
			name:     "Restart calls systemctl restart",
			run:      func(c *Controller, ctx context.Context) error { return c.Restart(ctx) },
			wantName: "systemctl",
			wantArgs: []string{"restart", "mtg.service"},
		},
		{
			name:     "DaemonReload calls systemctl daemon-reload",
			run:      func(c *Controller, ctx context.Context) error { return c.DaemonReload(ctx) },
			wantName: "systemctl",
			wantArgs: []string{"daemon-reload"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sink []captured
			fakeExec(t, &sink, tc.stubOut, false)
			stubStat(t, true)

			c := New("", "", "", nil)
			if err := tc.run(c, context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(sink) != 1 {
				t.Fatalf("captured %d invocations, want 1: %+v", len(sink), sink)
			}
			got := sink[0]
			if got.name != tc.wantName {
				t.Fatalf("cmd = %q, want %q", got.name, tc.wantName)
			}
			if !reflect.DeepEqual(got.args, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", got.args, tc.wantArgs)
			}
		})
	}
}

func TestController_IsActive(t *testing.T) {
	stubStat(t, true)
	cases := []struct {
		name    string
		stubOut string
		wantOn  bool
	}{
		{"active", "active\n", true},
		{"inactive", "inactive\n", false},
		{"failed", "failed\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sink []captured
			// is-active exits non-zero for anything but "active" — mirror
			// systemctl behavior so IsActive's error-swallowing branch is
			// exercised.
			fakeExec(t, &sink, tc.stubOut, tc.stubOut != "active\n")

			c := New("", "", "", nil)
			on, err := c.IsActive(context.Background())
			if err != nil {
				t.Fatalf("IsActive err: %v", err)
			}
			if on != tc.wantOn {
				t.Fatalf("IsActive = %v, want %v (out=%q)", on, tc.wantOn, tc.stubOut)
			}
			if len(sink) != 1 || sink[0].name != "systemctl" {
				t.Fatalf("unexpected exec: %+v", sink)
			}
			if !reflect.DeepEqual(sink[0].args, []string{"is-active", "mtg.service"}) {
				t.Fatalf("args = %v", sink[0].args)
			}
		})
	}
}

func TestController_Status_NotInstalled(t *testing.T) {
	stubStat(t, false)
	c := New("", "", "", nil)
	s, err := c.Status(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
	if s != "not-installed" {
		t.Fatalf("status = %q, want not-installed", s)
	}
}

func TestController_GenerateSecret_ArgvAndReturn(t *testing.T) {
	stubStat(t, true)
	var sink []captured
	fakeExec(t, &sink, "abc123SECRETbase64url\n", false)

	c := New("", "", "", nil)
	got, err := c.GenerateSecret(context.Background(), "www.example.com")
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if got != "abc123SECRETbase64url" {
		t.Fatalf("secret = %q, want trimmed stdout", got)
	}
	if len(sink) != 1 {
		t.Fatalf("captured %d, want 1", len(sink))
	}
	if sink[0].name != DefaultBinaryPath {
		t.Fatalf("cmd = %q, want %q", sink[0].name, DefaultBinaryPath)
	}
	want := []string{"generate-secret", "www.example.com"}
	if !reflect.DeepEqual(sink[0].args, want) {
		t.Fatalf("args = %v, want %v", sink[0].args, want)
	}
}

func TestController_GenerateSecret_DefaultsDomain(t *testing.T) {
	stubStat(t, true)
	var sink []captured
	fakeExec(t, &sink, "x\n", false)
	c := New("", "", "", nil)
	if _, err := c.GenerateSecret(context.Background(), ""); err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if len(sink) != 1 || sink[0].args[1] != "www.cloudflare.com" {
		t.Fatalf("empty domain should default to www.cloudflare.com, got %+v", sink)
	}
}

func TestController_GenerateSecret_MissingBinary(t *testing.T) {
	prev := osStat
	osStat = func(_ string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: "stub", Err: os.ErrNotExist}
	}
	t.Cleanup(func() { osStat = prev })

	c := New("", "", "", nil)
	if _, err := c.GenerateSecret(context.Background(), "www.cloudflare.com"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
}

func TestDecodeFrontingDomain(t *testing.T) {
	// Given secret is the example from the deployment doc; base64url
	// no-pad of [0xee | 16-byte-secret | "www.cloudflare.com"].
	const known = "7j85IUlh_jST-sGwIJ3FHRt3d3cuY2xvdWRmbGFyZS5jb20"
	got := DecodeFrontingDomain(known)
	if got != "www.cloudflare.com" {
		t.Fatalf("DecodeFrontingDomain = %q, want www.cloudflare.com", got)
	}

	// Garbage in → "" out.
	if v := DecodeFrontingDomain("not-a-valid-base64-!!!"); v != "" {
		t.Fatalf("garbage → %q, want empty", v)
	}
	// Missing ee-magic → "".
	if v := DecodeFrontingDomain("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); v != "" {
		t.Fatalf("no ee magic → %q, want empty", v)
	}
	// Empty input → "".
	if v := DecodeFrontingDomain(""); v != "" {
		t.Fatalf("empty → %q, want empty", v)
	}
}

func TestReadWriteUnit_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "mtg.service")

	// Skip DaemonReload by capturing but ignoring the exec calls.
	var sink []captured
	fakeExec(t, &sink, "", false)

	c := &Controller{
		BinaryPath: "/usr/local/bin/mtg",
		UnitName:   "mtg.service",
		UnitFile:   unitPath,
	}
	const secret = "7j85IUlh_jST-sGwIJ3FHRt3d3cuY2xvdWRmbGFyZS5jb20"
	if err := c.WriteUnit(context.Background(), "0.0.0.0:2443", secret); err != nil {
		t.Fatalf("WriteUnit: %v", err)
	}

	// Post-condition: unit file exists with ExecStart line we can parse.
	body, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(body), "ExecStart=/usr/local/bin/mtg run --bind 0.0.0.0:2443 "+secret) {
		t.Fatalf("unit body missing ExecStart shape:\n%s", body)
	}

	listen, gotSecret, err := c.ReadUnit(context.Background())
	if err != nil {
		t.Fatalf("ReadUnit: %v", err)
	}
	if listen != "0.0.0.0:2443" {
		t.Fatalf("listen = %q, want 0.0.0.0:2443", listen)
	}
	if gotSecret != secret {
		t.Fatalf("secret = %q, want %q", gotSecret, secret)
	}

	// And daemon-reload was invoked exactly once as part of WriteUnit.
	if len(sink) != 1 || sink[0].name != "systemctl" ||
		!reflect.DeepEqual(sink[0].args, []string{"daemon-reload"}) {
		t.Fatalf("WriteUnit did not run daemon-reload once: %+v", sink)
	}
}

func TestReadUnit_MissingFileReturnsEmpty(t *testing.T) {
	c := &Controller{UnitFile: filepath.Join(t.TempDir(), "no-such.service")}
	listen, secret, err := c.ReadUnit(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil for missing file", err)
	}
	if listen != "" || secret != "" {
		t.Fatalf("expected empty result, got listen=%q secret=%q", listen, secret)
	}
}

func TestWriteUnit_EmptySecretRejected(t *testing.T) {
	c := &Controller{UnitFile: filepath.Join(t.TempDir(), "mtg.service")}
	if err := c.WriteUnit(context.Background(), "0.0.0.0:2443", ""); err == nil {
		t.Fatalf("WriteUnit accepted empty secret")
	}
}

func TestParseExecStart_Variants(t *testing.T) {
	cases := []struct {
		in         string
		wantListen string
		wantSecret string
	}{
		{"/usr/local/bin/mtg run --bind 0.0.0.0:2443 SECRET", "0.0.0.0:2443", "SECRET"},
		{"/usr/local/bin/mtg run -b :2443 SECRET", ":2443", "SECRET"},
		{"/usr/local/bin/mtg run --bind=0.0.0.0:2443 SECRET", "0.0.0.0:2443", "SECRET"},
		{"/usr/local/bin/mtg run SECRET", "", "SECRET"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			listen, secret, err := parseExecStart(tc.in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if listen != tc.wantListen || secret != tc.wantSecret {
				t.Fatalf("got (%q, %q), want (%q, %q)", listen, secret, tc.wantListen, tc.wantSecret)
			}
		})
	}
}

func TestNew_Defaults(t *testing.T) {
	c := New("", "", "", nil)
	if c.BinaryPath != DefaultBinaryPath || c.UnitName != DefaultUnitName || c.UnitFile != DefaultUnitFile {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c2 := New("/opt/mtg", "custom.service", "/tmp/x", nil)
	if c2.BinaryPath != "/opt/mtg" || c2.UnitName != "custom.service" || c2.UnitFile != "/tmp/x" {
		t.Fatalf("overrides ignored: %+v", c2)
	}
}
