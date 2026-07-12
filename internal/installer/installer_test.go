package installer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstall_LaysDownEveryStep(t *testing.T) {
	ctx := context.Background()
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true

	if err := Install(ctx, env, rec, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}

	want := []string{env.BinDir, env.ConfigDir, env.DataDir, env.UnitDir}
	for _, d := range want {
		if !rec.Dirs[d] {
			t.Errorf("expected mkdir %s", d)
		}
	}
	if _, ok := rec.Files[env.ConfigPath()]; !ok {
		t.Errorf("expected config written at %s", env.ConfigPath())
	}
	if _, ok := rec.Files[env.UnitPath()]; !ok {
		t.Errorf("expected unit at %s", env.UnitPath())
	}
	if runtime.GOOS == "linux" {
		found := false
		for _, op := range rec.Ops {
			if op.Kind == "run" && op.Cmd == "systemctl" && len(op.Args) > 0 && op.Args[0] == "enable" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected systemctl enable on linux")
		}
	}
}

func TestInstall_ForceRewritesConfig(t *testing.T) {
	ctx := context.Background()
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true
	// Pre-populate the config so the first install treats it as existing.
	rec.Files[env.ConfigPath()] = []byte("existing")
	rec.Existing[env.ConfigPath()] = true

	// Non-force run: config must be preserved.
	if err := Install(ctx, env, rec, InstallOptions{SkipUnit: true, SkipEnable: true}); err != nil {
		t.Fatal(err)
	}
	if string(rec.Files[env.ConfigPath()]) != "existing" {
		t.Errorf("non-force install overwrote config")
	}

	// Force: config rewritten with default template.
	if err := Install(ctx, env, rec, InstallOptions{Force: true, SkipUnit: true, SkipEnable: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rec.Files[env.ConfigPath()]), "panel_port") {
		t.Errorf("force install did not rewrite: %s", rec.Files[env.ConfigPath()])
	}
}

func TestUninstall_PurgeRemovesState(t *testing.T) {
	ctx := context.Background()
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true
	rec.Existing[env.BinaryPath()] = true
	rec.Existing[env.UnitPath()] = true
	rec.Existing[env.ConfigDir] = true
	rec.Existing[env.DataDir] = true

	if err := Uninstall(ctx, env, rec, true); err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, op := range rec.Ops {
		if op.Kind == "remove" {
			removed[op.Path] = true
		}
	}
	for _, p := range []string{env.UnitPath(), env.BinaryPath(), env.ConfigDir, env.DataDir} {
		if !removed[p] {
			t.Errorf("expected remove %s", p)
		}
	}
}

func TestUninstall_NoPurgeKeepsConfig(t *testing.T) {
	ctx := context.Background()
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true

	if err := Uninstall(ctx, env, rec, false); err != nil {
		t.Fatal(err)
	}
	for _, op := range rec.Ops {
		if op.Kind == "remove" && (op.Path == env.ConfigDir || op.Path == env.DataDir) {
			t.Errorf("no-purge should not remove %s", op.Path)
		}
	}
}

// TestInstall_WritesSourceBinary is the regression that guards the
// install.sh → 5gpn-installer contract: install.sh downloads the daemon
// binary and passes its path via --source-binary; installer must copy
// that file to env.BinaryPath so a fresh curl-bash install actually
// leaves a runnable daemon on disk. Without this the systemd unit
// would fail with status=203/EXEC (no executable at ExecStart).
func TestInstall_WritesSourceBinary(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	src := tmp + "/downloaded-daemon"
	daemonBody := []byte("#!/bin/sh\necho fake 5gpn daemon\n")
	if err := os.WriteFile(src, daemonBody, 0o755); err != nil {
		t.Fatalf("prep source: %v", err)
	}

	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true

	if err := Install(ctx, env, rec, InstallOptions{SourceBinary: src}); err != nil {
		t.Fatalf("install: %v", err)
	}
	got, ok := rec.Files[env.BinaryPath()]
	if !ok {
		t.Fatalf("expected binary written at %s (got files %v)", env.BinaryPath(), keys(rec.Files))
	}
	if string(got) != string(daemonBody) {
		t.Errorf("binary body mismatch:\n got=%q\nwant=%q", got, daemonBody)
	}
}

func TestInstall_SecuresConfigAndStateTrees(t *testing.T) {
	ctx := context.Background()
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true

	if err := Install(ctx, env, rec, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, want := rec.Owners[env.ConfigDir], "root:"+env.Group; got != want {
		t.Errorf("expected chown %s → %s, got %q (owners=%v)",
			env.ConfigDir, want, got, rec.Owners)
	}
	if got, want := rec.Owners[env.DataDir], env.User+":"+env.Group; got != want {
		t.Errorf("expected chown %s → %s, got %q", env.DataDir, want, got)
	}
	for path, want := range map[string]os.FileMode{
		env.ConfigDir:    0o750,
		env.ConfigPath(): 0o640,
		env.DataDir:      0o700,
	} {
		if got := rec.Modes[path]; got != want {
			t.Errorf("mode %s=%o want=%o", path, got, want)
		}
	}
}

func TestInstall_TightensExistingSQLiteFiles(t *testing.T) {
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true
	for _, name := range []string{"5gpn.db", "5gpn.db-wal", "5gpn.db-shm", "jwt.key"} {
		rec.Existing[filepath.Join(env.DataDir, name)] = true
	}
	if err := Install(context.Background(), env, rec, InstallOptions{SkipUnit: true, SkipEnable: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"5gpn.db", "5gpn.db-wal", "5gpn.db-shm", "jwt.key"} {
		path := filepath.Join(env.DataDir, name)
		if got := rec.Modes[path]; got != 0o600 {
			t.Errorf("mode %s=%o want=600", path, got)
		}
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestUnitRender_HasHardening(t *testing.T) {
	env := Defaults()
	body, err := Render(UnitTemplate, env)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"UMask=0077",
		"CAP_NET_BIND_SERVICE",
		"ExecStart=/usr/local/bin/5gpn",
		"ReadWritePaths=/var/lib/5gpn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unit missing %q\n%s", want, got)
		}
	}
}

func TestDoctor_ReportsChecks(t *testing.T) {
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	report := Doctor(context.Background(), env, rec, Distro{})
	if len(report.Checks) == 0 {
		t.Fatal("doctor produced no checks")
	}
	names := map[string]bool{}
	for _, c := range report.Checks {
		names[c.Name] = true
	}
	if !names["bin dir writable"] {
		t.Errorf("missing bin dir check")
	}
}

func TestDoctor_WithDistroAddsRecognizedCheck(t *testing.T) {
	env := Defaults().WithRoot(t.TempDir())
	d, err := LoadOSFixture("testdata/os-release/ubuntu-24.04")
	if err != nil {
		t.Fatal(err)
	}
	rep := Doctor(context.Background(), env, NewRecorder(), d)
	var found bool
	for _, c := range rep.Checks {
		if c.Name == "distro recognized" {
			found = true
			if !c.OK {
				t.Errorf("ubuntu-24.04 should be OK, detail=%q", c.Detail)
			}
		}
	}
	if !found {
		t.Errorf("distro-recognized check missing when Doctor is given a distro")
	}
}
