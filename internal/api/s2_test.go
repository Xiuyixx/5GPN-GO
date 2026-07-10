package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// s2Setup boots a server, claims bootstrap, and returns a Bearer token.
func s2Setup(t *testing.T) (*Server, string) {
	t.Helper()
	srv := testServer(t)
	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/bootstrap", map[string]string{
		"token": "setup-token-for-tests", "username": "admin", "password": "correcthorse",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("bootstrap: %d (%s)", rr.Code, rr.Body.String())
	}
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin", "password": "correcthorse",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d", rr.Code)
	}
	tok := decode[map[string]string](t, rr)["token"]
	return srv, tok
}

func authed(t *testing.T, method, path string, body any, token string) *http.Request {
	t.Helper()
	r := jsonReq(t, method, path, body)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestS2Metrics(t *testing.T) {
	srv, tok := s2Setup(t)
	// Seed a couple of metrics rows so the endpoint has something to return
	// (M2 S4 replaced the synthetic in-handler generator with a SQLite read).
	if _, err := srv.DB.Exec(
		`INSERT INTO metrics_snapshot(ts, cpu, mem, conns, tx_bytes, rx_bytes) VALUES(?, ?, ?, ?, ?, ?)`,
		"2026-07-10T12:00:00Z", 12.3, 200_000_000, 100, 12345, 67890,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.Exec(
		`INSERT INTO metrics_snapshot(ts, cpu, mem, conns, tx_bytes, rx_bytes) VALUES(?, ?, ?, ?, ?, ?)`,
		"2026-07-10T12:00:10Z", 15.5, 210_000_000, 105, 22345, 77890,
	); err != nil {
		t.Fatal(err)
	}
	rr := do(t, srv, authed(t, "GET", "/api/v1/metrics", nil, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics: %d", rr.Code)
	}
	var samples []MetricsSample
	if err := json.Unmarshal(rr.Body.Bytes(), &samples); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("want 2 samples, got %d", len(samples))
	}
	if samples[1].CPU != 15.5 {
		t.Errorf("second sample CPU: %v", samples[1].CPU)
	}
}

func TestS2Exits(t *testing.T) {
	srv, tok := s2Setup(t)

	// list default: migration seeds 'direct' active=1.
	rr := do(t, srv, authed(t, "GET", "/api/v1/exits", nil, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	var listed struct {
		Exits  []ExitSummary `json:"exits"`
		Active string        `json:"active"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Active != "direct" {
		t.Fatalf("initial active want 'direct', got %q", listed.Active)
	}
	if len(listed.Exits) != 1 || listed.Exits[0].ID != "direct" {
		t.Fatalf("initial list want [direct], got %+v", listed.Exits)
	}

	// add
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/add", map[string]string{
		"id":  "wg1",
		"uri": "trojan://pw@example.com:443?sni=fake.example.com",
	}, tok))
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}
	// list now contains 2 with direct still active + active-first ordering.
	rr = do(t, srv, authed(t, "GET", "/api/v1/exits", nil, tok))
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list after add: %v", err)
	}
	if len(listed.Exits) != 2 || listed.Exits[0].ID != "direct" || !listed.Exits[0].Active {
		t.Fatalf("post-add ordering broken: %+v", listed.Exits)
	}

	// switch
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/switch", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("switch: %d (%s)", rr.Code, rr.Body.String())
	}
	// list must reflect new active + active-first ordering
	rr = do(t, srv, authed(t, "GET", "/api/v1/exits", nil, tok))
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list after switch: %v", err)
	}
	if listed.Active != "wg1" {
		t.Fatalf("post-switch active want 'wg1', got %q", listed.Active)
	}
	if listed.Exits[0].ID != "wg1" || !listed.Exits[0].Active {
		t.Fatalf("post-switch active-first broken: %+v", listed.Exits)
	}

	// cannot delete active
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/delete", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 on deleting active exit, got %d (%s)", rr.Code, rr.Body.String())
	}

	// switch back + delete succeeds
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/switch", map[string]string{"id": "direct"}, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("switch back: %d", rr.Code)
	}
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/delete", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d (%s)", rr.Code, rr.Body.String())
	}

	// bad requests
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/add", map[string]string{"id": "!!bad", "uri": "trojan://pw@x:443"}, tok))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad-id add: want 400, got %d", rr.Code)
	}
	rr = do(t, srv, authed(t, "POST", "/api/v1/switch/does-not-exist", map[string]string{"id": "ghost"}, tok))
	if rr.Code == http.StatusOK {
		t.Fatalf("switch to unknown must not 200")
	}
}

// TestS2ExitsRestartParity boots a server, adds + switches an exit, then
// tears the *Server down and rebuilds a fresh *Server on the SAME DB
// handle. The listing after restart must still show the pre-restart
// active row selected, proving the DB is the truth source (plan AC5).
func TestS2ExitsRestartParity(t *testing.T) {
	srv, tok := s2Setup(t)

	rr := do(t, srv, authed(t, "POST", "/api/v1/exits/add", map[string]string{
		"id": "wg1", "uri": "trojan://pw@example.com:443?sni=fake.example.com",
	}, tok))
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/switch", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("switch: %d", rr.Code)
	}

	// Simulated restart: build a fresh Server around the same DB (same
	// process, same handle — mirrors a Router-only lifecycle without
	// re-opening SQLite so the test stays hermetic).
	freshHandle := srv.DB
	fresh := New(freshHandle, Config{
		JWTSecret:  []byte("test-secret-only-not-for-prod"),
		SetupToken: "setup-token-for-tests",
		Issuer:     "test",
	}, srv.Logger)

	rr = do(t, fresh, authed(t, "GET", "/api/v1/exits", nil, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("post-restart list: %d", rr.Code)
	}
	var listed struct {
		Exits  []ExitSummary `json:"exits"`
		Active string        `json:"active"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.Active != "wg1" {
		t.Fatalf("restart parity broken: active=%q want 'wg1'", listed.Active)
	}
	if listed.Exits[0].ID != "wg1" {
		t.Fatalf("restart parity: active-first ordering broken: %+v", listed.Exits)
	}
}

func TestS2BackupExportImport(t *testing.T) {
	srv, tok := s2Setup(t)
	// export
	req := jsonReq(t, "GET", "/api/v1/backup/export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "gzip") {
		t.Fatalf("wrong content-type: %s", rr.Header().Get("Content-Type"))
	}
	body := rr.Body.Bytes()

	// import — validate we can round-trip the tar.gz.
	buf := bytes.NewReader(body)
	imp, _ := http.NewRequest("POST", "/api/v1/backup/import", buf)
	imp.Header.Set("Authorization", "Bearer "+tok)
	imp.Header.Set("Content-Type", "application/gzip")
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, imp)
	if rr.Code != http.StatusOK {
		t.Fatalf("import: %d (%s)", rr.Code, rr.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if _, ok := res["entries"]; !ok {
		t.Fatalf("import missing entries: %s", rr.Body.String())
	}
}

func TestS2BackupImportRejectsGarbage(t *testing.T) {
	srv, tok := s2Setup(t)
	body := bytes.NewReader([]byte("not a gzip file"))
	req, _ := http.NewRequest("POST", "/api/v1/backup/import", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-gzip, got %d", rr.Code)
	}
}

func TestS2SnapshotsListEmpty(t *testing.T) {
	srv, tok := s2Setup(t)
	rr := do(t, srv, authed(t, "GET", "/api/v1/snapshots", nil, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("snapshots list: %d", rr.Code)
	}
	res := decode[map[string]any](t, rr)
	arr, _ := res["snapshots"].([]any)
	if arr == nil { // could be nil for empty; either is fine
		return
	}
	if len(arr) > 0 {
		t.Logf("existing snapshots: %d", len(arr))
	}
}

// Confirm gzip stream is well-formed by decompressing.
func TestS2BackupExportIsValidGzip(t *testing.T) {
	srv, tok := s2Setup(t)
	req := jsonReq(t, "GET", "/api/v1/backup/export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: %d", rr.Code)
	}
	gz, err := gzip.NewReader(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	_ = gz.Close()
}
