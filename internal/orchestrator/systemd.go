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
	DnsdistPath   string // default /etc/dnsdist/dnsdist.conf
	MihomoPath    string // default /etc/mihomo/config.yaml
	SniproxyPath  string // default /etc/sniproxy.conf
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

	// Overrides are test seams for deterministic filesystem/systemctl failures.
	renderFileOverride func(string, Renderer, *config.Config) error
	restoreOverride    func(snapshotBundle) error
	reloadOverride     func(context.Context) error
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
		restoreErr := s.restore(prev)
		if restoreErr != nil {
			restoreErr = fmt.Errorf("restore after render failure: %w", restoreErr)
		}
		result, finalErr := failedApplyResult(err, restoreErr)
		s.applyMu.Unlock()
		return result, finalErr
	}

	if err := s.reloadUnits(ctx); err != nil {
		s.log().Warn("apply: reload failed — restoring previous configs", "err", err)
		rollbackErr := s.restoreAndReload(ctx, prev)
		if rollbackErr != nil {
			s.log().Error("apply: rollback failed after reload error", "err", rollbackErr)
		}
		result, finalErr := failedApplyResult(err, rollbackErr)
		s.applyMu.Unlock()
		return result, finalErr
	}

	if s.HealthCheck == nil {
		if rolledBack, err := s.commitOrRestore(ctx, req, prev); err != nil {
			s.applyMu.Unlock()
			return ApplyResult{RolledBack: rolledBack, Health: "failed", Reason: err.Error()}, err
		}
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
		rollbackErr := s.restoreAndReload(ctx, prev)
		if rollbackErr != nil {
			s.log().Error("apply: rollback failed after health error", "err", rollbackErr)
		}
		res, _ = failedApplyResult(err, rollbackErr)
	} else if rolledBack, err := s.commitOrRestore(ctx, req, prev); err != nil {
		res = ApplyResult{RolledBack: rolledBack, Health: "failed", Reason: err.Error()}
	} else {
		s.setConfig(req.Config)
		res.Health = "ok"
	}

	if s.HealthObserver != nil {
		s.HealthObserver(ctx, req, res)
	}
}

func (s *Systemd) commitOrRestore(ctx context.Context, req ApplyRequest, prev snapshotBundle) (bool, error) {
	if req.Commit == nil {
		return false, nil
	}
	if err := req.Commit(ctx); err != nil {
		s.log().Error("apply: control-plane commit failed — restoring previous configs", "err", err)
		commitErr := fmt.Errorf("control-plane commit: %w", err)
		if rollbackErr := s.restoreAndReload(ctx, prev); rollbackErr != nil {
			return false, errors.Join(commitErr, rollbackErr)
		}
		return true, commitErr
	}
	return false, nil
}

func failedApplyResult(primary, rollbackErr error) (ApplyResult, error) {
	if rollbackErr == nil {
		return ApplyResult{RolledBack: true, Health: "failed", Reason: primary.Error()}, primary
	}
	combined := errors.Join(primary, rollbackErr)
	return ApplyResult{Health: "failed", Reason: combined.Error()}, combined
}

func (s *Systemd) restoreAndReload(ctx context.Context, prev snapshotBundle) error {
	var restoreErr, reloadErr error
	if err := s.restore(prev); err != nil {
		restoreErr = fmt.Errorf("restore previous configs: %w", err)
	}
	if err := s.reloadUnits(ctx); err != nil {
		reloadErr = fmt.Errorf("reload previous configs: %w", err)
	}
	return errors.Join(restoreErr, reloadErr)
}

// Rollback re-renders and reloads the in-memory last-known-good Config.
// snapshotID is logged for correlation; this implementation does not load a
// snapshot archive by id. The core layer selects the snapshot's rule version
// and publishes the resulting Config before calling this method.
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
	prev, err := s.snapshotCurrent()
	if err != nil {
		return fmt.Errorf("snapshot current configs before rollback: %w", err)
	}
	if err := s.render(cfg); err != nil {
		return s.compensateRollbackFailure(ctx, prev, fmt.Errorf("render rollback configs: %w", err))
	}
	if err := s.reloadUnits(ctx); err != nil {
		return s.compensateRollbackFailure(ctx, prev, fmt.Errorf("reload rollback configs: %w", err))
	}
	return nil
}

func (s *Systemd) compensateRollbackFailure(ctx context.Context, prev snapshotBundle, primary error) error {
	if compensationErr := s.restoreAndReload(ctx, prev); compensationErr != nil {
		return errors.Join(primary, fmt.Errorf("compensate rollback failure: %w", compensationErr))
	}
	return primary
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
	if err := s.renderFile(s.DnsdistPath, render.DnsdistRenderer{}, cfg); err != nil {
		return fmt.Errorf("render dnsdist: %w", err)
	}
	if err := s.renderFile(s.MihomoPath, render.MihomoRenderer{}, cfg); err != nil {
		return fmt.Errorf("render mihomo: %w", err)
	}
	if err := s.renderFile(s.SniproxyPath, render.SniproxyRenderer{}, cfg); err != nil {
		return fmt.Errorf("render sniproxy: %w", err)
	}
	return nil
}

func (s *Systemd) renderFile(path string, r Renderer, cfg *config.Config) error {
	if s.renderFileOverride != nil {
		return s.renderFileOverride(path, r, cfg)
	}
	return writeRendered(path, r, cfg)
}

// writeRendered renders into a buffer first so a template error never
// leaves a half-written file on disk, then atomic-renames into place.
func writeRendered(path string, r Renderer, cfg *config.Config) error {
	buf := &bytes.Buffer{}
	if err := r.Render(cfg, buf); err != nil {
		return err
	}
	return writeBytesAtomic(path, buf.Bytes(), 0o644)
}

type snapshotFile struct {
	body   []byte
	exists bool
	mode   os.FileMode
}

type snapshotBundle struct {
	dnsdist, mihomo, sniproxy snapshotFile
}

func (s *Systemd) snapshotCurrent() (snapshotBundle, error) {
	dnsdist, err := snapshotPath(s.DnsdistPath)
	if err != nil {
		return snapshotBundle{}, err
	}
	mihomo, err := snapshotPath(s.MihomoPath)
	if err != nil {
		return snapshotBundle{}, err
	}
	sniproxy, err := snapshotPath(s.SniproxyPath)
	if err != nil {
		return snapshotBundle{}, err
	}
	return snapshotBundle{dnsdist: dnsdist, mihomo: mihomo, sniproxy: sniproxy}, nil
}

func snapshotPath(path string) (snapshotFile, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return snapshotFile{}, nil
	}
	if err != nil {
		return snapshotFile{}, fmt.Errorf("snapshot %s: %w", path, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return snapshotFile{}, fmt.Errorf("snapshot %s: %w", path, err)
	}
	return snapshotFile{body: body, exists: true, mode: info.Mode().Perm()}, nil
}

func (s *Systemd) restore(prev snapshotBundle) error {
	if s.restoreOverride != nil {
		return s.restoreOverride(prev)
	}
	var restoreErrs []error
	for _, item := range []struct {
		path string
		file snapshotFile
	}{
		{s.DnsdistPath, prev.dnsdist},
		{s.MihomoPath, prev.mihomo},
		{s.SniproxyPath, prev.sniproxy},
	} {
		if !item.file.exists {
			if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
				restoreErrs = append(restoreErrs, fmt.Errorf("remove newly rendered %s: %w", item.path, err))
			}
			continue
		}
		mode := item.file.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := writeBytesAtomic(item.path, item.file.body, mode); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore %s: %w", item.path, err))
		}
	}
	return errors.Join(restoreErrs...)
}

func writeBytesAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".5gpn-render-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
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
