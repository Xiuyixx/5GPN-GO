package installer

import (
	"context"
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
		"CAP_NET_BIND_SERVICE",
		"ExecStart=/usr/local/bin/5gpn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unit missing %q\n%s", want, got)
		}
	}
}

func TestDoctor_ReportsChecks(t *testing.T) {
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	report := Doctor(context.Background(), env, rec)
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
