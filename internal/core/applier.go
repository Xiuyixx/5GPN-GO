package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// Applier is the single "produce a variation" service. Every entry point
// that changes the data plane — API rules apply, backup import, rollback,
// boot restore — flows through here. It owns the mapping between an
// in-flight orchestrator.Apply call and the apply_status DB row that the
// panel UI polls.
type Applier struct {
	DB         *sql.DB
	BaseConfig *config.Config
	Store      Store
	Orch       orchestrator.Orchestrator
	Logger     *slog.Logger

	mu       sync.Mutex
	inflight map[int64]int64 // snapshot_id -> apply_status.id
}

// ApplyResult is the API-facing summary of a single Apply.
type ApplyResult struct {
	SnapshotID    int64
	RuleVersionID int64
	RolledBack    bool
	Health        string
	Reason        string
}

// ApplyStatusSnapshot is the JSON payload returned by GET /apply/status.
type ApplyStatusSnapshot struct {
	ID         int64  `json:"id"`
	SnapshotID int64  `json:"snapshot_id"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// ApplyRules is the API-triggered path: caller has already written the
// snapshot + rule_versions rows, we assemble the effective config and
// hand it to the orchestrator. The apply_status row is created before
// dispatch and updated from OnHealth when the background probe lands.
func (a *Applier) ApplyRules(ctx context.Context, snapshotID, ruleVersionID, prevSnapshot int64) (ApplyResult, error) {
	if a.Store == nil {
		return ApplyResult{}, errors.New("applier: nil store")
	}
	if a.Orch == nil {
		return ApplyResult{}, errors.New("applier: nil orchestrator")
	}

	cfg, err := Assemble(a.BaseConfig, a.Store)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: assemble: %w", err)
	}

	statusID, err := db.InsertApplyStatus(a.DB, snapshotID, "")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: insert apply_status: %w", err)
	}
	a.registerInflight(snapshotID, statusID)

	res, appErr := a.Orch.Apply(ctx, orchestrator.ApplyRequest{
		SnapshotID:    snapshotID,
		RuleVersionID: ruleVersionID,
		Config:        cfg,
		PrevSnapshot:  prevSnapshot,
	})

	result := ApplyResult{
		SnapshotID:    snapshotID,
		RuleVersionID: ruleVersionID,
		RolledBack:    res.RolledBack,
		Health:        res.Health,
		Reason:        res.Reason,
	}

	if errors.Is(appErr, orchestrator.ErrApplyInFlight) {
		// keep apply_status as 'submitted' with a diagnostic reason so
		// the panel can distinguish "queued lost" from "still applying".
		_ = db.UpdateApplyStatus(a.DB, statusID, "submitted", "apply-in-flight")
		a.clearInflight(snapshotID)
		return result, appErr
	}
	if appErr != nil {
		// Synchronous failure: orchestrator restored disk before returning.
		_ = db.UpdateApplyStatus(a.DB, statusID, "rolled_back", appErr.Error())
		a.clearInflight(snapshotID)
		return result, appErr
	}

	switch res.Health {
	case "observing":
		// Terminal state will land via OnHealth from the bg goroutine.
	case "":
		// Empty health means orchestrator gave no signal — treat as confirmed.
		_ = db.UpdateApplyStatus(a.DB, statusID, "confirmed", "")
		a.clearInflight(snapshotID)
	default:
		// NoOp / no-health-check path: commit immediately.
		state := "confirmed"
		if res.RolledBack {
			state = "rolled_back"
		}
		_ = db.UpdateApplyStatus(a.DB, statusID, state, res.Reason)
		a.clearInflight(snapshotID)
	}

	return result, nil
}

// ImportRules is the S3 preview: parse + validate a YAML body, persist a
// new snapshot + rule_versions row (active), then run through ApplyRules.
// Kept minimal in this batch — S3 will wire the audit trail.
func (a *Applier) ImportRules(ctx context.Context, rulesYAML, actor, ip string) (ApplyResult, error) {
	set, err := rules.ParseYAML([]byte(rulesYAML))
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier.import: parse: %w", err)
	}
	if err := set.Validate(); err != nil {
		return ApplyResult{}, fmt.Errorf("applier.import: validate: %w", err)
	}

	snapID, err := db.InsertSnapshot(a.DB, db.Snapshot{
		ConfigHash: rules.HashRules(set),
		Note:       fmt.Sprintf("import by %s", actor),
	})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier.import: snapshot: %w", err)
	}
	rvID, err := db.InsertRuleVersion(a.DB, snapID, rulesYAML, true)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier.import: rule_version: %w", err)
	}

	_ = db.AppendAudit(a.DB, db.AuditEntry{
		Actor:  actor,
		Action: "rules.import",
		Target: rules.HashRules(set),
		After:  rulesYAML,
		Result: "ok",
		IP:     ip,
	})

	return a.ApplyRules(ctx, snapID, rvID, 0)
}

// Status returns the most-recent apply lifecycle row as a JSON-friendly
// snapshot. Returns zero-value ApplyStatusSnapshot when the table is empty.
func (a *Applier) Status(ctx context.Context) (ApplyStatusSnapshot, error) {
	row, err := db.LatestApplyStatus(a.DB)
	if err != nil {
		return ApplyStatusSnapshot{}, err
	}
	if row == nil {
		return ApplyStatusSnapshot{}, nil
	}
	return ApplyStatusSnapshot{
		ID:         row.ID,
		SnapshotID: row.SnapshotID,
		State:      row.State,
		Reason:     row.Reason,
		CreatedAt:  row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:  row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

// OnHealth is the orchestrator.Systemd.HealthObserver callback. Wire it via
// `orch.HealthObserver = applier.OnHealth` in main.go once the orchestrator
// is a *Systemd. It flips the apply_status row that ApplyRules created.
func (a *Applier) OnHealth(ctx context.Context, req orchestrator.ApplyRequest, res orchestrator.ApplyResult) {
	statusID, ok := a.takeInflight(req.SnapshotID)
	if !ok {
		a.log().Warn("applier.OnHealth: no in-flight apply_status row",
			"snapshot_id", req.SnapshotID)
		return
	}
	state := "confirmed"
	if res.RolledBack {
		state = "rolled_back"
	}
	if err := db.UpdateApplyStatus(a.DB, statusID, state, res.Reason); err != nil {
		a.log().Error("applier.OnHealth: update apply_status failed",
			"snapshot_id", req.SnapshotID, "err", err)
	}
	if state == "rolled_back" {
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "rules.apply.rolled_back",
			Target: fmt.Sprintf("snapshot=%d", req.SnapshotID),
			Result: res.Reason,
		})
	} else {
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "rules.apply.confirmed",
			Target: fmt.Sprintf("snapshot=%d", req.SnapshotID),
			Result: "ok",
		})
	}
}

func (a *Applier) registerInflight(snapshotID, statusID int64) {
	a.mu.Lock()
	if a.inflight == nil {
		a.inflight = map[int64]int64{}
	}
	a.inflight[snapshotID] = statusID
	a.mu.Unlock()
}

func (a *Applier) clearInflight(snapshotID int64) {
	a.mu.Lock()
	delete(a.inflight, snapshotID)
	a.mu.Unlock()
}

func (a *Applier) takeInflight(snapshotID int64) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.inflight[snapshotID]
	if ok {
		delete(a.inflight, snapshotID)
	}
	return id, ok
}

func (a *Applier) log() *slog.Logger {
	if a.Logger == nil {
		return slog.Default()
	}
	return a.Logger
}
