package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// decodeBody is a goroutine-safe (no *testing.T) JSON decode helper for use
// inside worker goroutines that can't call t.Fatal.
func decodeBody(rr *httptest.ResponseRecorder) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &m)
	return m
}

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
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	orig := buildTableFn
	buildTableFn = func(rs []rules.Rule) (*resolver.RuleTable, error) {
		time.Sleep(200 * time.Millisecond)
		return resolver.BuildTable(rs)
	}
	defer func() { buildTableFn = orig }()

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
// 4. Different-hash concurrent applies are serialized (not collapsed):
//    both complete, 2 new applyStore entries, resolver generation +2.
// ------------------------------------------------------------------

func TestApply_DifferentHashSerialization(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	var calls int32
	orig := buildTableFn
	buildTableFn = func(rs []rules.Rule) (*resolver.RuleTable, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(150 * time.Millisecond)
		return resolver.BuildTable(rs)
	}

	var baseGen int64
	if tbl := srv.Resolver.Load(); tbl != nil {
		baseGen = tbl.Generation
	}
	baseEntries := len(srv.applyStore.List())

	req1 := jsonReq(t, "POST", "/api/v1/rules/apply", applyBody("diffA", "diff-a.example.com"))
	req1.Header.Set("Authorization", "Bearer "+token)
	req2 := jsonReq(t, "POST", "/api/v1/rules/apply", applyBody("diffB", "diff-b.example.com"))
	req2.Header.Set("Authorization", "Bearer "+token)

	statuses := make([]int, 2)
	bodies := make([]map[string]any, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req1)
		statuses[0] = rr.Code
		bodies[0] = decodeBody(rr)
	}()
	go func() {
		defer wg.Done()
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req2)
		statuses[1] = rr.Code
		bodies[1] = decodeBody(rr)
	}()
	wg.Wait()

	for i, code := range statuses {
		if code != http.StatusOK && code != http.StatusAccepted {
			t.Fatalf("request %d: unexpected status %d", i, code)
		}
	}

	// Fast-path (200) responses only return once their background
	// goroutine has already fully finished (its "done" channel closed
	// after buildTableFn returned), so no wait is needed for those. Slow
	// (202) responses may still have a background goroutine reading
	// buildTableFn — wait for those to go terminal before restoring it.
	for i, code := range statuses {
		if code == http.StatusAccepted {
			if id, _ := bodies[i]["apply_id"].(string); id != "" {
				waitTerminal(srv, id)
			}
		}
	}
	buildTableFn = orig

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want exactly 2 buildTableFn calls for different-hash concurrent applies, got %d", got)
	}
	if got := len(srv.applyStore.List()); got < baseEntries+2 {
		t.Fatalf("want at least %d applyStore entries, got %d", baseEntries+2, got)
	}
	tbl := srv.Resolver.Load()
	if tbl == nil || tbl.Generation != baseGen+2 {
		gotGen := int64(-1)
		if tbl != nil {
			gotGen = tbl.Generation
		}
		t.Fatalf("want resolver generation %d, got %d", baseGen+2, gotGen)
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
