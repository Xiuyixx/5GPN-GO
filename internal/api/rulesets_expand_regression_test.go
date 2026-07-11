package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
	"github.com/Xiuyixx/5GPN-Go/internal/rulesets"
)

// TestRulesetsExpand_NoDuplicateIDOnReapply pins the fix for the bug where
// the second apply after registering a ruleset was rejected with
// "duplicate id".
//
// Trigger:
//  1. Register a ruleset -> Rulesets.Expand() materializes deterministic IDs
//     (fmt.Sprintf("%s-%d", name, i+1)) and handleApply writes them into
//     rule_versions.rules_yaml with GroupID set.
//  2. Panel refreshes -> GET /api/v1/rules serves back every rule including
//     the expanded GroupID-tagged entries.
//  3. Panel edits + Apply -> req.Rules already contains the expanded rules;
//     the handler re-Expand()s them and the second batch collides on ID.
//
// The fix: handleDryRun / handleApply / handleApplyPreview each strip
// incoming rules with GroupID != "" before appending Expand()'s output.
func TestRulesetsExpand_NoDuplicateIDOnReapply(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{Resolver: &resolver.Store{}})

	// Wire the rulesets store + a stub syncer that primes cached content
	// so Expand() has something to emit.
	store := rulesets.New(srv.DB)
	srv.Rulesets = store

	// Seed one ruleset with cached content — two DOMAIN-SUFFIX lines that
	// will Expand into rules with GroupID = "list", IDs "list-1" and
	// "list-2".
	if err := store.Upsert(context.Background(), rulesets.Ruleset{
		Name:      "list",
		SourceURL: "https://example.test/list.txt",
		Kind:      rulesets.KindClash,
		Action:    "PROXY",
		Priority:  500,
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	content := []byte("DOMAIN-SUFFIX,alpha.example,PROXY\nDOMAIN-SUFFIX,beta.example,PROXY\n")
	if err := store.UpdateContent(context.Background(), "list", content, 2, "", ""); err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}

	// Sanity check Expand — the test is meaningful only if Expand actually
	// returns the deterministic-ID entries we expect to collide on re-apply.
	expanded, err := store.Expand(context.Background())
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(expanded) != 2 || expanded[0].GroupID != "list" || expanded[0].ID != "list-1" {
		t.Fatalf("Expand returned unexpected shape: %+v", expanded)
	}

	// First apply: send one manual rule; server appends Expand() and
	// snapshots manual + expanded into rules_yaml. This is the state a
	// real operator lands in after registering their first ruleset.
	manual := rules.Rule{
		ID: "r1", Kind: rules.KindDomainSuffix, Pattern: "manual.example",
		Action: "PROXY", Priority: 10, Enabled: true,
	}
	body1 := map[string]any{"rules": []rules.Rule{manual}, "note": "first apply"}
	rr := authPost(t, srv, "/api/v1/rules/apply", token, body1)
	if rr.Code != http.StatusOK {
		t.Fatalf("first apply: %d %s", rr.Code, rr.Body.String())
	}

	// Give the async resolver rebuild a moment to settle so the second
	// apply lands on a stable state.
	time.Sleep(50 * time.Millisecond)

	// Simulate a panel refresh: GET /api/v1/rules returns manual + expanded.
	rr = authGet(t, srv, "/api/v1/rules", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("list rules: %d %s", rr.Code, rr.Body.String())
	}
	listed := decode[ruleDoc](t, rr)
	if len(listed.Rules) != 3 {
		t.Fatalf("expected manual + 2 expanded, got %d: %+v", len(listed.Rules), listed.Rules)
	}
	grouped := 0
	for _, r := range listed.Rules {
		if r.GroupID == "list" {
			grouped++
		}
	}
	if grouped != 2 {
		t.Fatalf("expected 2 rules tagged GroupID=list, got %d", grouped)
	}

	// Second apply: panel echoes the entire listing back (this is what
	// Rules.tsx's stripDrafts sends), plus the operator's edit — one new
	// manual rule so the snapshot hash differs from the first apply and
	// the unique config_hash constraint on snapshots stays out of the
	// picture. Pre-fix, the server re-Expand()s and hits "duplicate id";
	// post-fix, GroupID-tagged rows are stripped server-side before Expand.
	edited := append([]rules.Rule{}, listed.Rules...)
	edited = append(edited, rules.Rule{
		ID: "r2", Kind: rules.KindDomainSuffix, Pattern: "extra.example",
		Action: "PROXY", Priority: 20, Enabled: true,
	})
	body2 := map[string]any{"rules": edited, "note": "reapply"}
	rr = authPost(t, srv, "/api/v1/rules/apply", token, body2)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-apply must succeed post-fix, got %d %s", rr.Code, rr.Body.String())
	}

	// Same shape for dry-run — same trap, same fix.
	body3 := map[string]any{"rules": listed.Rules, "fixtures": []any{}}
	rr = authPost(t, srv, "/api/v1/rules/dry-run", token, body3)
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run must succeed post-fix, got %d %s", rr.Code, rr.Body.String())
	}

	// And apply-preview.
	body4 := map[string]any{"rules": listed.Rules}
	rr = authPost(t, srv, "/api/v1/rules/apply/preview", token, body4)
	if rr.Code != http.StatusOK {
		t.Fatalf("apply/preview must succeed post-fix, got %d %s", rr.Code, rr.Body.String())
	}
}
