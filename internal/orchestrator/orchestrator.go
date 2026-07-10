// Package orchestrator owns the "apply a config change" pipeline: render
// third-party configs, reload/restart their systemd units, probe health, and
// roll back on failure. In M1 the concrete Systemd implementation is wired
// in but not exercised on developer machines — a NoOp orchestrator ships
// alongside so the daemon runs end-to-end without dnsdist / mihomo /
// sniproxy actually installed.
package orchestrator

import (
	"context"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// Orchestrator abstracts the data-plane apply loop.
type Orchestrator interface {
	// Apply performs the full pipeline for the given ruleset id. It is
	// responsible for renders, reloads, health probing, and automatic
	// rollback on failure. Returns a summary regardless of success.
	Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error)

	// Rollback restores state as of the given snapshot id.
	Rollback(ctx context.Context, snapshotID int64) error
}

// ApplyRequest carries what the API layer knows about the change.
// Config is the fully-assembled effective config (with EffectiveRules
// pre-populated by core.Assemble) that the orchestrator must render
// during this Apply. It is the sole source of truth for this call —
// s.Config is only used as a fallback baseline for Rollback.
type ApplyRequest struct {
	SnapshotID    int64
	RuleVersionID int64
	Config        *config.Config
	PrevSnapshot  int64 // used for rollback target if health fails
}

// ApplyResult is the summary handed back to the API layer.
type ApplyResult struct {
	RolledBack bool
	Health     string
	Reason     string
}
