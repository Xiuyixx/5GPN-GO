// Package installer implements the M3 replacement for the legacy 3.3k-line
// install.sh. Each subcommand (install / upgrade / uninstall / status /
// doctor) is a plain function that takes an Env + Executor so the same code
// path runs in production (real syscalls) and in tests (recorded operations).
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

// Env describes the target filesystem layout. Zero values fall back to the
// standard 5GPN paths so callers only override in tests.
type Env struct {
	BinDir    string // /usr/local/bin
	ConfigDir string // /etc/5gpn
	DataDir   string // /var/lib/5gpn
	UnitDir   string // /etc/systemd/system
	User      string // 5gpn
	Group     string // 5gpn
	Binary    string // 5gpn
	Unit      string // 5gpn.service
	Stdout    io.Writer
	Stderr    io.Writer
}

// Defaults returns the production layout.
func Defaults() Env {
	return Env{
		BinDir:    "/usr/local/bin",
		ConfigDir: "/etc/5gpn",
		DataDir:   "/var/lib/5gpn",
		UnitDir:   "/etc/systemd/system",
		User:      "5gpn",
		Group:     "5gpn",
		Binary:    "5gpn",
		Unit:      "5gpn.service",
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}
}

// WithRoot re-roots every path under a directory. Used in tests so the whole
// install runs inside a t.TempDir() sandbox.
func (e Env) WithRoot(root string) Env {
	out := e
	out.BinDir = filepath.Join(root, e.BinDir)
	out.ConfigDir = filepath.Join(root, e.ConfigDir)
	out.DataDir = filepath.Join(root, e.DataDir)
	out.UnitDir = filepath.Join(root, e.UnitDir)
	return out
}

// BinaryPath is where the daemon binary lives.
func (e Env) BinaryPath() string { return filepath.Join(e.BinDir, e.Binary) }

// ConfigPath is where the panel config lives.
func (e Env) ConfigPath() string { return filepath.Join(e.ConfigDir, "config.yaml") }

// UnitPath is where the systemd unit lives.
func (e Env) UnitPath() string { return filepath.Join(e.UnitDir, e.Unit) }

// Executor abstracts the two operations that mutate the host: running a
// command and modifying the filesystem. The RealExecutor drives real
// syscalls; RecordingExecutor collects them for tests + --dry-run.
type Executor interface {
	Run(ctx context.Context, name string, args ...string) error
	WriteFile(path string, data []byte, mode os.FileMode) error
	MkdirAll(path string, mode os.FileMode) error
	Remove(path string) error
	Chown(path, user, group string) error
	Chmod(path string, mode os.FileMode) error
	Exists(path string) bool
}

// RealExecutor runs the actions for real.
type RealExecutor struct {
	Out io.Writer
	Err io.Writer
}

func (r *RealExecutor) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Err
	return cmd.Run()
}

func (r *RealExecutor) WriteFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}

func (r *RealExecutor) MkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (r *RealExecutor) Remove(path string) error {
	err := os.RemoveAll(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (r *RealExecutor) Chown(path, user, group string) error {
	// chown is deferred to the `chown` binary so we do not have to resolve
	// UIDs ourselves; skipped on non-linux to keep dev machines usable.
	if runtime.GOOS != "linux" {
		return nil
	}
	return exec.Command("chown", "-R", fmt.Sprintf("%s:%s", user, group), path).Run()
}

func (r *RealExecutor) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

func (r *RealExecutor) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
