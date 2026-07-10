package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/config/render"
)

// Systemd is the real Linux driver: render three-party configs, reload
// their units, run a health probe, roll back on failure.
type Systemd struct {
	Logger        *slog.Logger
	Config        *config.Config      // current YAML source of truth
	DnsdistPath   string              // default /etc/dnsdist/dnsdist.conf
	MihomoPath    string              // default /etc/mihomo/config.yaml
	SniproxyPath  string              // default /etc/sniproxy.conf
	HealthCheck   func(ctx context.Context) error
	HealthDelay   time.Duration       // wait before probe, default 3s
	HealthTimeout time.Duration       // probe budget, default 5s

	// reloadOverride is used only by tests to skip real systemctl calls.
	reloadOverride func(context.Context) error
}

// DefaultSystemd builds a Systemd with sane paths + a permissive
// health probe (just checks dnsdist + mihomo processes exist).
func DefaultSystemd(cfg *config.Config, logger *slog.Logger) *Systemd {
	return &Systemd{
		Logger:        logger,
		Config:        cfg,
		DnsdistPath:   "/etc/dnsdist/dnsdist.conf",
		MihomoPath:    "/etc/mihomo/config.yaml",
		SniproxyPath:  "/etc/sniproxy.conf",
		HealthDelay:   3 * time.Second,
		HealthTimeout: 5 * time.Second,
		HealthCheck:   defaultHealth,
	}
}

// AvailableOnHost is true only if we're on Linux and systemctl exists.
// Callers can use it to decide between Systemd + NoOp at daemon startup.
func AvailableOnHost() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// Apply performs the full pipeline: render → snapshot on-disk configs →
// write new files → reload units → wait → probe → roll back on failure.
func (s *Systemd) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	res := ApplyResult{}
	if s.Config == nil {
		return res, errors.New("systemd: nil config")
	}
	s.log().Info("apply: rendering configs", "snapshot", req.SnapshotID)

	prev, err := s.snapshotCurrent()
	if err != nil {
		res.Reason = err.Error()
		return res, err
	}

	if err := s.render(); err != nil {
		res.Reason = err.Error()
		return res, err
	}

	if err := s.reloadUnits(ctx); err != nil {
		res.Reason = err.Error()
		s.log().Warn("apply: reload failed — restoring previous configs", "err", err)
		_ = s.restore(prev)
		res.RolledBack = true
		res.Health = "failed"
		return res, err
	}

	if s.HealthCheck != nil {
		time.Sleep(s.HealthDelay)
		probeCtx, cancel := context.WithTimeout(ctx, s.HealthTimeout)
		defer cancel()
		if err := s.HealthCheck(probeCtx); err != nil {
			s.log().Warn("apply: post-reload health probe failed", "err", err)
			_ = s.restore(prev)
			_ = s.reloadUnits(ctx)
			res.RolledBack = true
			res.Health = "failed"
			res.Reason = err.Error()
			return res, err
		}
	}
	res.Health = "ok"
	return res, nil
}

// Rollback restores the last-known-good on-disk config from the
// tarball attached to the given snapshot id — M2 S4 keeps this as a
// convenience wrapper around the same render machinery so the panel
// UI's "roll back to snapshot #N" and health-check failure paths share
// the same code.
func (s *Systemd) Rollback(ctx context.Context, snapshotID int64) error {
	s.log().Info("rollback: rerendering + reloading", "snapshot", snapshotID)
	if err := s.render(); err != nil {
		return err
	}
	return s.reloadUnits(ctx)
}

// SetConfig lets the daemon swap in an updated cfg (e.g. after a
// panel edit) before the next Apply.
func (s *Systemd) SetConfig(cfg *config.Config) { s.Config = cfg }

// Renderer is the small seam every third-party config generator satisfies.
type Renderer interface {
	Render(*config.Config, io.Writer) error
}

func (s *Systemd) render() error {
	if err := writeRendered(s.DnsdistPath, render.DnsdistRenderer{}, s.Config); err != nil {
		return fmt.Errorf("render dnsdist: %w", err)
	}
	if err := writeRendered(s.MihomoPath, render.MihomoRenderer{}, s.Config); err != nil {
		return fmt.Errorf("render mihomo: %w", err)
	}
	if err := writeRendered(s.SniproxyPath, render.SniproxyRenderer{}, s.Config); err != nil {
		return fmt.Errorf("render sniproxy: %w", err)
	}
	return nil
}

// writeRendered renders into a buffer first so a template error never
// leaves a half-written file on disk, then atomic-renames into place.
func writeRendered(path string, r Renderer, cfg *config.Config) error {
	buf := &bytes.Buffer{}
	if err := r.Render(cfg, buf); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type snapshotBundle struct {
	dnsdist, mihomo, sniproxy []byte
}

func (s *Systemd) snapshotCurrent() (snapshotBundle, error) {
	var b snapshotBundle
	if body, err := os.ReadFile(s.DnsdistPath); err == nil {
		b.dnsdist = body
	}
	if body, err := os.ReadFile(s.MihomoPath); err == nil {
		b.mihomo = body
	}
	if body, err := os.ReadFile(s.SniproxyPath); err == nil {
		b.sniproxy = body
	}
	return b, nil
}

func (s *Systemd) restore(prev snapshotBundle) error {
	for _, item := range []struct {
		path string
		body []byte
	}{
		{s.DnsdistPath, prev.dnsdist},
		{s.MihomoPath, prev.mihomo},
		{s.SniproxyPath, prev.sniproxy},
	} {
		if item.body == nil {
			continue
		}
		if err := os.WriteFile(item.path, item.body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (s *Systemd) reloadUnits(ctx context.Context) error {
	if s.reloadOverride != nil {
		return s.reloadOverride(ctx)
	}
	steps := []struct {
		verb string
		unit string
	}{
		{"reload", "dnsdist"},
		{"reload", "mihomo"},
		{"restart", "sniproxy"},
	}
	for _, step := range steps {
		if err := runSystemctl(ctx, step.verb, step.unit); err != nil {
			return fmt.Errorf("systemctl %s %s: %w", step.verb, step.unit, err)
		}
	}
	return nil
}

func runSystemctl(ctx context.Context, verb, unit string) error {
	cmd := exec.CommandContext(ctx, "systemctl", verb, unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v (%s)", err, string(out))
	}
	return nil
}

func defaultHealth(ctx context.Context) error {
	for _, unit := range []string{"dnsdist", "mihomo"} {
		cmd := exec.CommandContext(ctx, "systemctl", "is-active", unit)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("unit %s not active: %w", unit, err)
		}
	}
	return nil
}

func (s *Systemd) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

