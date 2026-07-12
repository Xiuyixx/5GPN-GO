package orchestrator

import (
	"context"
	"log/slog"
)

// NoOp is used on developer machines (macOS, containers without dnsdist etc.)
// and in tests. It logs everything and never fails, so the API layer can
// exercise the full apply → snapshot → rollback path without a real data
// plane installed.
type NoOp struct {
	Logger *slog.Logger
}

// Apply logs the request and returns success.
func (n *NoOp) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	n.log().Info("noop-orchestrator: apply",
		"snapshot_id", req.SnapshotID, "rule_version_id", req.RuleVersionID)
	if req.Commit != nil {
		if err := req.Commit(ctx); err != nil {
			return ApplyResult{RolledBack: true, Health: "failed", Reason: err.Error()}, err
		}
	}
	return ApplyResult{Health: "ok (noop)"}, nil
}

// Rollback logs and returns success.
func (n *NoOp) Rollback(_ context.Context, snapshotID int64) error {
	n.log().Info("noop-orchestrator: rollback", "snapshot_id", snapshotID)
	return nil
}

func (n *NoOp) log() *slog.Logger {
	if n.Logger == nil {
		return slog.Default()
	}
	return n.Logger
}
