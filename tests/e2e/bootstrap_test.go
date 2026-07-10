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

// TestBootstrapAndLogin walks the M1 golden path: bootstrap the panel
// user with the setup token, login, and hit a protected endpoint. This
// is the "does anything work at all end-to-end" smoke.
func TestBootstrapAndLogin(t *testing.T) {
	d := startDaemon(t)
	defer d.Stop()

	if d.setupToken == "" {
		t.Fatal("daemon did not print a setup token; is bootstrap wiring intact?")
	}

	// bootstrap
	body, _ := json.Marshal(map[string]string{
		"token":    d.setupToken,
		"username": "admin",
		"password": "correcthorse",
	})
	resp := postJSON(t, d.URL("/api/v1/bootstrap"), body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d (%s)", resp.StatusCode, resp.body)
	}

	// login
	body, _ = json.Marshal(map[string]string{
		"username": "admin",
		"password": "correcthorse",
	})
	resp = postJSON(t, d.URL("/api/v1/login"), body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d (%s)", resp.StatusCode, resp.body)
	}
	var loginRes map[string]string
	if err := json.Unmarshal([]byte(resp.body), &loginRes); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	token := loginRes["token"]
	if token == "" {
		t.Fatalf("no token in login response: %s", resp.body)
	}

	// protected endpoint: /api/v1/rules should return 200 + JSON with a "rules" key
	req, _ := http.NewRequest("GET", d.URL("/api/v1/rules"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rawResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rawBody, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusOK {
		t.Fatalf("rules GET: %d (%s)", rawResp.StatusCode, rawBody)
	}
	if !strings.Contains(string(rawBody), `"rules"`) {
		t.Errorf("rules response missing 'rules' key: %s", rawBody)
	}

	// Missing token → 401 (proves auth middleware is wired).
	rawResp, err = http.Get(d.URL("/api/v1/rules"))
	if err != nil {
		t.Fatal(err)
	}
	rawResp.Body.Close()
	if rawResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauth GET should be 401, got %d", rawResp.StatusCode)
	}
}

type simpleResp struct {
	StatusCode int
	body       string
}

func postJSON(t *testing.T, url string, body []byte) simpleResp {
	t.Helper()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return simpleResp{StatusCode: resp.StatusCode, body: string(b)}
}
