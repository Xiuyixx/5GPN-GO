package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
)

var errAny = errors.New("synthetic")

// Coverage-focused tests for handlers the S2 batch could not reach:
// handleMe (needs a live bearer), handleRollbackSnapshot, and the
// journalctl helpers (isUnitChar / journalTimestamp / journalPriority /
// asString / errorFrame). The logs SSE loop itself is exercised by the
// tests/e2e/ smoke — driving it in-process would leak a goroutine.

func TestHandleMe_ReturnsUsername(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("me: %d (%s)", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["username"] != "admin" {
		t.Errorf("username = %v, want admin", out["username"])
	}
	if _, ok := out["user_id"]; !ok {
		t.Errorf("me missing user_id: %s", rr.Body.String())
	}
}

func TestHandleMe_MissingAuthReturns401(t *testing.T) {
	srv := testServer(t)
	req := jsonReq(t, "GET", "/api/v1/me", nil)
	rr := do(t, srv, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("me without token: want 401, got %d", rr.Code)
	}
}

func TestHandleRollbackSnapshot_NotFound(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "POST", "/api/v1/snapshots/999999/rollback", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("rollback missing snapshot: want 404, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleRollbackSnapshot_BadID(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "POST", "/api/v1/snapshots/not-a-number/rollback", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("rollback bad id: want 400, got %d", rr.Code)
	}
}

// handleRollbackSnapshot round-trip: apply → find snapshot id → rollback → 200.
func TestHandleRollbackSnapshot_RoundTrip(t *testing.T) {
	srv, tok := s2Setup(t)

	// Seed a rule apply so a snapshot + rule_version exist and can be
	// re-activated by the rollback handler. Pass native maps — jsonReq
	// takes care of marshaling.
	applyBody := map[string]any{
		"rules": []map[string]any{{
			"id": "s", "kind": "MATCH", "pattern": "", "action": "direct", "priority": 100, "enabled": true,
		}},
		"note": "coverage-test",
	}
	rr := do(t, srv, authed(t, "POST", "/api/v1/rules/apply", applyBody, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("apply: %d (%s)", rr.Code, rr.Body.String())
	}
	var applyRes map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &applyRes)
	sid, ok := applyRes["snapshot_id"].(float64)
	if !ok || sid == 0 {
		t.Fatalf("no snapshot_id in apply response: %s", rr.Body.String())
	}

	// Rollback should succeed now that a rule_version is bound to this snapshot.
	rr = do(t, srv, authed(t,
		"POST", fmt.Sprintf("/api/v1/snapshots/%d/rollback", int64(sid)), nil, tok))
	if rr.Code != http.StatusOK {
		t.Errorf("rollback: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// Note: SSE handler paths are covered end-to-end by tests/e2e/ against
// a real HTTP server. httptest.ResponseRecorder never cancels the
// request context, so any in-process test that reaches the streaming
// select-loop leaks the producer goroutine and hangs the suite. The
// isUnitChar helper is exercised directly in TestJournalHelpers.

func TestHandleImportBackup_RejectsMalformedGzip(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "POST", "/api/v1/backup/import", "garbage")
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/gzip")
	rr := do(t, srv, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("garbage import: want 400, got %d", rr.Code)
	}
}

// serveIndex reads index.html out of the embedded FS. Drive it with an
// in-memory FS so the router's SPA-fallback path is exercised without
// the full production build pipeline.
func TestServeIndex_WithEmbeddedFS(t *testing.T) {
	srv := testServer(t)
	srv.WebFS = fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>panel</html>")},
	}

	// Unknown non-/api path → SPA fallback → serveIndex → 200 + html.
	rr := do(t, srv, jsonReq(t, "GET", "/some/deep/spa/route", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("spa fallback: %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "<html>panel</html>") {
		t.Errorf("body missing index.html: %s", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type: %s", ct)
	}
}

// serveIndex hits the 500 path when the embedded FS has no index.html.
func TestServeIndex_MissingIndexReturns500(t *testing.T) {
	srv := testServer(t)
	srv.WebFS = fstest.MapFS{} // empty — no index.html
	rr := do(t, srv, jsonReq(t, "GET", "/does/not/matter", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("missing index: want 500, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// -----------------------------------------------------------------
// Error-branch coverage: exercise the 400/401/409 paths of each of
// the panel/rules/exits handlers.
// -----------------------------------------------------------------

func TestHandleLogin_BadPasswordReturns401(t *testing.T) {
	srv, _ := s2Setup(t)
	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	}))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("bad pw: want 401, got %d", rr.Code)
	}
}

func TestHandleLogin_UnknownUserReturns401(t *testing.T) {
	srv, _ := s2Setup(t)
	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "ghost",
		"password": "correcthorse",
	}))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("unknown user: want 401, got %d", rr.Code)
	}
}

func TestHandleLogin_MalformedJSONReturns400(t *testing.T) {
	srv := testServer(t)
	req, _ := http.NewRequest("POST", "/api/v1/login", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := do(t, srv, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("malformed json: want 400, got %d", rr.Code)
	}
}

func TestHandleLogout_ReturnsNoContent(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "POST", "/api/v1/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("logout: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	// Same token must now be rejected — proves session revocation is enforced.
	req = jsonReq(t, "GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = do(t, srv, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("revoked token still works: %d", rr.Code)
	}
}

func TestHandleBootstrapStatus_AlreadyClaimed(t *testing.T) {
	srv, _ := s2Setup(t) // s2Setup already claims bootstrap
	rr := do(t, srv, jsonReq(t, "GET", "/api/v1/bootstrap", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var out map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["needs_setup"] {
		t.Errorf("post-claim: needs_setup should be false")
	}
}

func TestHandleBootstrapClaim_BadToken(t *testing.T) {
	srv := testServer(t)
	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/bootstrap", map[string]string{
		"token": "wrong-token", "username": "admin", "password": "hunter2",
	}))
	if rr.Code == http.StatusCreated {
		t.Errorf("bad token accepted!")
	}
}

func TestHandleAddExit_InvalidURI(t *testing.T) {
	srv, tok := s2Setup(t)
	rr := do(t, srv, authed(t, "POST", "/api/v1/exits/add", map[string]string{
		"id":  "bad",
		"uri": "not-a-real-uri://",
	}, tok))
	if rr.Code == http.StatusCreated {
		t.Errorf("bogus URI accepted: %s", rr.Body.String())
	}
}

func TestHandleListRules_ReturnsEmpty(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "GET", "/api/v1/rules", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"rules"`) {
		t.Errorf("empty response missing 'rules' key: %s", rr.Body.String())
	}
}

func TestHandleApply_MalformedBodyReturns400(t *testing.T) {
	srv, tok := s2Setup(t)
	req, _ := http.NewRequest("POST", "/api/v1/rules/apply", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := do(t, srv, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad apply body: want 400, got %d", rr.Code)
	}
}

// handleImportBackup happy path: build a real tar.gz containing
// rules/active.yaml, import it, confirm entries+applied+apply_result come back.
// Uses the NoOp orchestrator that testServer wires by default — applier
// returns health="" which the applier maps to confirmed.
func TestHandleImportBackup_HappyPath(t *testing.T) {
	srv, tok := s2Setup(t)

	// Build a minimal tar.gz in memory. Real backup exports emit a
	// rules-keyed document via rules.MarshalYAML, so the fixture matches.
	buf, err := buildBackupTar(map[string]string{
		"rules/active.yaml": "rules:\n- id: r1\n  kind: MATCH\n  pattern: \"\"\n  action: direct\n  priority: 10\n  enabled: true\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", "/api/v1/backup/import", strings.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/gzip")
	rr := do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("import: %d (%s)", rr.Code, rr.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if entries, _ := res["entries"].(float64); entries < 1 {
		t.Errorf("expected entries >=1, got %v", res["entries"])
	}
	if applied, _ := res["applied"].(bool); !applied {
		t.Errorf("expected applied=true, got %v (%s)", res["applied"], rr.Body.String())
	}
	if _, ok := res["applied_snapshot_id"]; !ok {
		t.Errorf("expected applied_snapshot_id in response: %s", rr.Body.String())
	}
	apply, ok := res["apply_result"].(map[string]any)
	if !ok {
		t.Fatalf("expected apply_result object, got %v", res["apply_result"])
	}
	if apply["rolled_back"] != false {
		t.Errorf("apply_result.rolled_back = %v, want false", apply["rolled_back"])
	}

	// Post-condition: the just-imported YAML is now the active rule version.
	active, err := db.GetActiveRuleVersion(srv.DB)
	if err != nil {
		t.Fatalf("no active rule version after import: %v", err)
	}
	if !strings.Contains(active.RulesYAML, "id: r1") {
		t.Errorf("active rule_version does not carry imported content: %q", active.RulesYAML)
	}
}

// TestHandleImportBackup_OrchestratorFailure covers the negative path
// mandated by AC8: when the apply pipeline reports an error, the DB
// active-rule pointer must roll back to the prior version, the audit
// log must carry a `backup.restore.rolled_back` entry, and the response
// must be non-2xx. This guarantees "applied" never means "DB advanced
// but data plane unchanged" (Critic F3, plan v3.2 AC8 negative path).
func TestHandleImportBackup_OrchestratorFailure(t *testing.T) {
	srv, tok := s2Setup(t)

	// Seed a pre-existing active rule version so the rollback has a
	// target to restore. Uses the standard apply path with the NoOp
	// orchestrator so no faults are triggered.
	seedBody := map[string]any{
		"rules": []map[string]any{{
			"id": "seed", "kind": "MATCH", "pattern": "",
			"action": "direct", "priority": 100, "enabled": true,
		}},
		"note": "seed prior active",
	}
	rr := do(t, srv, authed(t, "POST", "/api/v1/rules/apply", seedBody, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("seed apply: %d (%s)", rr.Code, rr.Body.String())
	}
	priorActive, err := db.GetActiveRuleVersion(srv.DB)
	if err != nil {
		t.Fatalf("no active version after seed: %v", err)
	}

	// Swap in a failing orchestrator on both the server AND the applier
	// it shares — the API layer talks through Applier.Orch, so mutating
	// only server.Orchestrator would leave the applier on the NoOp.
	failing := &failingOrchestrator{err: errAny}
	srv.Orchestrator = failing
	srv.Applier.Orch = failing

	buf, err := buildBackupTar(map[string]string{
		"rules/active.yaml": "rules:\n- id: imported\n  kind: MATCH\n  pattern: \"\"\n  action: direct\n  priority: 10\n  enabled: true\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", "/api/v1/backup/import", strings.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/gzip")
	rr = do(t, srv, req)
	if rr.Code < 400 {
		t.Fatalf("import should have failed, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Active rule version must have rolled back to what was seeded.
	after, err := db.GetActiveRuleVersion(srv.DB)
	if err != nil {
		t.Fatalf("no active rule version after rollback: %v", err)
	}
	if after.ID != priorActive.ID {
		t.Errorf("active rule_version id = %d, want %d (rollback did not restore prior)",
			after.ID, priorActive.ID)
	}

	// Audit log must show a rolled_back entry for backup.restore.
	rows, err := srv.DB.Query(
		`SELECT action FROM audit_log WHERE action LIKE 'backup.restore%' ORDER BY id DESC LIMIT 3`,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		if action == "backup.restore.rolled_back" {
			found = true
		}
	}
	if !found {
		t.Errorf("audit_log missing backup.restore.rolled_back entry")
	}
}

func TestHandleImportBackupValidatesWholeArchiveBeforeApply(t *testing.T) {
	srv, tok := s2Setup(t)
	seed := map[string]any{
		"rules": []map[string]any{{
			"id": "seed", "kind": "MATCH", "pattern": "",
			"action": "direct", "priority": 100, "enabled": true,
		}},
		"note": "seed",
	}
	rr := do(t, srv, authed(t, "POST", "/api/v1/rules/apply", seed, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", rr.Code, rr.Body.String())
	}
	prior, _ := db.GetActiveRuleVersion(srv.DB)

	body, err := buildBackupEntries([]backupTestMember{
		{Name: "rules/active.yaml", Body: "rules:\n- id: imported\n  kind: MATCH\n  action: block\n  priority: 1\n  enabled: true\n"},
		{Name: "unexpected/file", Body: "must reject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", "/api/v1/backup/import", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = do(t, srv, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown member: want 400, got %d %s", rr.Code, rr.Body.String())
	}
	after, _ := db.GetActiveRuleVersion(srv.DB)
	if after.ID != prior.ID {
		t.Fatalf("archive applied before final validation: active=%d prior=%d", after.ID, prior.ID)
	}
}

func TestHandleImportBackupRejectsDuplicateRulesAndLinks(t *testing.T) {
	srv, tok := s2Setup(t)
	rulesBody := "rules:\n- id: r\n  kind: MATCH\n  action: direct\n  priority: 1\n  enabled: true\n"
	for name, members := range map[string][]backupTestMember{
		"duplicate": {
			{Name: "rules/active.yaml", Body: rulesBody},
			{Name: "rules/active.yaml", Body: rulesBody},
		},
		"symlink": {{Name: "rules/active.yaml", Typeflag: tar.TypeSymlink}},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := buildBackupEntries(members)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("POST", "/api/v1/backup/import", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			rr := do(t, srv, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleImportBackupRejectsCompressedAndExpandedLimits(t *testing.T) {
	srv, tok := s2Setup(t)
	req, _ := http.NewRequest("POST", "/api/v1/backup/import", strings.NewReader("short"))
	req.ContentLength = backupMaxCompressedBytes + 1
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("compressed limit: want 413, got %d %s", rr.Code, rr.Body.String())
	}

	large := strings.Repeat("x", backupMaxRulesBytes+1)
	body, err := buildBackupEntries([]backupTestMember{{Name: "rules/active.yaml", Body: large}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("POST", "/api/v1/backup/import", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr = do(t, srv, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expanded rules limit: want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

// failingOrchestrator returns err from Apply, letting Applier.ImportRules
// bubble the error out and drive the handler's rollback branch.
type failingOrchestrator struct{ err error }

func (f *failingOrchestrator) Apply(_ context.Context, _ orchestrator.ApplyRequest) (orchestrator.ApplyResult, error) {
	return orchestrator.ApplyResult{}, f.err
}

func (f *failingOrchestrator) Rollback(_ context.Context, _ int64) error { return nil }

func TestJournalHelpers(t *testing.T) {
	for _, unit := range []string{"5gpn", "dnsdist", "mihomo", "sniproxy", "mtg"} {
		if _, ok := allowedLogUnits[unit]; !ok {
			t.Errorf("expected %q in log-unit allowlist", unit)
		}
	}
	if _, ok := allowedLogUnits["ssh.service"]; ok {
		t.Error("unrelated system service must not be readable through panel logs")
	}

	// journalTimestamp: convert journalctl's microsecond epoch into the
	// RFC3339 timestamp consumed by the browser; fall back to now when absent.
	if got := journalTimestamp(map[string]any{"__REALTIME_TIMESTAMP": "1704067200000000"}); got != "2024-01-01T00:00:00Z" {
		t.Errorf("realtime timestamp = %q", got)
	}
	if journalTimestamp(map[string]any{}) == "" {
		t.Errorf("fallback timestamp should format Now(), not empty")
	}

	// journalPriority: numeric strings map to level names per the switch
	// in logs.go — 0/1/2/3 → error, 4 → warn, 5/6 → info, 7 → debug,
	// anything else → info.
	cases := map[string]string{
		"0": "error", "1": "error", "3": "error",
		"4": "warn", "5": "info", "6": "info", "7": "debug",
		"not-a-num": "info", "": "info",
	}
	for in, want := range cases {
		if got := journalPriority(map[string]any{"PRIORITY": in}); got != want {
			t.Errorf("journalPriority(%q) = %q, want %q", in, got, want)
		}
	}

	// asString handles nil, string, and float64 (json.Unmarshal emits
	// float64 for JSON numbers). []byte is not in the type switch.
	if asString(nil) != "" {
		t.Error("asString(nil) should be empty")
	}
	if asString("hello") != "hello" {
		t.Error("asString(string)")
	}
	if asString(float64(42)) != "42" {
		t.Errorf("asString(float64) = %q, want 42", asString(float64(42)))
	}

	// errorFrame wraps the message in the SSE JSON envelope.
	frame := errorFrame("dnsdist", errAny)
	if !strings.Contains(frame, `"unit":"dnsdist"`) || !strings.Contains(frame, `"level":"error"`) {
		t.Errorf("errorFrame missing fields: %s", frame)
	}
}

// buildBackupTar produces a gzip'd tar carrying the given members in
// memory. Used to feed handleImportBackup its happy-path input.
func buildBackupTar(members map[string]string) (string, error) {
	entries := make([]backupTestMember, 0, len(members))
	for name, body := range members {
		entries = append(entries, backupTestMember{Name: name, Body: body})
	}
	return buildBackupEntries(entries)
}

type backupTestMember struct {
	Name     string
	Body     string
	Typeflag byte
}

func buildBackupEntries(members []backupTestMember) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, member := range members {
		typeflag := member.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: member.Name, Mode: 0o600, Size: int64(len(member.Body)), Typeflag: typeflag,
		}); err != nil {
			return "", err
		}
		if _, err := tw.Write([]byte(member.Body)); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
