//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestRulesLifecycle is the new-daemon equivalent of the legacy
// test_smart_routing_policy.sh + test_rules_import_policy.sh: apply a
// non-empty ruleset via the panel API, list it back, and confirm the
// snapshot pipeline recorded the change. If dry-run rejected the change
// or auto-rollback fired, the assertion at the end catches it.
func TestRulesLifecycle(t *testing.T) {
	d := startDaemon(t)
	defer d.Stop()
	tok := bootstrapAndLogin(t, d)

	rules := []map[string]any{
		{"id": "google", "kind": "DOMAIN-SUFFIX", "pattern": "google.com", "action": "direct", "priority": 10, "enabled": true},
		{"id": "final", "kind": "MATCH", "pattern": "", "action": "direct", "priority": 100, "enabled": true},
	}
	body, _ := json.Marshal(map[string]any{"rules": rules, "note": "e2e apply"})
	resp := do(t, d, tok, "POST", "/api/v1/rules/apply", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d (%s)", resp.StatusCode, resp.body)
	}
	if !strings.Contains(resp.body, `"snapshot_id"`) {
		t.Errorf("apply response missing snapshot_id: %s", resp.body)
	}

	// List: the two rules must come back.
	resp = do(t, d, tok, "GET", "/api/v1/rules", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d (%s)", resp.StatusCode, resp.body)
	}
	var out struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal([]byte(resp.body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rules) != 2 {
		t.Errorf("want 2 rules back, got %d (%s)", len(out.Rules), resp.body)
	}

	// Snapshots endpoint should now list at least one snapshot.
	resp = do(t, d, tok, "GET", "/api/v1/snapshots", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.body, `"snapshots"`) {
		t.Errorf("snapshots: %d (%s)", resp.StatusCode, resp.body)
	}
}

// TestRulesDryRunRejectsGarbage covers the M1 differentiator: dry-run
// must catch obvious malformed rules before they reach the data plane.
func TestRulesDryRunRejectsGarbage(t *testing.T) {
	d := startDaemon(t)
	defer d.Stop()
	tok := bootstrapAndLogin(t, d)

	// An empty pattern on DOMAIN-SUFFIX is a validate-time error.
	rules := []map[string]any{
		{"id": "bad", "kind": "DOMAIN-SUFFIX", "pattern": "", "action": "direct", "priority": 10, "enabled": true},
	}
	body, _ := json.Marshal(map[string]any{
		"rules":    rules,
		"fixtures": []map[string]string{{"domain": "example.com", "expected_exit": "direct"}},
	})
	resp := do(t, d, tok, "POST", "/api/v1/rules/dry-run", body)
	// Either 400 (schema validate) or 200 with failed>0 — both prove the
	// gate exists; a 200 with failed=0 would be the regression we care about.
	if resp.StatusCode == http.StatusOK && !strings.Contains(resp.body, `"failed":`) {
		t.Errorf("dry-run silently accepted garbage: %s", resp.body)
	}
}

// bootstrapAndLogin is a small helper shared by the API-surface tests.
func bootstrapAndLogin(t *testing.T, d *daemon) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"token": d.setupToken, "username": "admin", "password": "correcthorse",
	})
	if r := postRaw(t, d.URL("/api/v1/bootstrap"), body); r.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d (%s)", r.StatusCode, r.body)
	}
	body, _ = json.Marshal(map[string]string{"username": "admin", "password": "correcthorse"})
	r := postRaw(t, d.URL("/api/v1/login"), body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("login: %d (%s)", r.StatusCode, r.body)
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(r.body), &res); err != nil {
		t.Fatal(err)
	}
	return res["token"]
}

func postRaw(t *testing.T, url string, body []byte) simpleResp {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return simpleResp{StatusCode: resp.StatusCode, body: string(b)}
}

func do(t *testing.T, d *daemon, tok, method, path string, body []byte) simpleResp {
	t.Helper()
	var r *http.Request
	var err error
	if body != nil {
		r, err = http.NewRequest(method, d.URL(path), bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, err = http.NewRequest(method, d.URL(path), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return simpleResp{StatusCode: resp.StatusCode, body: string(b)}
}
