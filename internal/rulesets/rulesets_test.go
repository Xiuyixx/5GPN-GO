package rulesets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xdb "github.com/Xiuyixx/5GPN-Go/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	handle, err := xdb.Open(xdb.Config{Path: filepath.Join(t.TempDir(), "rulesets.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := xdb.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return New(handle)
}

func addCachedRuleset(t *testing.T, store *Store, name, content string) {
	t.Helper()
	ctx := context.Background()
	if err := store.Upsert(ctx, Ruleset{
		Name: name, SourceURL: "https://example.test/" + name,
		Kind: KindClash, Action: "proxy", Priority: 10, Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.UpdateContent(ctx, name, []byte(content), 0, "", ""); err != nil {
		t.Fatalf("UpdateContent: %v", err)
	}
}

func TestExpandRejectsMalformedCachedRule(t *testing.T) {
	store := newTestStore(t)
	addCachedRuleset(t, store, "malformed", "DOMAIN,,Proxy")

	_, err := store.Expand(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid rule") {
		t.Fatalf("Expand error = %v, want invalid-rule error", err)
	}
}

func TestExpandRejectsNonEmptyCacheWithZeroValidRules(t *testing.T) {
	store := newTestStore(t)
	addCachedRuleset(t, store, "unsupported", "PROCESS-NAME,browser,Proxy")

	_, err := store.Expand(context.Background())
	if err == nil || !strings.Contains(err.Error(), "produced no valid rules") {
		t.Fatalf("Expand error = %v, want zero-valid-rules error", err)
	}
}

func TestRecordErrorPreservesLastSuccessfulSyncTime(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	addCachedRuleset(t, store, "timing", "DOMAIN,example.test,Proxy")

	const successfulUnix = int64(1_700_000_000)
	if _, err := store.db.ExecContext(ctx,
		`UPDATE rulesets SET last_synced_at = ? WHERE name = ?`, successfulUnix, "timing"); err != nil {
		t.Fatalf("seed last_synced_at: %v", err)
	}
	if err := store.RecordError(ctx, "timing", "network timeout"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	got, err := store.Get(ctx, "timing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantTime := time.Unix(successfulUnix, 0)
	if got.LastSyncedAt == nil || !got.LastSyncedAt.Equal(wantTime) {
		t.Fatalf("LastSyncedAt = %v, want preserved %v", got.LastSyncedAt, wantTime)
	}
	if got.LastError != "network timeout" {
		t.Fatalf("LastError = %q, want network timeout", got.LastError)
	}
}
