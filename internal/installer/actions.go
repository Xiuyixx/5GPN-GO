package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
		fmt.Fprintln(out, "status: not linux — nothing to report")
		return nil
	}
	if err := ex.Run(ctx, "systemctl", "is-active", env.Unit); err != nil {
		fmt.Fprintln(out, "unit: inactive")
	} else {
		fmt.Fprintln(out, "unit: active")
	}
	return nil
}

// DoctorReport summarizes doctor findings.
type DoctorReport struct {
	Checks []DoctorCheck
}

// DoctorCheck is one boolean assertion about the host.
type DoctorCheck struct {
	Name    string
	OK      bool
	Detail  string
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
	// useradd is idempotent-ish: -r creates a system user, we ignore
	// "already exists" via the exit code the shell would swallow.
	_ = ex.Run(ctx, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", env.User)
	return nil
}

func ensureDirs(env Env, ex Executor) error {
	for _, d := range []string{env.BinDir, env.ConfigDir, env.DataDir, env.UnitDir} {
		if err := ex.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return ex.Chown(env.DataDir, env.User, env.Group)
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
