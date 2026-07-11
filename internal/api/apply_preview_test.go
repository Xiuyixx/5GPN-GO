package api

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// previewRule builds a minimal, valid DOMAIN rule for preview-diff fixtures.
func previewRule(id, pattern, action string) rules.Rule {
	return rules.Rule{ID: id, Kind: rules.KindDomain, Pattern: pattern, Action: action, Priority: 0, Enabled: true}
}

func previewBody(rs []rules.Rule) map[string]any {
	return map[string]any{"rules": rs}
}

// ------------------------------------------------------------------
// 1. Empty baseline (Resolver wired but never published) -> adding one
//    block rule reports AddedBlock=1 and everything else zero.
// ------------------------------------------------------------------

func TestApplyPreview_EmptyBaselineAddsBlock(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	rr := authPost(t, srv, "/api/v1/rules/apply/preview", token,
		previewBody([]rules.Rule{previewRule("add-block", "block.example.com", "block")}))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[applyPreviewResponse](t, rr)

	if resp.AddedBlock != 1 {
		t.Fatalf("AddedBlock = %d, want 1", resp.AddedBlock)
	}
	if resp.AddedDirect != 0 || resp.AddedProxy != 0 || resp.RemovedBlock != 0 ||
		resp.RemovedDirect != 0 || resp.RemovedProxy != 0 || resp.ChangedProxy != 0 {
		t.Fatalf("expected all other counts zero, got %+v", resp)
	}
	if resp.Total != 1 {
		t.Fatalf("Total = %d, want 1", resp.Total)
	}
}

// ------------------------------------------------------------------
// 2. Non-empty baseline -> adding 3 block, changing 1 direct->proxy,
//    removing 2 (one block, one direct) yields exact counts.
// ------------------------------------------------------------------

func TestApplyPreview_NonEmptyBaselineExactCounts(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	baseline := []rules.Rule{
		previewRule("keep", "keep.example.com", "block"),        // unchanged
		previewRule("changeme", "changeme.example.com", "direct"), // direct -> proxy
		previewRule("rm-block", "rm-block.example.com", "block"),  // removed
		previewRule("rm-direct", "rm-direct.example.com", "direct"), // removed
	}
	srv.rebuildAndPublish(context.Background(), baseline, "apply")

	candidate := []rules.Rule{
		previewRule("keep", "keep.example.com", "block"),
		previewRule("changeme", "changeme.example.com", "proxy"),
		previewRule("add1", "add1.example.com", "block"),
		previewRule("add2", "add2.example.com", "block"),
		previewRule("add3", "add3.example.com", "block"),
	}

	rr := authPost(t, srv, "/api/v1/rules/apply/preview", token, previewBody(candidate))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[applyPreviewResponse](t, rr)

	if resp.AddedBlock != 3 {
		t.Fatalf("AddedBlock = %d, want 3", resp.AddedBlock)
	}
	if resp.AddedDirect != 0 || resp.AddedProxy != 0 {
		t.Fatalf("unexpected added counts: %+v", resp)
	}
	if resp.ChangedProxy != 1 {
		t.Fatalf("ChangedProxy = %d, want 1", resp.ChangedProxy)
	}
	if resp.RemovedBlock != 1 {
		t.Fatalf("RemovedBlock = %d, want 1", resp.RemovedBlock)
	}
	if resp.RemovedDirect != 1 {
		t.Fatalf("RemovedDirect = %d, want 1", resp.RemovedDirect)
	}
	if resp.RemovedProxy != 0 {
		t.Fatalf("RemovedProxy = %d, want 0", resp.RemovedProxy)
	}
	if resp.Total != 6 {
		t.Fatalf("Total = %d, want 6 (3 added + 1 changed + 2 removed)", resp.Total)
	}
}

// ------------------------------------------------------------------
// 3. Previewing the exact ruleset that is already published yields all
//    zero counts.
// ------------------------------------------------------------------

func TestApplyPreview_SameRulesetTwiceAllZero(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	rs := []rules.Rule{
		previewRule("a", "a.example.com", "block"),
		previewRule("b", "b.example.com", "direct"),
		previewRule("c", "c.example.com", "proxy"),
	}
	srv.rebuildAndPublish(context.Background(), rs, "apply")

	rr := authPost(t, srv, "/api/v1/rules/apply/preview", token, previewBody(rs))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[applyPreviewResponse](t, rr)

	if resp.AddedBlock != 0 || resp.AddedDirect != 0 || resp.AddedProxy != 0 ||
		resp.RemovedBlock != 0 || resp.RemovedDirect != 0 || resp.RemovedProxy != 0 ||
		resp.ChangedProxy != 0 || resp.Total != 0 {
		t.Fatalf("expected an all-zero diff for an unchanged ruleset, got %+v", resp)
	}
}

// ------------------------------------------------------------------
// 4. A Server with no Resolver wired in at all still answers 200,
//    treating the baseline as empty.
// ------------------------------------------------------------------

func TestApplyPreview_NilResolverTreatsBaselineEmpty(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{}) // no Resolver

	if srv.Resolver != nil {
		t.Fatal("test setup: expected srv.Resolver to be nil")
	}

	rr := authPost(t, srv, "/api/v1/rules/apply/preview", token,
		previewBody([]rules.Rule{previewRule("add-direct", "direct.example.com", "direct")}))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[applyPreviewResponse](t, rr)
	if resp.AddedDirect != 1 {
		t.Fatalf("AddedDirect = %d, want 1", resp.AddedDirect)
	}
}

// ------------------------------------------------------------------
// 5. Malformed JSON body -> 400.
// ------------------------------------------------------------------

func TestApplyPreview_MalformedBody400(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/rules/apply/preview", strings.NewReader("{not valid json"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, r)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ------------------------------------------------------------------
// 6. Response Hash matches resolver.BuildTable(sameRules).Hash — i.e. what
//    a real apply of the same rules would publish.
// ------------------------------------------------------------------

func TestApplyPreview_HashMatchesBuildTable(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	rs := []rules.Rule{
		previewRule("h1", "hash1.example.com", "block"),
		previewRule("h2", "hash2.example.com", "proxy"),
	}

	rr := authPost(t, srv, "/api/v1/rules/apply/preview", token, previewBody(rs))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[applyPreviewResponse](t, rr)

	wantTbl, err := resolver.BuildTable(rs)
	if err != nil {
		t.Fatalf("resolver.BuildTable: %v", err)
	}
	wantHash := hex.EncodeToString(wantTbl.Hash[:])

	if resp.Hash != wantHash {
		t.Fatalf("Hash = %q, want %q (matches what handleApply would publish)", resp.Hash, wantHash)
	}
}
