package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindAsset(t *testing.T) {
	r := &Release{Assets: []Asset{
		{Name: "5gpn-v0.2.0-linux-amd64.tar.gz"},
		{Name: "5gpn-v0.2.0-linux-arm64.tar.gz"},
		{Name: "5gpn-v0.2.0-darwin-arm64.tar.gz"},
	}}
	a, err := FindAsset(r, "linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Name, "amd64") {
		t.Fatalf("wrong asset: %s", a.Name)
	}
	if _, err := FindAsset(r, "windows-nvidia"); err == nil {
		t.Fatal("expected error for non-existent asset")
	}
}

func TestExtractSha256(t *testing.T) {
	body := `
Release notes.

Artifacts (sha256):
5gpn-v0.2.0-linux-amd64.tar.gz  abc123def4567890abc123def4567890abc123def4567890abc123def4567890
5gpn-v0.2.0-linux-arm64.tar.gz  1234abcd5678ef901234abcd5678ef901234abcd5678ef901234abcd5678ef90
`
	h, err := ExtractSha256(body, "linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if h != "abc123def4567890abc123def4567890abc123def4567890abc123def4567890" {
		t.Fatalf("wrong sha: %s", h)
	}
	if _, err := ExtractSha256("no hex anywhere", "x"); err == nil {
		t.Fatal("expected error when no hex present")
	}
}

func TestDownloadVerifiesSha(t *testing.T) {
	payload := []byte("hello 5gpn binary")
	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client := New(Config{HTTPClient: srv.Client()})
	dir := t.TempDir()
	dest := filepath.Join(dir, "5gpn.new")
	n, err := client.Download(context.Background(), &Asset{Name: "5gpn", DownloadURL: srv.URL}, dest, sumHex)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != len(payload) {
		t.Fatalf("wrote %d bytes, want %d", n, len(payload))
	}
	body, _ := os.ReadFile(dest)
	if string(body) != string(payload) {
		t.Fatalf("file body mismatch")
	}
}

func TestDownloadRejectsBadSha(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("bytes"))
	}))
	defer srv.Close()
	client := New(Config{HTTPClient: srv.Client()})
	dir := t.TempDir()
	dest := filepath.Join(dir, "5gpn.new")
	_, err := client.Download(context.Background(), &Asset{DownloadURL: srv.URL}, dest, "deadbeef")
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch, got %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest file should have been removed: %v", err)
	}
}

func TestSwapKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "5gpn")
	newBin := filepath.Join(dir, "5gpn.new")
	if err := os.WriteFile(cur, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Swap(context.Background(), SwapOptions{
		CurrentPath: cur, NewPath: newBin,
		HealthCheck: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(cur)
	if string(body) != "NEW" {
		t.Fatalf("current not replaced: %s", body)
	}
	backup, _ := os.ReadFile(cur + ".prev")
	if string(backup) != "OLD" {
		t.Fatalf("backup missing/wrong: %s", backup)
	}
}

func TestSwapRollsBackOnHealthFailure(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "5gpn")
	newBin := filepath.Join(dir, "5gpn.new")
	_ = os.WriteFile(cur, []byte("OLD"), 0o755)
	_ = os.WriteFile(newBin, []byte("NEW"), 0o755)
	err := Swap(context.Background(), SwapOptions{
		CurrentPath: cur, NewPath: newBin,
		HealthCheck: func(context.Context) error { return errUnhealthy },
	})
	if err == nil {
		t.Fatal("expected swap to surface health error")
	}
	body, _ := os.ReadFile(cur)
	if string(body) != "OLD" {
		t.Fatalf("expected rollback to restore OLD, got %s", body)
	}
}

var errUnhealthy = &jsonErr{"unhealthy"}

type jsonErr struct{ msg string }

func (e *jsonErr) Error() string { return e.msg }

// Ensures Release JSON round-trips without dropping our subset of fields.
func TestReleaseJSONRoundTrip(t *testing.T) {
	body := `{"tag_name":"v1.0.0","name":"1.0.0","draft":false,"assets":[{"name":"5gpn","size":123,"browser_download_url":"http://x/y"}],"body":"notes"}`
	var r Release
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.TagName != "v1.0.0" || len(r.Assets) != 1 {
		t.Fatalf("bad decode: %+v", r)
	}
}
