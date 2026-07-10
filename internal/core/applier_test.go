package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
)

func newApplierTestDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := db.Open(db.Config{Path: filepath.Join(t.TempDir(), "applier.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return handle
}

// recordingOrch is an orchestrator that lets tests choose the returned
// ApplyResult / error and inspect the ApplyRequest it saw.
type recordingOrch struct {
	mu      sync.Mutex
	seen    []orchestrator.ApplyRequest
	res     orchestrator.ApplyResult
	err     error
	rbErr   error
}

func (r *recordingOrch) Apply(_ context.Context, req orchestrator.ApplyRequest) (orchestrator.ApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, req)
	return r.res, r.err
}

func (r *recordingOrch) Rollback(_ context.Context, snapshotID int64) error {
	return r.rbErr
}

func (r *recordingOrch) requests() []orchestrator.ApplyRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]orchestrator.ApplyRequest, len(r.seen))
	copy(out, r.seen)
	return out
}

// insertSnapshotForTest writes a snapshots row so apply_status.snapshot_id
// FK is satisfied.
var snapCounter int64

func insertSnapshotForTest(t *testing.T, handle *sql.DB) int64 {
	t.Helper()
	n := atomic.AddInt64(&snapCounter, 1)
	id, err := db.InsertSnapshot(handle, db.Snapshot{
		ConfigHash: "hash-" + strconv.FormatInt(n, 10),
		Note:       "test",
	})
	if err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}
	return id
}

func newApplier(t *testing.T, orch orchestrator.Orchestrator) (*Applier, *sql.DB) {
	t.Helper()
	handle := newApplierTestDB(t)
	base := &config.Config{}
	return &Applier{
		DB:         handle,
		BaseConfig: base,
		Store:      &fakeStore{},
		Orch:       orch,
	}, handle
}

func TestApplierApplyRulesNoOpConfirmed(t *testing.T) {
	orch := &orchestrator.NoOp{}
	a, handle := newApplier(t, orch)
	snapID := insertSnapshotForTest(t, handle)

	res, err := a.ApplyRules(context.Background(), snapID, 42, 0)
	if err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if res.SnapshotID != snapID || res.RuleVersionID != 42 {
		t.Fatalf("result echoes wrong ids: %+v", res)
	}

	status, err := db.LatestApplyStatus(handle)
	if err != nil {
		t.Fatalf("LatestApplyStatus: %v", err)
	}
	if status == nil {
		t.Fatalf("expected an apply_status row")
	}
	if status.State != "confirmed" {
		t.Fatalf("NoOp path must land 'confirmed', got %q reason=%q", status.State, status.Reason)
	}
}

func TestApplierApplyRulesSyncErrorRollsBack(t *testing.T) {
	orch := &recordingOrch{err: errors.New("boom")}
	a, handle := newApplier(t, orch)
	snapID := insertSnapshotForTest(t, handle)

	_, err := a.ApplyRules(context.Background(), snapID, 1, 0)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	status, err := db.LatestApplyStatus(handle)
	if err != nil {
		t.Fatalf("LatestApplyStatus: %v", err)
	}
	if status.State != "rolled_back" {
		t.Fatalf("expected rolled_back, got %q", status.State)
	}
	if status.Reason != "boom" {
		t.Fatalf("expected reason 'boom', got %q", status.Reason)
	}
}

func TestApplierApplyRulesObservingLeavesSubmitted(t *testing.T) {
	orch := &recordingOrch{res: orchestrator.ApplyResult{Health: "observing"}}
	a, handle := newApplier(t, orch)
	snapID := insertSnapshotForTest(t, handle)

	_, err := a.ApplyRules(context.Background(), snapID, 1, 0)
	if err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	status, err := db.LatestApplyStatus(handle)
	if err != nil {
		t.Fatalf("LatestApplyStatus: %v", err)
	}
	if status.State != "submitted" {
		t.Fatalf("observing path must stay 'submitted', got %q", status.State)
	}

	a.OnHealth(context.Background(),
		orchestrator.ApplyRequest{SnapshotID: snapID},
		orchestrator.ApplyResult{Health: "ok"})

	status, _ = db.LatestApplyStatus(handle)
	if status.State != "confirmed" {
		t.Fatalf("post-OnHealth must be confirmed, got %q", status.State)
	}
}

func TestApplierApplyRulesObservingRolledBackViaObserver(t *testing.T) {
	orch := &recordingOrch{res: orchestrator.ApplyResult{Health: "observing"}}
	a, handle := newApplier(t, orch)
	snapID := insertSnapshotForTest(t, handle)

	if _, err := a.ApplyRules(context.Background(), snapID, 1, 0); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	a.OnHealth(context.Background(),
		orchestrator.ApplyRequest{SnapshotID: snapID},
		orchestrator.ApplyResult{RolledBack: true, Health: "failed", Reason: "probe failed"})

	status, _ := db.LatestApplyStatus(handle)
	if status.State != "rolled_back" || status.Reason != "probe failed" {
		t.Fatalf("expected rolled_back/probe failed, got %+v", status)
	}
}

func TestApplierApplyInFlight(t *testing.T) {
	orch := &recordingOrch{err: orchestrator.ErrApplyInFlight}
	a, handle := newApplier(t, orch)
	snapID := insertSnapshotForTest(t, handle)

	_, err := a.ApplyRules(context.Background(), snapID, 1, 0)
	if !errors.Is(err, orchestrator.ErrApplyInFlight) {
		t.Fatalf("expected ErrApplyInFlight, got %v", err)
	}
	status, _ := db.LatestApplyStatus(handle)
	if status.State != "rolled_back" || status.Reason != "apply-in-flight" {
		t.Fatalf("expected rolled_back/apply-in-flight, got %+v", status)
	}
}

func TestApplierStatusEmptyTable(t *testing.T) {
	a, _ := newApplier(t, &orchestrator.NoOp{})
	snap, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if snap.ID != 0 || snap.State != "" {
		t.Fatalf("expected zero snapshot, got %+v", snap)
	}
}

func TestApplierImportRulesRoundtrip(t *testing.T) {
	orch := &orchestrator.NoOp{}
	a, handle := newApplier(t, orch)

	yamlBody := "" +
		"rules:\n" +
		"  - id: r1\n" +
		"    kind: DOMAIN\n" +
		"    pattern: example.com\n" +
		"    action: PROXY\n" +
		"    priority: 1\n" +
		"    enabled: true\n"

	res, err := a.ImportRules(context.Background(), yamlBody, "alice", "127.0.0.1")
	if err != nil {
		t.Fatalf("ImportRules: %v", err)
	}
	if res.SnapshotID == 0 || res.RuleVersionID == 0 {
		t.Fatalf("expected non-zero snapshot + rule_version ids, got %+v", res)
	}

	status, err := db.LatestApplyStatus(handle)
	if err != nil {
		t.Fatalf("LatestApplyStatus: %v", err)
	}
	if status.State != "confirmed" {
		t.Fatalf("import path must land confirmed, got %q", status.State)
	}
}

func TestApplierOnHealthUnknownSnapshotIgnored(t *testing.T) {
	a, _ := newApplier(t, &orchestrator.NoOp{})
	// Should not panic and should not write anything.
	a.OnHealth(context.Background(),
		orchestrator.ApplyRequest{SnapshotID: 9999},
		orchestrator.ApplyResult{Health: "ok"})
}

func TestApplierNilStoreRejects(t *testing.T) {
	handle := newApplierTestDB(t)
	a := &Applier{DB: handle, BaseConfig: &config.Config{}, Orch: &orchestrator.NoOp{}}
	_, err := a.ApplyRules(context.Background(), 1, 1, 0)
	if err == nil {
		t.Fatalf("expected error on nil store")
	}
}

func TestApplierNilOrchRejects(t *testing.T) {
	handle := newApplierTestDB(t)
	a := &Applier{DB: handle, BaseConfig: &config.Config{}, Store: &fakeStore{}}
	_, err := a.ApplyRules(context.Background(), 1, 1, 0)
	if err == nil {
		t.Fatalf("expected error on nil orch")
	}
}

// TestApplierConcurrentRegisterInflight exercises the mutex path under -race.
func TestApplierConcurrentRegisterInflight(t *testing.T) {
	a, handle := newApplier(t, &recordingOrch{res: orchestrator.ApplyResult{Health: "observing"}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapID := insertSnapshotForTest(t, handle)
			_, _ = a.ApplyRules(context.Background(), snapID, 1, 0)
			a.OnHealth(context.Background(),
				orchestrator.ApplyRequest{SnapshotID: snapID},
				orchestrator.ApplyResult{Health: "ok"})
		}()
	}
	wg.Wait()
}
