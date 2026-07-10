package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// Systemd runs the real reload/restart pipeline against dnsdist / mihomo /
// sniproxy on a Linux host. M1 leaves the config render layer as
// ErrNotImplemented, so this driver focuses on the reload + probe + rollback
// mechanics; M2 wires the render step in front of it.
type Systemd struct {
	Logger        *slog.Logger
	HealthCheck   func(ctx context.Context) error
	HealthDelay   time.Duration
	HealthTimeout time.Duration
}

// Apply reloads dnsdist and mihomo, restarts sniproxy, probes health, and
// rolls back to the previous snapshot when the probe fails.
func (s *Systemd) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	res := ApplyResult{}
	if err := s.reloadUnits(ctx); err != nil {
		res.Reason = err.Error()
		return res, err
	}
	if s.HealthCheck != nil {
		time.Sleep(s.HealthDelay)
		probeCtx, cancel := context.WithTimeout(ctx, s.HealthTimeout)
		defer cancel()
		if err := s.HealthCheck(probeCtx); err != nil {
			s.log().Warn("post-apply health probe failed; rolling back",
				"snapshot_id", req.SnapshotID, "prev", req.PrevSnapshot, "err", err)
			res.RolledBack = true
			res.Health = "failed"
			res.Reason = err.Error()
			if rbErr := s.Rollback(ctx, req.PrevSnapshot); rbErr != nil {
				return res, fmt.Errorf("apply failed and rollback failed: apply=%v rollback=%w", err, rbErr)
			}
			return res, err
		}
	}
	res.Health = "ok"
	return res, nil
}

// Rollback restores the previous snapshot by reloading units after the
// caller has restored the rendered config files (M2 will render prev
// files here; M1 assumes files are still on disk from the pre-apply state).
func (s *Systemd) Rollback(ctx context.Context, snapshotID int64) error {
	s.log().Info("rollback: reloading units", "snapshot_id", snapshotID)
	return s.reloadUnits(ctx)
}

func (s *Systemd) reloadUnits(ctx context.Context) error {
	steps := []struct {
		unit string
		verb string
	}{
		{"dnsdist", "reload"},
		{"mihomo", "reload"}, // mihomo REST reload lands in M2; systemctl reload used until then
		{"sniproxy", "restart"},
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
		return fmt.Errorf("%s (%s)", err, string(out))
	}
	return nil
}

func (s *Systemd) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}
