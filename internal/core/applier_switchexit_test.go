package core

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
)

// fakeExitStore is an in-memory ExitStore for Applier.SwitchExit tests.
// It also exposes call counters and an optional switchErr override so
// tests can force a DB failure path.
type fakeExitStore struct {
	mu         sync.Mutex
	active     string
	known      map[string]bool
	switchErr  error
	activeErr  error
	switchCall []string // ordered log of Switch(exitID) calls
}

func newFakeExitStore(activeID string, extras ...string) *fakeExitStore {
	known := map[string]bool{activeID: true}
	for _, e := range extras {
		known[e] = true
	}
	return &fakeExitStore{active: activeID, known: known}
}

func (f *fakeExitStore) Active(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, f.activeErr
}

func (f *fakeExitStore) Switch(_ context.Context, exitID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.switchErr != nil {
		return f.switchErr
	}
	if !f.known[exitID] {
		return errors.New("unknown exit id")
	}
	f.active = exitID
	f.switchCall = append(f.switchCall, exitID)
	return nil
}

func (f *fakeExitStore) currentActive() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

func (f *fakeExitStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.switchCall)
}

func newSwitchApplier(t *testing.T, orch orchestrator.Orchestrator, exitStore *fakeExitStore) (*Applier, *sql.DB) {
	t.Helper()
	handle := newApplierTestDB(t)
	return &Applier{
		DB:         handle,
		BaseConfig: &config.Config{},
		Store:      &fakeStore{},
		ExitStore:  exitStore,
		Orch:       orch,
	}, handle
}

func lastAuditAction(t *testing.T, handle *sql.DB) (action, target, result string) {
	t.Helper()
	row := handle.QueryRow(`SELECT action, COALESCE(target,''), COALESCE(result,'') FROM audit_log ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&action, &target, &result); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ""
		}
		t.Fatalf("audit_log query: %v", err)
	}
	return
}

func countAuditRowsWithAction(t *testing.T, handle *sql.DB, action string) int {
	t.Helper()
	var n int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("audit_log count: %v", err)
	}
	return n
}

// TestSwitchExitHappyPath: direct → wg1, apply_status confirmed,
// ExitStore.Active reports wg1.
func TestSwitchExitHappyPath(t *testing.T) {
	orch := &orchestrator.NoOp{}
	es := newFakeExitStore("direct", "wg1")
	a, handle := newSwitchApplier(t, orch, es)

	res, err := a.SwitchExit(context.Background(), "wg1")
	if err != nil {
		t.Fatalf("SwitchExit: %v", err)
	}
	if res.SnapshotID == 0 {
		t.Fatalf("expected non-zero snapshot id")
	}
	if got := es.currentActive(); got != "wg1" {
		t.Fatalf("active after switch = %q, want wg1", got)
	}
	status, err := db.LatestApplyStatus(handle)
	if err != nil {
		t.Fatalf("LatestApplyStatus: %v", err)
	}
	if status == nil {
		t.Fatalf("expected apply_status row")
	}
	if status.State != "confirmed" {
		t.Fatalf("state = %q, want confirmed (reason=%q)", status.State, status.Reason)
	}
	if got := countAuditRowsWithAction(t, handle, "exits.switch.confirmed"); got != 1 {
		t.Fatalf("expected 1 exits.switch.confirmed audit row, got %d", got)
	}
	if got := countAuditRowsWithAction(t, handle, "exits.switch.rolled_back"); got != 0 {
		t.Fatalf("unexpected rolled_back audit rows: %d", got)
	}
}

// TestSwitchExitRollsBackOnSyncFailure: mock orch returns error → active
// reverts to prior, apply_status rolled_back, audit_log has rolled_back
// entry.
func TestSwitchExitRollsBackOnSyncFailure(t *testing.T) {
	orch := &recordingOrch{err: errors.New("boom")}
	es := newFakeExitStore("direct", "wg1")
	a, handle := newSwitchApplier(t, orch, es)

	_, err := a.SwitchExit(context.Background(), "wg1")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := es.currentActive(); got != "direct" {
		t.Fatalf("active after failed switch = %q, want direct", got)
	}
	status, err := db.LatestApplyStatus(handle)
	if err != nil {
		t.Fatalf("LatestApplyStatus: %v", err)
	}
	if status == nil || status.State != "rolled_back" {
		t.Fatalf("expected rolled_back apply_status, got %+v", status)
	}
	if status.Reason != "boom" {
		t.Fatalf("reason = %q, want 'boom'", status.Reason)
	}
	if got := countAuditRowsWithAction(t, handle, "exits.switch.rolled_back"); got != 1 {
		t.Fatalf("expected 1 rolled_back audit row, got %d", got)
	}
	action, _, result := lastAuditAction(t, handle)
	if action != "exits.switch.rolled_back" || result != "boom" {
		t.Fatalf("last audit = %q/%q, want exits.switch.rolled_back/boom", action, result)
	}
}

// TestSwitchExitAppliesInFlight409: SwitchExit with orch returning
// ErrApplyInFlight marks apply_status 'rolled_back' with reason
// 'apply-in-flight' (F25 pattern) and reverts DB pointer.
func TestSwitchExitAppliesInFlight409(t *testing.T) {
	orch := &recordingOrch{err: orchestrator.ErrApplyInFlight}
	es := newFakeExitStore("direct", "wg1")
	a, handle := newSwitchApplier(t, orch, es)

	_, err := a.SwitchExit(context.Background(), "wg1")
	if !errors.Is(err, orchestrator.ErrApplyInFlight) {
		t.Fatalf("expected ErrApplyInFlight, got %v", err)
	}
	if got := es.currentActive(); got != "direct" {
		t.Fatalf("active after in-flight rejection = %q, want direct", got)
	}
	status, _ := db.LatestApplyStatus(handle)
	if status == nil || status.State != "rolled_back" || status.Reason != "apply-in-flight" {
		t.Fatalf("expected rolled_back/apply-in-flight apply_status, got %+v", status)
	}
	if got := countAuditRowsWithAction(t, handle, "exits.switch.rolled_back"); got != 1 {
		t.Fatalf("expected 1 rolled_back audit row, got %d", got)
	}
}

// TestSwitchExitUnknownID: unknown exit id → ExitStore.Switch returns
// error, no apply_status row is written, active is untouched.
func TestSwitchExitUnknownID(t *testing.T) {
	orch := &orchestrator.NoOp{}
	es := newFakeExitStore("direct", "wg1")
	a, handle := newSwitchApplier(t, orch, es)

	_, err := a.SwitchExit(context.Background(), "bogus")
	if err == nil {
		t.Fatalf("expected error for unknown exit id")
	}
	if got := es.currentActive(); got != "direct" {
		t.Fatalf("active mutated on unknown id: %q", got)
	}
	if status, _ := db.LatestApplyStatus(handle); status != nil {
		t.Fatalf("apply_status row should not exist, got %+v", status)
	}
	if got := countAuditRowsWithAction(t, handle, "exits.switch.rolled_back"); got != 0 {
		t.Fatalf("no audit row expected on failed DB switch, got %d", got)
	}
}

// TestSwitchExitObservingRolledBackViaObserver: orch returns
// Health=observing then OnHealth reports RolledBack — SwitchExit
// compensates via the observer path, active reverts, apply_status
// flips to rolled_back.
func TestSwitchExitObservingRolledBackViaObserver(t *testing.T) {
	orch := &recordingOrch{res: orchestrator.ApplyResult{Health: "observing"}}
	es := newFakeExitStore("direct", "wg1")
	a, handle := newSwitchApplier(t, orch, es)

	res, err := a.SwitchExit(context.Background(), "wg1")
	if err != nil {
		t.Fatalf("SwitchExit: %v", err)
	}
	// While observing, DB active is already wg1.
	if got := es.currentActive(); got != "wg1" {
		t.Fatalf("active during observing = %q, want wg1", got)
	}
	// Observer reports rollback.
	a.OnHealth(context.Background(),
		orchestrator.ApplyRequest{SnapshotID: res.SnapshotID},
		orchestrator.ApplyResult{RolledBack: true, Health: "failed", Reason: "probe failed"})

	if got := es.currentActive(); got != "direct" {
		t.Fatalf("active after observer rollback = %q, want direct", got)
	}
	status, _ := db.LatestApplyStatus(handle)
	if status == nil || status.State != "rolled_back" || status.Reason != "probe failed" {
		t.Fatalf("expected rolled_back/probe failed, got %+v", status)
	}
	if got := countAuditRowsWithAction(t, handle, "exits.switch.rolled_back"); got != 1 {
		t.Fatalf("expected 1 rolled_back audit row, got %d", got)
	}
}

// TestSwitchExitNilExitStoreRejects covers the constructor-shape guard.
func TestSwitchExitNilExitStoreRejects(t *testing.T) {
	handle := newApplierTestDB(t)
	a := &Applier{
		DB:         handle,
		BaseConfig: &config.Config{},
		Store:      &fakeStore{},
		Orch:       &orchestrator.NoOp{},
	}
	_, err := a.SwitchExit(context.Background(), "wg1")
	if err == nil {
		t.Fatalf("expected error on nil ExitStore")
	}
}
