package installer

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRealExecutor_MkdirWriteRemove(t *testing.T) {
	tmp := t.TempDir()
	ex := &RealExecutor{Out: io.Discard, Err: io.Discard}

	dir := filepath.Join(tmp, "a", "b")
	if err := ex.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !ex.Exists(dir) {
		t.Errorf("Exists() false right after mkdir")
	}

	target := filepath.Join(dir, "hello.txt")
	if err := ex.WriteFile(target, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "hi" {
		t.Errorf("write round-trip: body=%q err=%v", body, err)
	}

	if err := ex.Remove(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if ex.Exists(dir) {
		t.Errorf("dir still exists after remove")
	}

	// Removing a non-existent path is a no-op (idempotent uninstall).
	if err := ex.Remove(filepath.Join(tmp, "nope")); err != nil {
		t.Errorf("remove-missing should succeed: %v", err)
	}
}

func TestRealExecutor_Run(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/true")
	}
	var buf bytes.Buffer
	ex := &RealExecutor{Out: &buf, Err: &buf}
	if err := ex.Run(context.Background(), "true"); err != nil {
		t.Errorf("run true: %v", err)
	}
	if err := ex.Run(context.Background(), "false"); err == nil {
		t.Errorf("expected run(false) to fail")
	}
}

func TestRealExecutor_Chown_NonLinuxIsNoop(t *testing.T) {
	// Chown returns nil on non-linux (dev machines). On Linux the
	// underlying `chown` may fail without root; we still just assert
	// the call is reachable, not the outcome.
	ex := &RealExecutor{Out: io.Discard, Err: io.Discard}
	err := ex.Chown(t.TempDir(), "nobody", "nobody")
	if runtime.GOOS != "linux" && err != nil {
		t.Errorf("Chown on %s should no-op, got %v", runtime.GOOS, err)
	}
}

func TestRealExecutor_Chmod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := &RealExecutor{Out: io.Discard, Err: io.Discard}
	if err := ex.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%o want=600", got)
	}
}

func TestStatus_NonLinuxPrintsSkip(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this test covers the mac/win path")
	}
	env := Defaults()
	var buf bytes.Buffer
	if err := Status(context.Background(), env, NewRecorder(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "not linux") {
		t.Errorf("expected non-linux notice, got %q", buf.String())
	}
}

func TestUpgrade_SwapsBinaryAndKeepsPrev(t *testing.T) {
	root := t.TempDir()
	env := Defaults().WithRoot(root)
	if err := os.MkdirAll(env.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a "current" binary so Upgrade will take a .prev backup.
	if err := os.WriteFile(env.BinaryPath(), []byte("v-old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Stage the new artefact.
	newBin := filepath.Join(t.TempDir(), "5gpn")
	if err := os.WriteFile(newBin, []byte("v-new"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := NewRecorder()
	rec.Apply = true
	rec.Existing[env.BinaryPath()] = true
	if err := Upgrade(context.Background(), env, rec, UpgradeOptions{
		NewBinary:   newBin,
		SkipRestart: true,
	}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	// The recorder must have written both the .prev backup and the new binary.
	if _, ok := rec.Files[env.BinaryPath()]; !ok {
		t.Errorf("new binary not written")
	}
	if _, ok := rec.Files[env.BinaryPath()+".prev"]; !ok {
		t.Errorf("prev backup not created")
	}
	if string(rec.Files[env.BinaryPath()]) != "v-new" {
		t.Errorf("wrong new-bin body: %q", rec.Files[env.BinaryPath()])
	}
	if string(rec.Files[env.BinaryPath()+".prev"]) != "v-old" {
		t.Errorf("wrong prev body: %q", rec.Files[env.BinaryPath()+".prev"])
	}
}

func TestUpgrade_RequiresNewPath(t *testing.T) {
	err := Upgrade(context.Background(), Defaults(), NewRecorder(), UpgradeOptions{})
	if err == nil || !strings.Contains(err.Error(), "NewBinary") {
		t.Errorf("expected NewBinary error, got %v", err)
	}
}

func TestDistroString_ZeroValueIsUnknown(t *testing.T) {
	if got := (Distro{}).String(); got != "unknown" {
		t.Errorf("zero Distro.String() = %q, want unknown", got)
	}
}
