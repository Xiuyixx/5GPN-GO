package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
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
// rules/active.yaml, import it, confirm entries+applied come back.
func TestHandleImportBackup_HappyPath(t *testing.T) {
	srv, tok := s2Setup(t)

	// Build a minimal tar.gz in memory.
	buf, err := buildBackupTar(map[string]string{
		"rules/active.yaml": "- id: r1\n  kind: MATCH\n  action: direct\n  priority: 10\n  enabled: true\n",
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
}

func TestJournalHelpers(t *testing.T) {
	// isUnitChar allows only safe characters for the journalctl arg.
	safe := "dnsdist-1.2_a"
	for _, r := range safe {
		if !isUnitChar(r) {
			t.Errorf("expected %q to be a valid unit char", r)
		}
	}
	for _, r := range " ;`$()<>" {
		if isUnitChar(r) {
			t.Errorf("expected %q to be REJECTED", r)
		}
	}

	// journalTimestamp: prefer __REALTIME_TIMESTAMP when present as a
	// string; falls back to "now" formatted RFC3339Nano when absent.
	if got := journalTimestamp(map[string]any{"__REALTIME_TIMESTAMP": "1704067200000000"}); got != "1704067200000000" {
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
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(body)),
		}); err != nil {
			return "", err
		}
		if _, err := tw.Write([]byte(body)); err != nil {
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
