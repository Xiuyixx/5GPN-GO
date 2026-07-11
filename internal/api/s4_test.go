package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/updater"
)

// bootstrapAndLogin sets up an admin user in a fresh test server and
// returns (server, bearer token, tempBinaryPath). tempBinaryPath is a
// scratch file usable as UpdaterConfig.BinaryPath for swap-target tests.
func bootstrapAndLogin(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	handle, err := db.Open(db.Config{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	if cfg.JWTSecret == nil {
		cfg.JWTSecret = []byte("s4-test-secret")
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "test"
	}
	cfg.SetupToken = "s4-setup"
	srv := New(handle, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/bootstrap", map[string]string{
		"token": "s4-setup", "username": "admin", "password": "supersecret1",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("bootstrap: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin", "password": "supersecret1",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	token := decode[map[string]string](t, rr)["token"]
	return srv, token
}

func authGet(t *testing.T, srv *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonReq(t, "GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return do(t, srv, r)
}

func authPost(t *testing.T, srv *Server, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonReq(t, "POST", path, body)
	r.Header.Set("Authorization", "Bearer "+token)
	return do(t, srv, r)
}

// ------------------------------------------------------------------
// T2: change password
// ------------------------------------------------------------------

func TestChangePassword_HappyPathAndKeepsCurrentSession(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})

	// mint a second session that must be revoked after change
	rr := do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin", "password": "supersecret1",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("second login: %d", rr.Code)
	}
	other := decode[map[string]string](t, rr)["token"]

	rr = authPost(t, srv, "/api/v1/password", token, map[string]string{
		"current": "supersecret1", "next": "newpassword9",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rr.Code, rr.Body.String())
	}

	// Current caller's session still usable.
	rr = authGet(t, srv, "/api/v1/me", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("current session unexpectedly killed: %d %s", rr.Code, rr.Body.String())
	}

	// The *other* session must be revoked.
	rr = authGet(t, srv, "/api/v1/me", other)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("other session should be revoked, got %d %s", rr.Code, rr.Body.String())
	}

	// New password now works, old one doesn't.
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin", "password": "newpassword9",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("login with new password: %d", rr.Code)
	}
	rr = do(t, srv, jsonReq(t, "POST", "/api/v1/login", map[string]string{
		"username": "admin", "password": "supersecret1",
	}))
	if rr.Code == http.StatusOK {
		t.Fatal("old password still accepted after change")
	}
}

func TestChangePassword_WrongCurrentRejected(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := authPost(t, srv, "/api/v1/password", token, map[string]string{
		"current": "wrong-guess", "next": "newpassword9",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestChangePassword_WeakRejected(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := authPost(t, srv, "/api/v1/password", token, map[string]string{
		"current": "supersecret1", "next": "short",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", rr.Code, rr.Body.String())
	}
}

// ------------------------------------------------------------------
// T3: iOS profile URL
// ------------------------------------------------------------------

// Since v0.2.7 the mobileconfig is served by the panel router itself on
// :443 (with the LE cert), not by the standalone :8111 listener. The URL
// therefore no longer carries a port, and the response no longer carries
// a "port" field. The UUID for "auto" (or empty) config is derived
// deterministically from the domain, so the same domain always yields
// the same UUID across reinstalls.

func TestIOSProfileURL_UsesDomainOnPanelPort(t *testing.T) {
	base := &config.Config{}
	base.Server.Domain = "gw.example.com"
	// A non-empty ProfileUUID that is not a valid RFC 4122 UUID and is
	// not the literal "auto" — the handler should still fall back to a
	// derived value here (invalid UUIDs are not trusted verbatim).
	// If the operator supplies a real UUID we honour it (see next test).
	base.IOS.ProfileUUID = "auto"
	srv, token := bootstrapAndLogin(t, Config{BaseConfig: base})
	rr := authGet(t, srv, "/api/v1/ios/profile-url", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("code: %d %s", rr.Code, rr.Body.String())
	}
	got := decode[map[string]any](t, rr)
	if got["url"] != "https://gw.example.com/ios-dot.mobileconfig" {
		t.Fatalf("unexpected url: %v", got["url"])
	}
	if _, ok := got["port"]; ok {
		t.Fatalf("port field should be absent since v0.2.7, got %v", got["port"])
	}
	uuid, _ := got["uuid"].(string)
	if uuid == "" || uuid == "auto" {
		t.Fatalf("uuid should be a derived value, got %q", uuid)
	}
}

func TestIOSProfileURL_HonoursExplicitUUID(t *testing.T) {
	base := &config.Config{}
	base.Server.Domain = "gw.example.com"
	base.IOS.ProfileUUID = "550e8400-e29b-41d4-a716-446655440000"
	srv, token := bootstrapAndLogin(t, Config{BaseConfig: base})
	rr := authGet(t, srv, "/api/v1/ios/profile-url", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("code: %d %s", rr.Code, rr.Body.String())
	}
	got := decode[map[string]any](t, rr)
	if got["uuid"] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("explicit uuid was rewritten: %v", got["uuid"])
	}
}

// ------------------------------------------------------------------
// T1: updater check + apply
// ------------------------------------------------------------------

// releaseServer stubs the GitHub Releases API + asset download endpoints
// enough for the updater client + handler to run end-to-end.
type releaseServer struct {
	*httptest.Server
	tag     string
	asset   string
	payload []byte
}

func newReleaseServer(t *testing.T, tag string, payload []byte, includeSums bool) *releaseServer {
	t.Helper()
	rs := &releaseServer{tag: tag, payload: payload}
	rs.asset = fmt.Sprintf("5gpn-%s-%s.tar.gz", tag, updater.ArtifactSuffix())
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])
	body := fmt.Sprintf("Notes\n%s  %s\n", rs.asset, sumHex)

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		assets := []map[string]any{
			{"name": rs.asset, "size": len(payload), "browser_download_url": rs.URL + "/dl/" + rs.asset},
		}
		if includeSums {
			assets = append(assets, map[string]any{
				"name":                 "SHA256SUMS",
				"size":                 128,
				"browser_download_url": rs.URL + "/dl/SHA256SUMS",
			})
		}
		rel := map[string]any{
			"tag_name": rs.tag, "name": rs.tag,
			"body":   body,
			"assets": assets,
		}
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			fmt.Fprintf(w, "%s  %s\n", sumHex, rs.asset)
			return
		}
		w.Write(payload)
	})
	rs.Server = httptest.NewServer(mux)
	return rs
}

// updaterClientRewrite builds a client whose Latest() hits our stub. The
// updater package hardcodes api.github.com so we swap its HTTPClient
// against a transport that rewrites the outbound URL.
func stubUpdaterClient(rs *releaseServer) *updater.Client {
	c := updater.New(updater.Config{Owner: "x", Repo: "y"})
	c = updater.New(updater.Config{
		Owner: "x", Repo: "y",
		HTTPClient: &http.Client{
			Transport: rewriteTransport{target: rs.URL},
		},
	})
	return c
}

type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/releases/latest") {
		r.URL.Scheme = "http"
		r.URL.Host = strings.TrimPrefix(rt.target, "http://")
		r.URL.Path = "/latest"
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestUpdateCheck_HasUpdate(t *testing.T) {
	rs := newReleaseServer(t, "v9.9.9", []byte("bin"), false)
	defer rs.Close()

	srv, token := bootstrapAndLogin(t, Config{
		Updater: UpdaterConfig{
			Owner: "x", Repo: "y", Version: "v0.0.1",
			Client: stubUpdaterClient(rs),
		},
	})
	rr := authGet(t, srv, "/api/v1/update/check", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("code: %d %s", rr.Code, rr.Body.String())
	}
	var out updateCheckResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if !out.HasUpdate || out.Latest != "v9.9.9" {
		t.Fatalf("bad response: %+v", out)
	}
	if out.Checksum == "" {
		t.Fatalf("expected checksum from body, got empty")
	}
}

func TestUpdateCheck_DisabledWhenNoClient(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := authGet(t, srv, "/api/v1/update/check", token)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateApply_SwapsBinary(t *testing.T) {
	payload := []byte("new-binary-bytes-v9")
	rs := newReleaseServer(t, "v9.9.9", payload, false)
	defer rs.Close()

	dir := t.TempDir()
	binPath := filepath.Join(dir, "5gpn")
	if err := os.WriteFile(binPath, []byte("old-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	srv, token := bootstrapAndLogin(t, Config{
		Updater: UpdaterConfig{
			Owner: "x", Repo: "y", Version: "v0.0.1", BinaryPath: binPath,
			Client: stubUpdaterClient(rs),
		},
	})
	rr := authPost(t, srv, "/api/v1/update/apply", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("code: %d %s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("binary not swapped: %q", string(got))
	}
	// audit log records ok
	if !auditContains(t, srv, "update.apply", "ok") {
		t.Fatal("expected audit update.apply=ok")
	}
}

func TestUpdateApply_RejectsWhenNoChecksum(t *testing.T) {
	rs := newReleaseServer(t, "v9.9.9", []byte("payload"), false)
	// Strip checksums from body by overriding handler.
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		rel := map[string]any{
			"tag_name": "v9.9.9", "name": "v9.9.9",
			"body": "no hex here",
			"assets": []map[string]any{
				{"name": rs.asset, "size": 7, "browser_download_url": rs.URL + "/dl/" + rs.asset},
			},
		}
		_ = json.NewEncoder(w).Encode(rel)
	})
	rs.Server.Config.Handler = mux
	defer rs.Close()

	dir := t.TempDir()
	binPath := filepath.Join(dir, "5gpn")
	_ = os.WriteFile(binPath, []byte("old"), 0o755)

	srv, token := bootstrapAndLogin(t, Config{
		Updater: UpdaterConfig{
			Owner: "x", Repo: "y", Version: "v0.0.1", BinaryPath: binPath,
			Client: stubUpdaterClient(rs),
		},
	})
	rr := authPost(t, srv, "/api/v1/update/apply", token, nil)
	if rr.Code != http.StatusFailedDependency {
		t.Fatalf("expected 424, got %d %s", rr.Code, rr.Body.String())
	}
}

func auditContains(t *testing.T, srv *Server, action, result string) bool {
	t.Helper()
	rows, err := srv.DB.Query(`SELECT action, result FROM audit_log WHERE action = ? AND result = ?`, action, result)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	return rows.Next()
}
