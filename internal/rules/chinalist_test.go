package rules

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Exec(`CREATE TABLE rule_sources (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		url         TEXT NOT NULL UNIQUE,
		kind        TEXT NOT NULL,
		last_synced DATETIME,
		etag        TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSyncDownloadsFile(t *testing.T) {
	const content = "example.com\ngoogle.com\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "chinalist.txt")

	if err := syncWithClient(context.Background(), nil, srv.URL, path, srv.Client()); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("want %q got %q", content, got)
	}
}

func TestSyncETagNotModified(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"etag-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"etag-v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("example.com\n"))
	}))
	defer srv.Close()

	store := openTestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "chinalist.txt")

	// First call: downloads and stores ETag.
	if err := syncWithClient(context.Background(), store, srv.URL, path, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Verify ETag was persisted.
	etag, err := db.GetRuleSourceETag(store, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if etag != `"etag-v1"` {
		t.Errorf("expected stored etag %q, got %q", `"etag-v1"`, etag)
	}

	// Second call: server returns 304, file unchanged, no write.
	stat1, _ := os.Stat(path)
	if err := syncWithClient(context.Background(), store, srv.URL, path, srv.Client()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 total calls, got %d", calls)
	}
	stat2, _ := os.Stat(path)
	if stat1.ModTime() != stat2.ModTime() {
		t.Error("file should not have been rewritten on 304")
	}
}

func TestSyncAtomicWrite(t *testing.T) {
	// Verifies the temp+rename pattern: file appears complete or not at all.
	const content = "baidu.com\nweibo.com\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "chinalist.txt")

	if err := syncWithClient(context.Background(), nil, srv.URL, path, srv.Client()); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("want %q got %q", content, got)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file not cleaned up: %s", e.Name())
		}
	}
}

func TestSyncBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	err := syncWithClient(context.Background(), nil, srv.URL, filepath.Join(dir, "chinalist.txt"), srv.Client())
	if err == nil {
		t.Error("expected error on 500 response")
	}
}

func TestSyncDefaultClientRejectsPrivateDestination(t *testing.T) {
	err := Sync(context.Background(), nil, "http://169.254.169.254/latest/meta-data", filepath.Join(t.TempDir(), "list"))
	if err == nil {
		t.Fatal("metadata endpoint accepted")
	}
}
