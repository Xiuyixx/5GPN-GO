package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// ExitStore is the narrow interface Applier needs to flip the DB-backed
// active exit pointer during SwitchExit. It is deliberately typed against
// string IDs (not the internal/exit.Exit struct) so this package does not
// have to import internal/exit — the concrete adapter lives in main.go.
//
// Active returns the current active exit_id, or "" if none is active.
// Switch demotes any currently-active exit and promotes exitID inside a
// single transaction (enforced by the DB partial-unique index).
type ExitStore interface {
	Active(ctx context.Context) (string, error)
	Switch(ctx context.Context, exitID string) error
}

// Applier is the single "produce a variation" service. Every entry point
// that changes the data plane — API rules apply, backup import, rollback,
// boot restore — flows through here. It owns the mapping between an
// in-flight orchestrator.Apply call and the apply_status DB row that the
// panel UI polls.
type Applier struct {
	DB         *sql.DB
	BaseConfig *config.Config
	Store      Store
	ExitStore  ExitStore
	Orch       orchestrator.Orchestrator
	Logger     *slog.Logger

	mu             sync.Mutex
	inflight       map[int64]int64      // snapshot_id -> apply_status.id
	switchInflight map[int64]switchInfo // snapshot_id -> exit switch metadata
}

// switchInfo carries the extra state OnHealth needs when a snapshot came
// from SwitchExit rather than ApplyRules: the target/prior exit ids so a
// rolled-back probe can compensate the DB pointer and emit the right
// audit action.
type switchInfo struct {
	statusID    int64
	newExitID   string
	priorExitID string
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
		// Rejected because another Apply is still landing. Mark this row
		// terminal so LatestApplyStatus() surfaces the real in-flight row
		// instead of masking it with a stuck 'submitted' entry.
		_ = db.UpdateApplyStatus(a.DB, statusID, "rolled_back", "apply-in-flight")
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

// SwitchExit flips the active exit pointer in the DB, assembles a fresh
// config against the new active exit, and drives it through the same
// orchestrator/apply_status/OnHealth machinery ApplyRules uses. On any
// failure (assemble, snapshot insert, in-flight, sync apply error, or a
// bg-observer rollback) the DB pointer is restored to the prior active
// exit and an audit_log 'exits.switch.rolled_back' entry is emitted.
//
// The apply_status row carries a synthetic snapshot per switch so the
// panel's polling surface sees the same three-state lifecycle it already
// understands for rules applies.
func (a *Applier) SwitchExit(ctx context.Context, exitID string) (ApplyResult, error) {
	if a.Store == nil {
		return ApplyResult{}, errors.New("applier: nil store")
	}
	if a.ExitStore == nil {
		return ApplyResult{}, errors.New("applier: nil exit store")
	}
	if a.Orch == nil {
		return ApplyResult{}, errors.New("applier: nil orchestrator")
	}
	if exitID == "" {
		return ApplyResult{}, errors.New("applier.switch: empty exit id")
	}

	priorExitID, err := a.ExitStore.Active(ctx)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier.switch: read active: %w", err)
	}

	if err := a.ExitStore.Switch(ctx, exitID); err != nil {
		return ApplyResult{}, fmt.Errorf("applier.switch: db switch: %w", err)
	}

	cfg, err := Assemble(a.BaseConfig, a.Store)
	if err != nil {
		a.compensateSwitch(ctx, priorExitID, "assemble: "+err.Error())
		return ApplyResult{}, fmt.Errorf("applier.switch: assemble: %w", err)
	}

	snapID, err := db.InsertSnapshot(a.DB, db.Snapshot{
		ConfigHash: fmt.Sprintf("exit-switch:%s:%d", exitID, time.Now().UnixNano()),
		Note:       fmt.Sprintf("exit-switch:%s", exitID),
	})
	if err != nil {
		a.compensateSwitch(ctx, priorExitID, "snapshot: "+err.Error())
		return ApplyResult{}, fmt.Errorf("applier.switch: snapshot: %w", err)
	}

	statusID, err := db.InsertApplyStatus(a.DB, snapID, "exits.switch")
	if err != nil {
		a.compensateSwitch(ctx, priorExitID, "apply_status: "+err.Error())
		return ApplyResult{}, fmt.Errorf("applier.switch: insert apply_status: %w", err)
	}

	a.registerSwitchInflight(snapID, switchInfo{
		statusID:    statusID,
		newExitID:   exitID,
		priorExitID: priorExitID,
	})

	res, appErr := a.Orch.Apply(ctx, orchestrator.ApplyRequest{
		SnapshotID: snapID,
		Config:     cfg,
	})

	result := ApplyResult{
		SnapshotID: snapID,
		RolledBack: res.RolledBack,
		Health:     res.Health,
		Reason:     res.Reason,
	}

	if errors.Is(appErr, orchestrator.ErrApplyInFlight) {
		_ = db.UpdateApplyStatus(a.DB, statusID, "rolled_back", "apply-in-flight")
		a.clearSwitchInflight(snapID)
		a.compensateSwitch(ctx, priorExitID, "apply-in-flight")
		return result, appErr
	}
	if appErr != nil {
		_ = db.UpdateApplyStatus(a.DB, statusID, "rolled_back", appErr.Error())
		a.clearSwitchInflight(snapID)
		a.compensateSwitch(ctx, priorExitID, appErr.Error())
		return result, appErr
	}

	switch res.Health {
	case "observing":
		// Terminal state lands via OnHealth from the bg goroutine — the
		// switchInflight entry stays until it fires.
	case "":
		_ = db.UpdateApplyStatus(a.DB, statusID, "confirmed", "")
		a.clearSwitchInflight(snapID)
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "exits.switch.confirmed",
			Target: fmt.Sprintf("exit=%s prior=%s", exitID, priorExitID),
			Result: "ok",
		})
	default:
		state := "confirmed"
		if res.RolledBack {
			state = "rolled_back"
		}
		_ = db.UpdateApplyStatus(a.DB, statusID, state, res.Reason)
		a.clearSwitchInflight(snapID)
		if res.RolledBack {
			a.compensateSwitch(ctx, priorExitID, res.Reason)
		} else {
			_ = db.AppendAudit(a.DB, db.AuditEntry{
				Action: "exits.switch.confirmed",
				Target: fmt.Sprintf("exit=%s prior=%s", exitID, priorExitID),
				Result: "ok",
			})
		}
	}

	return result, nil
}

// compensateSwitch reverts the DB active exit pointer to prior and logs a
// rollback audit entry. Best-effort: errors are logged but do not surface
// to the caller — the primary error is already being propagated.
func (a *Applier) compensateSwitch(ctx context.Context, priorExitID, reason string) {
	if priorExitID != "" && a.ExitStore != nil {
		if err := a.ExitStore.Switch(ctx, priorExitID); err != nil {
			a.log().Error("applier.switch: compensation failed",
				"prior", priorExitID, "err", err)
		}
	}
	_ = db.AppendAudit(a.DB, db.AuditEntry{
		Action: "exits.switch.rolled_back",
		Target: fmt.Sprintf("prior=%s", priorExitID),
		Result: reason,
	})
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
// is a *Systemd. It flips the apply_status row that ApplyRules or
// SwitchExit created.
func (a *Applier) OnHealth(ctx context.Context, req orchestrator.ApplyRequest, res orchestrator.ApplyResult) {
	if info, ok := a.takeSwitchInflight(req.SnapshotID); ok {
		a.finalizeSwitchOnHealth(ctx, req, res, info)
		return
	}
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

func (a *Applier) finalizeSwitchOnHealth(ctx context.Context, req orchestrator.ApplyRequest, res orchestrator.ApplyResult, info switchInfo) {
	state := "confirmed"
	if res.RolledBack {
		state = "rolled_back"
	}
	if err := db.UpdateApplyStatus(a.DB, info.statusID, state, res.Reason); err != nil {
		a.log().Error("applier.OnHealth: switch update apply_status failed",
			"snapshot_id", req.SnapshotID, "err", err)
	}
	if state == "rolled_back" {
		a.compensateSwitch(ctx, info.priorExitID, res.Reason)
		return
	}
	_ = db.AppendAudit(a.DB, db.AuditEntry{
		Action: "exits.switch.confirmed",
		Target: fmt.Sprintf("exit=%s prior=%s", info.newExitID, info.priorExitID),
		Result: "ok",
	})
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

func (a *Applier) registerSwitchInflight(snapshotID int64, info switchInfo) {
	a.mu.Lock()
	if a.switchInflight == nil {
		a.switchInflight = map[int64]switchInfo{}
	}
	a.switchInflight[snapshotID] = info
	a.mu.Unlock()
}

func (a *Applier) clearSwitchInflight(snapshotID int64) {
	a.mu.Lock()
	delete(a.switchInflight, snapshotID)
	a.mu.Unlock()
}

func (a *Applier) takeSwitchInflight(snapshotID int64) (switchInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	info, ok := a.switchInflight[snapshotID]
	if ok {
		delete(a.switchInflight, snapshotID)
	}
	return info, ok
}

func (a *Applier) log() *slog.Logger {
	if a.Logger == nil {
		return slog.Default()
	}
	return a.Logger
}
