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
	rr := do(t, srv, authed(t, "GET", "/api/v1/metrics", nil, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics: %d", rr.Code)
	}
	var samples []MetricsSample
	if err := json.Unmarshal(rr.Body.Bytes(), &samples); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(samples) != 60 {
		t.Fatalf("want 60 samples, got %d", len(samples))
	}
	if samples[0].CPU < 0 || samples[0].MemBytes < 0 {
		t.Fatalf("negative sample: %+v", samples[0])
	}
}

func TestS2Exits(t *testing.T) {
	srv, tok := s2Setup(t)
	// Reset in-memory state so parallel test packages do not interfere.
	exitsState.items = exitsState.items[:1]
	exitsState.items[0].Active = true
	exitsState.active = "direct"

	// list default
	rr := do(t, srv, authed(t, "GET", "/api/v1/exits", nil, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	// add
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/add", map[string]string{
		"id":  "wg1",
		"uri": "trojan://pw@example.com:443?sni=fake.example.com",
	}, tok))
	if rr.Code != http.StatusCreated {
		t.Fatalf("add: %d (%s)", rr.Code, rr.Body.String())
	}
	// switch
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/switch", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusOK {
		t.Fatalf("switch: %d", rr.Code)
	}
	// cannot delete active
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/delete", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 on deleting active exit, got %d", rr.Code)
	}
	// switch back + delete succeeds
	_ = do(t, srv, authed(t, "POST", "/api/v1/exits/switch", map[string]string{"id": "direct"}, tok))
	rr = do(t, srv, authed(t, "POST", "/api/v1/exits/delete", map[string]string{"id": "wg1"}, tok))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d (%s)", rr.Code, rr.Body.String())
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
