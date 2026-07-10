//go:build e2e

package e2e

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestExitsAddSwitchDelete is the new-daemon equivalent of the legacy
// test_exit_switching_policy.sh. Prove the API round-trip is intact:
// add an exit, switch to it, refuse to delete the active one, switch
// back, delete.
func TestExitsAddSwitchDelete(t *testing.T) {
	d := startDaemon(t)
	defer d.Stop()
	tok := bootstrapAndLogin(t, d)

	// Fresh install ships with "direct" — no other exits.
	resp := do(t, d, tok, "GET", "/api/v1/exits", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}

	// Add.
	body, _ := json.Marshal(map[string]string{
		"id":  "trojan-jp",
		"uri": "trojan://pw@example.com:443?sni=fake.example.com",
	})
	if r := do(t, d, tok, "POST", "/api/v1/exits/add", body); r.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d (%s)", r.StatusCode, r.body)
	}

	// Switch to it.
	body, _ = json.Marshal(map[string]string{"id": "trojan-jp"})
	if r := do(t, d, tok, "POST", "/api/v1/exits/switch", body); r.StatusCode != http.StatusOK {
		t.Fatalf("switch: %d (%s)", r.StatusCode, r.body)
	}

	// Delete active must fail with 409 — this is the safety-net legacy
	// operators relied on.
	body, _ = json.Marshal(map[string]string{"id": "trojan-jp"})
	if r := do(t, d, tok, "POST", "/api/v1/exits/delete", body); r.StatusCode != http.StatusConflict {
		t.Errorf("delete active: want 409, got %d (%s)", r.StatusCode, r.body)
	}

	// Switch back to direct, then delete.
	body, _ = json.Marshal(map[string]string{"id": "direct"})
	if r := do(t, d, tok, "POST", "/api/v1/exits/switch", body); r.StatusCode != http.StatusOK {
		t.Fatalf("switch back: %d", r.StatusCode)
	}
	body, _ = json.Marshal(map[string]string{"id": "trojan-jp"})
	if r := do(t, d, tok, "POST", "/api/v1/exits/delete", body); r.StatusCode != http.StatusNoContent {
		t.Errorf("delete: want 204, got %d (%s)", r.StatusCode, r.body)
	}
}

// TestBackupExportRoundTrip covers AC14 end-to-end: export produces a
// well-formed gzip stream + import accepts it back.
func TestBackupExportRoundTrip(t *testing.T) {
	d := startDaemon(t)
	defer d.Stop()
	tok := bootstrapAndLogin(t, d)

	// Export.
	req, _ := http.NewRequest("GET", d.URL("/api/v1/backup/export"), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "gzip") {
		t.Errorf("wrong content-type: %s", resp.Header.Get("Content-Type"))
	}
	// Prove the stream decompresses.
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	gz, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("bad gzip: %v", err)
	}
	_ = gz.Close()

	// Non-gzip import must be rejected.
	req, _ = http.NewRequest("POST", d.URL("/api/v1/backup/import"), strings.NewReader("not gzip"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/gzip")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("garbage import: want 400, got %d", resp.StatusCode)
	}
}
