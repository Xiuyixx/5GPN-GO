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
	"sync"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/config/render"
)

// ErrApplyInFlight is returned by Apply when another Apply is still
// inside its full lifecycle (render → reload → background health).
// The API layer maps this to HTTP 409.
var ErrApplyInFlight = errors.New("orchestrator: apply in flight")

// Systemd is the real Linux driver: render three-party configs, reload
// their units, run a health probe, roll back on failure.
type Systemd struct {
	Logger        *slog.Logger
	DnsdistPath   string        // default /etc/dnsdist/dnsdist.conf
	MihomoPath    string        // default /etc/mihomo/config.yaml
	SniproxyPath  string        // default /etc/sniproxy.conf
	HealthCheck   func(ctx context.Context) error
	HealthDelay   time.Duration // wait before probe, default 3s
	HealthTimeout time.Duration // probe budget, default 5s

	// HealthObserver, if set, is invoked from the background health
	// goroutine with the final ApplyResult (health-confirmed or
	// rolled-back). Applier uses this to persist apply status.
	HealthObserver func(ctx context.Context, req ApplyRequest, res ApplyResult)

	// Config is the last successfully-applied effective config; used
	// only as a fallback baseline for Rollback. Guarded by configMu.
	Config   *config.Config
	configMu sync.RWMutex

	// applyMu is held for the entire apply lifecycle — render, reload,
	// and the background health-observation goroutine. A concurrent
	// Apply attempt uses TryLock and returns ErrApplyInFlight.
	applyMu sync.Mutex

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
// write new files → reload units → return; a background goroutine then
// waits HealthDelay, probes health, and either commits the new config
// or restores the previous on-disk snapshot + reloads.
//
// Concurrency: Apply is serialized by applyMu. If another Apply is
// still inside its lifecycle (including the background health phase),
// the call returns ErrApplyInFlight.
//
// req.Config is authoritative — s.Config is untouched by this call
// until the background health check confirms.
func (s *Systemd) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	if req.Config == nil {
		return ApplyResult{Reason: "nil config"}, errors.New("systemd: apply request missing config")
	}
	if !s.applyMu.TryLock() {
		return ApplyResult{Reason: "apply-in-flight"}, ErrApplyInFlight
	}
	// applyMu is released either inline on synchronous failure or
	// success-without-health, or from observeHealth on success-with-health.
	s.log().Info("apply: rendering configs", "snapshot", req.SnapshotID)

	prev, err := s.snapshotCurrent()
	if err != nil {
		s.applyMu.Unlock()
		return ApplyResult{Reason: err.Error()}, err
	}

	if err := s.render(req.Config); err != nil {
		s.applyMu.Unlock()
		return ApplyResult{Reason: err.Error()}, err
	}

	if err := s.reloadUnits(ctx); err != nil {
		s.log().Warn("apply: reload failed — restoring previous configs", "err", err)
		_ = s.restore(prev)
		s.applyMu.Unlock()
		return ApplyResult{RolledBack: true, Health: "failed", Reason: err.Error()}, err
	}

	if s.HealthCheck == nil {
		s.setConfig(req.Config)
		s.applyMu.Unlock()
		return ApplyResult{Health: "ok"}, nil
	}

	go s.observeHealth(req, prev)
	return ApplyResult{Health: "observing"}, nil
}

// observeHealth runs the post-reload health probe on an independent
// context (not the request context — the HTTP handler has already
// returned by now, cancelling req ctx would kill this goroutine).
// It unlocks applyMu on exit.
func (s *Systemd) observeHealth(req ApplyRequest, prev snapshotBundle) {
	defer s.applyMu.Unlock()
	// Budget: HealthDelay + HealthTimeout + small slack for rollback reload.
	budget := s.HealthDelay + s.HealthTimeout + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	if s.HealthDelay > 0 {
		select {
		case <-time.After(s.HealthDelay):
		case <-ctx.Done():
			s.log().Warn("apply: health observation aborted before probe", "err", ctx.Err())
			return
		}
	}

	probeCtx, pcancel := context.WithTimeout(ctx, s.HealthTimeout)
	err := s.HealthCheck(probeCtx)
	pcancel()

	res := ApplyResult{}
	if err != nil {
		s.log().Warn("apply: post-reload health probe failed", "err", err)
		if rerr := s.restore(prev); rerr != nil {
			s.log().Error("apply: restore failed during rollback", "err", rerr)
		}
		if rerr := s.reloadUnits(ctx); rerr != nil {
			s.log().Error("apply: reload failed during rollback", "err", rerr)
		}
		res.RolledBack = true
		res.Health = "failed"
		res.Reason = err.Error()
	} else {
		s.setConfig(req.Config)
		res.Health = "ok"
	}

	if s.HealthObserver != nil {
		s.HealthObserver(ctx, req, res)
	}
}

// Rollback restores the last-known-good on-disk config from the
// tarball attached to the given snapshot id — M2 S4 keeps this as a
// convenience wrapper around the same render machinery so the panel
// UI's "roll back to snapshot #N" and health-check failure paths share
// the same code.
func (s *Systemd) Rollback(ctx context.Context, snapshotID int64) error {
	if !s.applyMu.TryLock() {
		return ErrApplyInFlight
	}
	defer s.applyMu.Unlock()
	s.log().Info("rollback: rerendering + reloading", "snapshot", snapshotID)
	s.configMu.RLock()
	cfg := s.Config
	s.configMu.RUnlock()
	if cfg == nil {
		return errors.New("systemd: no base config to rollback to")
	}
	if err := s.render(cfg); err != nil {
		return err
	}
	return s.reloadUnits(ctx)
}

func (s *Systemd) setConfig(cfg *config.Config) {
	s.configMu.Lock()
	s.Config = cfg
	s.configMu.Unlock()
}

// Renderer is the small seam every third-party config generator satisfies.
type Renderer interface {
	Render(*config.Config, io.Writer) error
}

func (s *Systemd) render(cfg *config.Config) error {
	if err := writeRendered(s.DnsdistPath, render.DnsdistRenderer{}, cfg); err != nil {
		return fmt.Errorf("render dnsdist: %w", err)
	}
	if err := writeRendered(s.MihomoPath, render.MihomoRenderer{}, cfg); err != nil {
		return fmt.Errorf("render mihomo: %w", err)
	}
	if err := writeRendered(s.SniproxyPath, render.SniproxyRenderer{}, cfg); err != nil {
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
