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
	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
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
	Delete(ctx context.Context, exitID string) error
}

// ErrExitSwitchInFlight prevents deletion of either side of an exit switch
// until its health observation reaches a terminal state.
var ErrExitSwitchInFlight = errors.New("exit switch is still observing health")

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
	Resolver   *resolver.Store

	mu              sync.Mutex
	inflight        map[int64]*ruleApplyInfo
	switchInflight  map[int64]switchInfo // snapshot_id -> exit switch metadata
	protectedExits  map[string]int       // exit_id -> in-flight switch reference count
	terminalStatus  map[int64]applyTerminalStatus
	terminalOrder   []int64
	variationActive bool
}

const applyTerminalCacheCap = 128

type applyTerminalStatus struct {
	State  string
	Reason string
}

type ruleApplyInfo struct {
	mu              sync.Mutex
	statusID        int64
	ruleVersionID   int64
	priorVersionID  int64
	priorSnapshotID int64
	targetTable     *resolver.RuleTable
	priorTable      *resolver.RuleTable
	committed       bool
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
	if !a.beginVariation() {
		return ApplyResult{}, orchestrator.ErrApplyInFlight
	}
	releaseVariation := true
	defer func() {
		if releaseVariation {
			a.endVariation()
		}
	}()

	target, err := db.GetRuleVersionByID(a.DB, ruleVersionID)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: read target rule_version: %w", err)
	}
	if target.SnapshotID != snapshotID {
		return ApplyResult{}, fmt.Errorf("applier: rule_version %d belongs to snapshot %d, not %d", ruleVersionID, target.SnapshotID, snapshotID)
	}
	set, err := rules.ParseYAML([]byte(target.RulesYAML))
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: parse target rules: %w", err)
	}
	if err := set.Validate(); err != nil {
		return ApplyResult{}, fmt.Errorf("applier: validate target rules: %w", err)
	}
	targetTable, err := resolver.BuildTable(set.Rules)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: build target resolver table: %w", err)
	}

	cfg, err := Assemble(a.BaseConfig, a.Store)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: assemble: %w", err)
	}
	cfg.EffectiveRules = append([]rules.Rule(nil), set.Rules...)

	var priorVersionID, priorSnapshotID int64
	var priorTable *resolver.RuleTable
	if prior, priorErr := db.GetActiveRuleVersion(a.DB); priorErr == nil {
		priorVersionID = prior.ID
		priorSnapshotID = prior.SnapshotID
		if priorSet, parseErr := rules.ParseYAML([]byte(prior.RulesYAML)); parseErr == nil {
			priorTable, _ = resolver.BuildTable(priorSet.Rules)
		}
	} else if !errors.Is(priorErr, db.ErrNoRows) {
		return ApplyResult{}, fmt.Errorf("applier: read prior rule_version: %w", priorErr)
	}
	if priorTable == nil {
		priorTable, _ = resolver.BuildTable(nil)
	}
	if prevSnapshot != 0 && priorSnapshotID != 0 && prevSnapshot != priorSnapshotID {
		return ApplyResult{}, fmt.Errorf("applier: stale previous snapshot: got %d, active is %d", prevSnapshot, priorSnapshotID)
	}

	statusID, err := db.InsertApplyStatus(a.DB, snapshotID, "")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier: insert apply_status: %w", err)
	}
	a.clearTerminalStatus(snapshotID)
	a.registerInflight(snapshotID, &ruleApplyInfo{
		statusID:        statusID,
		ruleVersionID:   ruleVersionID,
		priorVersionID:  priorVersionID,
		priorSnapshotID: priorSnapshotID,
		targetTable:     targetTable,
		priorTable:      priorTable,
	})

	req := orchestrator.ApplyRequest{
		SnapshotID:    snapshotID,
		RuleVersionID: ruleVersionID,
		Config:        cfg,
		PrevSnapshot:  priorSnapshotID,
		Commit: func(context.Context) error {
			return a.commitRuleApply(snapshotID)
		},
	}
	res, appErr := a.Orch.Apply(ctx, req)

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
		a.persistApplyStatus(statusID, snapshotID, "rolled_back", "apply-in-flight")
		a.clearInflight(snapshotID)
		return result, appErr
	}
	if appErr != nil {
		state := "failed"
		if res.RolledBack {
			state = "rolled_back"
		}
		a.persistApplyStatus(statusID, snapshotID, state, appErr.Error())
		a.clearInflight(snapshotID)
		return result, appErr
	}

	switch res.Health {
	case "observing":
		// Terminal state will land via OnHealth from the bg goroutine.
		releaseVariation = false
	default:
		state := applyTerminalState(res)
		if state == "confirmed" {
			if err := a.commitRuleApply(snapshotID); err != nil {
				rollbackErr := a.Orch.Rollback(ctx, priorSnapshotID)
				state = "rolled_back"
				rolledBack := true
				finalErr := err
				if rollbackErr != nil {
					state = "failed"
					rolledBack = false
					finalErr = errors.Join(err, fmt.Errorf("restore data plane: %w", rollbackErr))
				}
				a.persistApplyStatus(statusID, snapshotID, state, finalErr.Error())
				a.clearInflight(snapshotID)
				return ApplyResult{SnapshotID: snapshotID, RuleVersionID: ruleVersionID, RolledBack: rolledBack, Health: "failed", Reason: finalErr.Error()}, finalErr
			}
		}
		a.persistApplyStatus(statusID, snapshotID, state, res.Reason)
		a.clearInflight(snapshotID)
		if state == "failed" {
			reason := res.Reason
			if reason == "" {
				reason = "orchestrator reported failed health without a confirmed rollback"
			}
			return result, errors.New(reason)
		}
	}

	return result, nil
}

func applyTerminalState(res orchestrator.ApplyResult) string {
	if res.RolledBack {
		return "rolled_back"
	}
	if res.Health == "failed" {
		return "failed"
	}
	return "confirmed"
}

// SwitchExit flips the active exit pointer in the DB, assembles a fresh
// config against the new active exit, and drives it through the same
// orchestrator/apply_status/OnHealth machinery ApplyRules uses. On any
// failure (assemble, snapshot insert, in-flight, sync apply error, or a
// bg-observer rollback) the DB pointer is restored to the prior active
// exit and an audit_log 'exits.switch.rolled_back' entry is emitted.
//
// The apply_status row carries a synthetic snapshot per switch so the
// panel's polling surface sees the same durable lifecycle it uses for rules
// applies (submitted, confirmed, rolled_back, or failed).
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
	if !a.beginVariation() {
		return ApplyResult{}, orchestrator.ErrApplyInFlight
	}
	releaseVariation := true
	defer func() {
		if releaseVariation {
			a.endVariation()
		}
	}()

	priorExitID, err := a.ExitStore.Active(ctx)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("applier.switch: read active: %w", err)
	}
	a.protectSwitchExits(priorExitID, exitID)
	keepProtection := false
	defer func() {
		if !keepProtection {
			a.unprotectSwitchExits(priorExitID, exitID)
		}
	}()

	if err := a.ExitStore.Switch(ctx, exitID); err != nil {
		return ApplyResult{}, fmt.Errorf("applier.switch: db switch: %w", err)
	}

	cfg, err := Assemble(a.BaseConfig, a.Store)
	if err != nil {
		primary := fmt.Errorf("applier.switch: assemble: %w", err)
		_, restoreErr := a.compensateSwitch(ctx, priorExitID, primary.Error(), "rolled_back")
		return ApplyResult{}, errors.Join(primary, restoreErr)
	}

	snapID, err := db.InsertSnapshot(a.DB, db.Snapshot{
		ConfigHash: fmt.Sprintf("exit-switch:%s:%d", exitID, time.Now().UnixNano()),
		Note:       fmt.Sprintf("exit-switch:%s", exitID),
	})
	if err != nil {
		primary := fmt.Errorf("applier.switch: snapshot: %w", err)
		_, restoreErr := a.compensateSwitch(ctx, priorExitID, primary.Error(), "rolled_back")
		return ApplyResult{}, errors.Join(primary, restoreErr)
	}

	statusID, err := db.InsertApplyStatus(a.DB, snapID, "exits.switch")
	if err != nil {
		primary := fmt.Errorf("applier.switch: insert apply_status: %w", err)
		_, restoreErr := a.compensateSwitch(ctx, priorExitID, primary.Error(), "rolled_back")
		return ApplyResult{}, errors.Join(primary, restoreErr)
	}
	a.clearTerminalStatus(snapID)

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
		a.clearSwitchInflight(snapID)
		state, restoreErr := a.compensateSwitch(ctx, priorExitID, "apply-in-flight", "rolled_back")
		a.persistApplyStatus(statusID, snapID, state, "apply-in-flight")
		return result, errors.Join(appErr, restoreErr)
	}
	if appErr != nil {
		a.clearSwitchInflight(snapID)
		state := "failed"
		if res.RolledBack {
			state = "rolled_back"
		}
		state, restoreErr := a.compensateSwitch(ctx, priorExitID, appErr.Error(), state)
		finalErr := errors.Join(appErr, restoreErr)
		a.persistApplyStatus(statusID, snapID, state, finalErr.Error())
		return result, finalErr
	}

	switch res.Health {
	case "observing":
		// Terminal state lands via OnHealth from the bg goroutine — the
		// switchInflight entry and protected exit ids stay until it fires.
		keepProtection = true
		releaseVariation = false
	case "":
		a.persistApplyStatus(statusID, snapID, "confirmed", "")
		a.clearSwitchInflight(snapID)
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "exits.switch.confirmed",
			Target: fmt.Sprintf("exit=%s prior=%s", exitID, priorExitID),
			Result: "ok",
		})
	default:
		state := applyTerminalState(res)
		if state != "confirmed" {
			if res.Reason == "" {
				res.Reason = "orchestrator did not confirm the exit switch"
			}
			var restoreErr error
			state, restoreErr = a.compensateSwitch(ctx, priorExitID, res.Reason, state)
			if restoreErr != nil {
				res.Reason = errors.Join(errors.New(res.Reason), restoreErr).Error()
			}
		}
		a.persistApplyStatus(statusID, snapID, state, res.Reason)
		a.clearSwitchInflight(snapID)
		switch state {
		case "confirmed":
			_ = db.AppendAudit(a.DB, db.AuditEntry{
				Action: "exits.switch.confirmed",
				Target: fmt.Sprintf("exit=%s prior=%s", exitID, priorExitID),
				Result: "ok",
			})
		case "failed":
			return result, errors.New(res.Reason)
		}
	}

	return result, nil
}

// compensateSwitch reverts the DB active exit pointer and records whether the
// requested terminal state was actually achieved.
func (a *Applier) compensateSwitch(ctx context.Context, priorExitID, reason, terminal string) (string, error) {
	if reason == "" {
		reason = "exit switch did not reach a confirmed terminal state"
	}
	var restoreErr error
	if priorExitID != "" && a.ExitStore != nil {
		if err := a.ExitStore.Switch(ctx, priorExitID); err != nil {
			restoreErr = fmt.Errorf("restore prior exit %s: %w", priorExitID, err)
			terminal = "failed"
			a.log().Error("applier.switch: compensation failed",
				"prior", priorExitID, "err", err)
		}
	}
	result := reason
	if restoreErr != nil {
		result = errors.Join(errors.New(reason), restoreErr).Error()
	}
	_ = db.AppendAudit(a.DB, db.AuditEntry{
		Action: "exits.switch." + terminal,
		Target: fmt.Sprintf("prior=%s", priorExitID),
		Result: result,
	})
	return terminal, restoreErr
}

// ImportRules parses and validates a YAML body, persists an inactive candidate
// rule version, then delegates activation and compensation to ApplyRules.
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
	rvID, err := db.InsertRuleVersion(a.DB, snapID, rulesYAML, false)
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
	if terminal, ok := a.TerminalStatus(row.SnapshotID); ok && row.State == "submitted" {
		row.State = terminal.State
		row.Reason = terminal.Reason
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
		defer a.endVariation()
		a.finalizeSwitchOnHealth(ctx, req, res, info)
		return
	}
	info, ok := a.getInflight(req.SnapshotID)
	if !ok {
		a.log().Warn("applier.OnHealth: no in-flight apply_status row",
			"snapshot_id", req.SnapshotID)
		return
	}
	defer a.endVariation()
	state := applyTerminalState(res)
	switch state {
	case "rolled_back":
		a.rollbackRuleApply(req.SnapshotID)
	case "confirmed":
		if err := a.commitRuleApply(req.SnapshotID); err != nil {
			rollbackErr := a.Orch.Rollback(ctx, info.priorSnapshotID)
			state = "rolled_back"
			res.RolledBack = true
			res.Reason = err.Error()
			if rollbackErr != nil {
				state = "failed"
				res.RolledBack = false
				res.Reason = errors.Join(err, fmt.Errorf("restore data plane: %w", rollbackErr)).Error()
			}
		}
	}
	a.persistApplyStatus(info.statusID, req.SnapshotID, state, res.Reason)
	a.clearInflight(req.SnapshotID)
	switch state {
	case "rolled_back":
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "rules.apply.rolled_back",
			Target: fmt.Sprintf("snapshot=%d", req.SnapshotID),
			Result: res.Reason,
		})
	case "failed":
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "rules.apply.failed",
			Target: fmt.Sprintf("snapshot=%d", req.SnapshotID),
			Result: res.Reason,
		})
	default:
		_ = db.AppendAudit(a.DB, db.AuditEntry{
			Action: "rules.apply.confirmed",
			Target: fmt.Sprintf("snapshot=%d", req.SnapshotID),
			Result: "ok",
		})
	}
}

func (a *Applier) beginVariation() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.variationActive {
		return false
	}
	a.variationActive = true
	return true
}

func (a *Applier) endVariation() {
	a.mu.Lock()
	a.variationActive = false
	a.mu.Unlock()
}

func (a *Applier) finalizeSwitchOnHealth(ctx context.Context, req orchestrator.ApplyRequest, res orchestrator.ApplyResult, info switchInfo) {
	defer a.unprotectSwitchExits(info.priorExitID, info.newExitID)
	state := applyTerminalState(res)
	if state != "confirmed" {
		if res.Reason == "" {
			res.Reason = "orchestrator did not confirm the exit switch"
		}
		var restoreErr error
		state, restoreErr = a.compensateSwitch(ctx, info.priorExitID, res.Reason, state)
		if restoreErr != nil {
			res.Reason = errors.Join(errors.New(res.Reason), restoreErr).Error()
		}
	}
	a.persistApplyStatus(info.statusID, req.SnapshotID, state, res.Reason)
	if state != "confirmed" {
		return
	}
	_ = db.AppendAudit(a.DB, db.AuditEntry{
		Action: "exits.switch.confirmed",
		Target: fmt.Sprintf("exit=%s prior=%s", info.newExitID, info.priorExitID),
		Result: "ok",
	})
}

// DeleteExit serializes the protection check and deletion with SwitchExit's
// reservation. This closes the check/delete race that would otherwise let an
// inactive rollback target disappear during asynchronous health observation.
func (a *Applier) DeleteExit(ctx context.Context, exitID string) error {
	if a.ExitStore == nil {
		return errors.New("applier: nil exit store")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.protectedExits[exitID] > 0 {
		return ErrExitSwitchInFlight
	}
	return a.ExitStore.Delete(ctx, exitID)
}

// TerminalStatus returns the bounded in-memory fallback used only when a
// terminal apply_status UPDATE could not be persisted. The database remains
// the durable source; this prevents live clients from polling forever during
// a transient write failure.
func (a *Applier) TerminalStatus(snapshotID int64) (ApplyStatusSnapshot, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	terminal, ok := a.terminalStatus[snapshotID]
	if !ok {
		return ApplyStatusSnapshot{}, false
	}
	return ApplyStatusSnapshot{
		SnapshotID: snapshotID,
		State:      terminal.State,
		Reason:     terminal.Reason,
	}, true
}

func (a *Applier) persistApplyStatus(statusID, snapshotID int64, state, reason string) {
	if err := db.UpdateApplyStatus(a.DB, statusID, state, reason); err != nil {
		a.recordTerminalStatus(snapshotID, state, reason)
		a.log().Error("applier: persist terminal apply_status failed; serving in-memory fallback",
			"snapshot_id", snapshotID, "state", state, "err", err)
		return
	}
	a.clearTerminalStatus(snapshotID)
}

func (a *Applier) recordTerminalStatus(snapshotID int64, state, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminalStatus == nil {
		a.terminalStatus = map[int64]applyTerminalStatus{}
	}
	if _, exists := a.terminalStatus[snapshotID]; !exists {
		a.terminalOrder = append(a.terminalOrder, snapshotID)
	}
	a.terminalStatus[snapshotID] = applyTerminalStatus{State: state, Reason: reason}
	for len(a.terminalOrder) > applyTerminalCacheCap {
		oldest := a.terminalOrder[0]
		a.terminalOrder = a.terminalOrder[1:]
		delete(a.terminalStatus, oldest)
	}
}

func (a *Applier) clearTerminalStatus(snapshotID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.terminalStatus[snapshotID]; !ok {
		return
	}
	delete(a.terminalStatus, snapshotID)
	for i, id := range a.terminalOrder {
		if id == snapshotID {
			a.terminalOrder = append(a.terminalOrder[:i], a.terminalOrder[i+1:]...)
			break
		}
	}
}

func (a *Applier) protectSwitchExits(exitIDs ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.protectedExits == nil {
		a.protectedExits = map[string]int{}
	}
	for _, exitID := range exitIDs {
		if exitID != "" {
			a.protectedExits[exitID]++
		}
	}
}

func (a *Applier) unprotectSwitchExits(exitIDs ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, exitID := range exitIDs {
		if exitID == "" {
			continue
		}
		if a.protectedExits[exitID] <= 1 {
			delete(a.protectedExits, exitID)
		} else {
			a.protectedExits[exitID]--
		}
	}
}

func (a *Applier) registerInflight(snapshotID int64, info *ruleApplyInfo) {
	a.mu.Lock()
	if a.inflight == nil {
		a.inflight = map[int64]*ruleApplyInfo{}
	}
	a.inflight[snapshotID] = info
	a.mu.Unlock()
}

func (a *Applier) clearInflight(snapshotID int64) {
	a.mu.Lock()
	delete(a.inflight, snapshotID)
	a.mu.Unlock()
}

func (a *Applier) getInflight(snapshotID int64) (*ruleApplyInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	info, ok := a.inflight[snapshotID]
	return info, ok
}

func (a *Applier) commitRuleApply(snapshotID int64) error {
	info, ok := a.getInflight(snapshotID)
	if !ok {
		return fmt.Errorf("applier: no in-flight rules apply for snapshot %d", snapshotID)
	}
	info.mu.Lock()
	defer info.mu.Unlock()
	if info.committed {
		return nil
	}
	if err := db.SetActiveRuleVersion(a.DB, info.ruleVersionID); err != nil {
		return fmt.Errorf("activate rule_version %d: %w", info.ruleVersionID, err)
	}
	if a.Resolver != nil {
		a.Resolver.Publish(info.targetTable)
	}
	info.committed = true
	return nil
}

func (a *Applier) rollbackRuleApply(snapshotID int64) {
	info, ok := a.getInflight(snapshotID)
	if !ok {
		return
	}
	info.mu.Lock()
	defer info.mu.Unlock()
	if !info.committed {
		return
	}
	var err error
	if info.priorVersionID == 0 {
		err = db.ClearActiveRuleVersion(a.DB)
	} else {
		err = db.SetActiveRuleVersion(a.DB, info.priorVersionID)
	}
	if err != nil {
		a.log().Error("applier: failed to restore prior rule version", "snapshot_id", snapshotID, "err", err)
		return
	}
	if a.Resolver != nil {
		a.Resolver.Publish(info.priorTable)
	}
	info.committed = false
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
