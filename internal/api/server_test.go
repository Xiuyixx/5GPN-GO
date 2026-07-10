package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	handle, err := db.Open(db.Config{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return New(handle, Config{
		JWTSecret:  []byte("test-secret-only-not-for-prod"),
		SetupToken: "setup-token-for-tests",
		Issuer:     "test",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, srv *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	return rr
}

func jsonReq(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	buf := &bytes.Buffer{}
	if body != nil {
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, path, buf)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func decode[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rr.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	return v
}

func TestHealth(t *testing.T) {
	rr := do(t, testServer(t), jsonReq(t, "GET", "/api/v1/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestBootstrapThenLoginThenApply(t *testing.T) {
	srv := testServer(t)

	// bootstrap status: needs_setup=true
	rr := do(t, srv, jsonReq(t, "GET", "/api/v1/bootstrap", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("bootstrap status: %d", rr.Code)
	}
	status := decode[map[string]any](t, rr)
	if status["needs_setup"] != true {
		t.Fatalf("want needs_setup=true, got %v", status)
	}

	// claim bootstrap
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/bootstrap", map[string]string{
		"token": "setup-token-for-tests", "username": "admin", "password": "supersecret1",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("bootstrap claim: %d (%s)", rr.Code, rr.Body.String())
	}

	// re-claim must fail (token cleared or user exists)
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/bootstrap", map[string]string{
		"token": "setup-token-for-tests", "username": "admin2", "password": "supersecret2",
	}))
	if rr.Code != http.StatusForbidden && rr.Code != http.StatusConflict {
		t.Fatalf("second bootstrap should be rejected: %d", rr.Code)
	}

	// login
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin", "password": "supersecret1",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d (%s)", rr.Code, rr.Body.String())
	}
	token := decode[map[string]string](t, rr)["token"]
	if token == "" {
		t.Fatalf("empty token")
	}

	// dry-run
	dryRunReq := map[string]any{
		"rules": []map[string]any{
			{"id": "cn", "kind": "DOMAIN-SUFFIX", "pattern": "cn", "action": "direct", "priority": 10, "enabled": true},
			{"id": "match", "kind": "MATCH", "pattern": "", "action": "wg1", "priority": 100, "enabled": true},
		},
		"fixtures": []map[string]string{
			{"domain": "baidu.cn", "expected_exit": "direct"},
			{"domain": "example.com", "expected_exit": "wg1"},
		},
	}
	req := jsonReq(t, "POST", "/api/v1/rules/dry-run", dryRunReq)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run: %d (%s)", rr.Code, rr.Body.String())
	}
	dryRes := decode[map[string]any](t, rr)
	if dryRes["passed"] != float64(2) {
		t.Fatalf("want 2 passed, got %v", dryRes)
	}

	// apply
	applyReq := map[string]any{
		"rules": dryRunReq["rules"],
		"note":  "integration test",
	}
	req = jsonReq(t, "POST", "/api/v1/rules/apply", applyReq)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("apply: %d (%s)", rr.Code, rr.Body.String())
	}
	applyRes := decode[map[string]any](t, rr)
	if _, ok := applyRes["snapshot_id"]; !ok {
		t.Fatalf("apply missing snapshot_id: %v", applyRes)
	}
	if applyRes["rolled_back"] != false {
		t.Fatalf("apply should not have rolled back with NoOp orchestrator: %v", applyRes)
	}

	// list rules
	req, _ = http.NewRequest("GET", "/api/v1/rules", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rr.Code, rr.Body.String())
	}
	list := decode[map[string]any](t, rr)
	items, _ := list["rules"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 rules, got %d", len(items))
	}

	// logout revokes session
	req, _ = http.NewRequest("POST", "/api/v1/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = do(t, srv, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rr.Code)
	}
	// subsequent request with same token should be 401
	req, _ = http.NewRequest("GET", "/api/v1/rules", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = do(t, srv, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d", rr.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv := testServer(t)
	// seed a user directly so we can hit the wrong-password path
	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/bootstrap", map[string]string{
		"token": "setup-token-for-tests", "username": "u", "password": "correcthorse",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed: %d (%s)", rr.Code, rr.Body.String())
	}

	// Blast 20 wrong-password attempts from the same IP. We expect at least
	// one 429 (rate limit) or 401 that transitions to a lockout.
	sawBlock := false
	for i := 0; i < 20; i++ {
		req := jsonReq(t, "POST", "/api/v1/login", map[string]string{
			"username": "u", "password": "wrong",
		})
		req.RemoteAddr = "192.0.2.10:12345"
		rr := do(t, srv, req)
		if rr.Code == http.StatusTooManyRequests {
			sawBlock = true
			break
		}
	}
	if !sawBlock {
		t.Fatalf("expected rate limiter or lockout to trigger within 20 attempts")
	}
}

func TestUnauthorizedNoToken(t *testing.T) {
	srv := testServer(t)
	req, _ := http.NewRequest("GET", "/api/v1/rules", nil)
	rr := do(t, srv, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "unauthorized") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}
