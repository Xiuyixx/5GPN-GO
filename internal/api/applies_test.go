package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// waitTerminal blocks (bounded by a deadline) until applyStore entry id is
// no longer "pending". rebuildAndPublish can return to its caller (via the
// 100ms sync-window timeout) while its background build goroutine keeps
// running and keeps reading the buildTableFn package var — tests that
// override buildTableFn must wait for the entry to go terminal before
// restoring it, or the restore races with that still-running goroutine.
func waitTerminal(srv *Server, id string) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		live, ok := srv.applyStore.Get(id)
		if ok && live.Status != "pending" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// applyBody builds a minimal single-rule apply request whose hash is driven
// entirely by pattern/id, so distinct patterns always produce distinct
// rules.HashRules output (needed to exercise the same-hash vs
// different-hash singleflight paths deterministically).
func applyBody(idSuffix, pattern string) map[string]any {
	return map[string]any{
		"rules": []map[string]any{
			{"id": "r-" + idSuffix, "kind": "DOMAIN-SUFFIX", "pattern": pattern, "action": "direct", "priority": 10, "enabled": true},
		},
		"note": "applies_test " + idSuffix,
	}
}

type observingOrch struct {
	mu      sync.Mutex
	req     orchestrator.ApplyRequest
	applied chan struct{}
	once    sync.Once
}

func newObservingOrch() *observingOrch {
	return &observingOrch{applied: make(chan struct{})}
}

func (o *observingOrch) Apply(_ context.Context, req orchestrator.ApplyRequest) (orchestrator.ApplyResult, error) {
	o.mu.Lock()
	o.req = req
	o.mu.Unlock()
	o.once.Do(func() { close(o.applied) })
	return orchestrator.ApplyResult{Health: "observing"}, nil
}

func (o *observingOrch) Rollback(context.Context, int64) error { return nil }

func (o *observingOrch) confirm(srv *Server) {
	o.mu.Lock()
	req := o.req
	o.mu.Unlock()
	srv.Applier.OnHealth(context.Background(), req, orchestrator.ApplyResult{Health: "ok"})
}

// ------------------------------------------------------------------
// 1. Fast path: build completes within the 100ms sync window -> 200.
// ------------------------------------------------------------------

func TestApply_FastPathReturns200(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	rr := authPost(t, srv, "/api/v1/rules/apply", token, applyBody("fast", "fast.example.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if srv.Resolver.Load() == nil {
		t.Fatal("resolver table was never published on the fast path")
	}
}

// ------------------------------------------------------------------
// 2. Slow path: build takes longer than 100ms -> 202 + Location, then
//    polling /api/v1/applies/{id} transitions pending -> succeeded.
// ------------------------------------------------------------------

func TestApply_SlowPathReturns202AndPolls(t *testing.T) {
	orch := newObservingOrch()
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}, Orchestrator: orch})

	rr := authPost(t, srv, "/api/v1/rules/apply", token, applyBody("slow", "slow.example.com"))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header on 202 response")
	}
	body := decode[map[string]any](t, rr)
	applyID, _ := body["apply_id"].(string)
	if applyID == "" {
		t.Fatalf("missing apply_id in 202 body: %v", body)
	}
	if body["status"] != "pending" {
		t.Fatalf("expected status=pending in 202 body, got %v", body)
	}
	if wantLoc := "/api/v1/applies/" + applyID; loc != wantLoc {
		t.Fatalf("Location=%q want %q", loc, wantLoc)
	}
	orch.confirm(srv)

	deadline := time.Now().Add(2 * time.Second)
	var final map[string]any
	for time.Now().Before(deadline) {
		pr := authGet(t, srv, "/api/v1/applies/"+applyID, token)
		if pr.Code != http.StatusOK {
			t.Fatalf("poll: want 200, got %d: %s", pr.Code, pr.Body.String())
		}
		final = decode[map[string]any](t, pr)
		if final["status"] == "succeeded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final["status"] != "succeeded" {
		t.Fatalf("apply never reached succeeded status: %v", final)
	}
	if srv.Resolver.Load() == nil {
		t.Fatal("resolver table never published after slow apply completed")
	}
}

// ------------------------------------------------------------------
// 3. Same-hash concurrent applies collapse into exactly one build via
//    singleflight.
// ------------------------------------------------------------------

func TestApply_SameHashCollapsesToOneBuild(t *testing.T) {
	srv, _ := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	var calls int32
	orig := buildTableFn
	buildTableFn = func(rs []rules.Rule) (*resolver.RuleTable, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(150 * time.Millisecond)
		return resolver.BuildTable(rs)
	}

	// Same-hash collapse is a property of rebuildAndPublish / applyStore's
	// singleflight group, independent of the full HTTP apply pipeline (two
	// concurrent HTTP applies with byte-identical bodies would instead
	// collide on the pre-existing snapshots.config_hash UNIQUE constraint,
	// which is unrelated to this Phase 5 change). Drive rebuildAndPublish
	// directly with an identical rule set from two goroutines.
	rs := []rules.Rule{{
		ID: "r-collapse", Kind: rules.KindDomainSuffix,
		Pattern: "collapse.example.com", Action: "direct",
		Priority: 10, Enabled: true,
	}}

	entries := make([]*applyEntry, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range entries {
		i := i
		go func() {
			defer wg.Done()
			entries[i] = srv.rebuildAndPublish(context.Background(), rs, "apply")
		}()
	}
	wg.Wait()

	// Both entries may still be "pending" (the artificial 150ms build
	// latency exceeds the 100ms sync window) — wait for both to reach a
	// terminal status before restoring buildTableFn, so the restore below
	// can never race with a background goroutine still reading it.
	for _, e := range entries {
		waitTerminal(srv, e.ID)
	}
	buildTableFn = orig

	for _, e := range entries {
		live, ok := srv.applyStore.Get(e.ID)
		if !ok || live.Status != "succeeded" {
			t.Fatalf("entry %s did not reach succeeded status: %+v", e.ID, live)
		}
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("want exactly 1 buildTableFn call for same-hash concurrent applies, got %d", got)
	}
}

// ------------------------------------------------------------------
// 4. The health-observation period is a single-writer transaction. A
//    second variation is rejected until the first one reaches terminal
//    health, so DB and resolver state cannot be committed out of order.
// ------------------------------------------------------------------

func TestApply_RejectsSecondVariationWhileObserving(t *testing.T) {
	orch := newObservingOrch()
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}, Orchestrator: orch})

	first := authPost(t, srv, "/api/v1/rules/apply", token, applyBody("diffA", "diff-a.example.com"))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first apply: want 202, got %d: %s", first.Code, first.Body.String())
	}

	second := authPost(t, srv, "/api/v1/rules/apply", token, applyBody("diffB", "diff-b.example.com"))
	if second.Code != http.StatusConflict {
		t.Fatalf("second apply: want 409, got %d: %s", second.Code, second.Body.String())
	}

	orch.confirm(srv)
	tbl := srv.Resolver.Load()
	if tbl == nil {
		t.Fatal("resolver table not published after confirmation")
	}
	entries := tbl.Entries()
	if _, ok := entries["suffix:diff-a.example.com"]; !ok {
		t.Fatalf("confirmed first variation is absent from resolver: %v", entries)
	}
	if _, ok := entries["suffix:diff-b.example.com"]; ok {
		t.Fatalf("rejected second variation leaked into resolver: %v", entries)
	}
}

// ------------------------------------------------------------------
// 5. 404 apply_expired on an unknown apply id.
// ------------------------------------------------------------------

func TestApplyGet_404Expired(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	rr := authGet(t, srv, "/api/v1/applies/does-not-exist", token)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
	body := decode[map[string]any](t, rr)
	if body["error"] != "apply_expired" {
		t.Fatalf("want error=apply_expired, got %v", body)
	}
}

// ------------------------------------------------------------------
// 6. Ring TTL eviction: an entry older than 24h is excluded from
//    Get/List even though the count-cap hasn't been hit.
// ------------------------------------------------------------------

func TestApplyStore_RingTTLEviction(t *testing.T) {
	as := newApplyStore()
	now := time.Now()
	as.now = func() time.Time { return now }

	e := as.create("deadbeef", "apply", 1)

	if _, ok := as.Get(e.ID); !ok {
		t.Fatal("expected entry to be readable immediately after creation")
	}

	now = now.Add(applyRingTTL + time.Minute)

	if _, ok := as.Get(e.ID); ok {
		t.Fatal("expected entry to be TTL-expired after 24h+")
	}
	for _, le := range as.List() {
		if le.ID == e.ID {
			t.Fatalf("TTL-expired entry %s should not appear in List()", e.ID)
		}
	}
}

// ------------------------------------------------------------------
// 7. Rollback republishes the restored RuleTable and shows up as a
//    rollback-kind entry in /api/v1/applies alongside forward applies.
// ------------------------------------------------------------------

func TestRollback_RepublishesAndAppearsInApplies(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	rr := authPost(t, srv, "/api/v1/rules/apply", token, applyBody("first", "first.example.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("first apply: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	first := decode[map[string]any](t, rr)
	firstSnapID, _ := first["snapshot_id"].(float64)
	if firstSnapID == 0 {
		t.Fatalf("missing snapshot_id in first apply response: %v", first)
	}

	rr = authPost(t, srv, "/api/v1/rules/apply", token, applyBody("second", "second.example.com"))
	if rr.Code != http.StatusOK {
		t.Fatalf("second apply: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	rr = authPost(t, srv, fmt.Sprintf("/api/v1/snapshots/%d/rollback", int64(firstSnapID)), token, nil)
	if rr.Code != http.StatusOK && rr.Code != http.StatusAccepted {
		t.Fatalf("rollback: want 200/202, got %d: %s", rr.Code, rr.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, e := range srv.applyStore.List() {
			if e.Kind == "rollback" {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("expected a rollback-kind entry in applyStore after rollback")
	}
}

func TestExitSwitchObservingReturnsCorrelatedApplyID(t *testing.T) {
	orch := newObservingOrch()
	srv, token := bootstrapAndLogin(t, Config{Orchestrator: orch})
	rr := authPost(t, srv, "/api/v1/exits/add", token, map[string]any{
		"id": "wg1", "uri": "trojan://pw@example.com:443?sni=fake.example.com",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("add exit: %d %s", rr.Code, rr.Body.String())
	}
	rr = authPost(t, srv, "/api/v1/exits/switch", token, map[string]any{"id": "wg1"})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("observing switch: got %d %s, want 202", rr.Code, rr.Body.String())
	}
	response := decode[map[string]any](t, rr)
	applyID, _ := response["apply_id"].(string)
	if applyID == "" || response["status"] != "pending" {
		t.Fatalf("switch response lacks correlated apply id: %v", response)
	}
	var auditResult string
	if err := srv.DB.QueryRow(`SELECT result FROM audit_log WHERE action = 'exits.switch' ORDER BY id DESC LIMIT 1`).Scan(&auditResult); err != nil {
		t.Fatalf("read switch audit: %v", err)
	}
	if auditResult != "observing" {
		t.Fatalf("initial switch audit result = %q, want observing", auditResult)
	}
	rr = authPost(t, srv, "/api/v1/exits/delete", token, map[string]any{"id": "direct"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("delete rollback target while observing: got %d %s, want 409", rr.Code, rr.Body.String())
	}
	orch.confirm(srv)
	entry, ok := srv.applyStore.Get(applyID)
	if !ok {
		t.Fatalf("apply %q not tracked", applyID)
	}
	entry = srv.refreshApplyEntry(entry)
	if entry.Status != "succeeded" || entry.Kind != "exit_switch" {
		t.Fatalf("terminal exit switch entry: %+v", entry)
	}
	rr = authPost(t, srv, "/api/v1/exits/delete", token, map[string]any{"id": "direct"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete prior exit after terminal state: got %d %s, want 204", rr.Code, rr.Body.String())
	}
}
