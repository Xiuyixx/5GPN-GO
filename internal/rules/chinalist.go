package rules

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/netguard"
)

// Sync downloads the chinalist from url and atomically writes it to path.
// It reuses the ETag stored in the rule_sources table to skip the download
// when the server reports 304 Not Modified.
//
// The store parameter may be nil — in that case ETag caching is skipped and
// every call performs a full download.
func Sync(ctx context.Context, store *sql.DB, url, path string) error {
	return syncWithClient(ctx, store, url, path, netguard.NewHTTPClient(30*time.Second))
}

func syncWithClient(ctx context.Context, store *sql.DB, url, path string, client *http.Client) error {
	if _, err := netguard.ValidateHTTPURL(url); err != nil {
		return fmt.Errorf("chinalist: %w", err)
	}
	var prevETag string
	if store != nil {
		var err error
		prevETag, err = db.GetRuleSourceETag(store, url)
		if err != nil {
			// non-fatal: proceed without ETag
			prevETag = ""
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("chinalist: build request: %w", err)
	}
	if prevETag != "" {
		req.Header.Set("If-None-Match", prevETag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("chinalist: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chinalist: unexpected status %d from %s", resp.StatusCode, url)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("chinalist: mkdir %s: %w", filepath.Dir(path), err)
	}

	// Atomic write: write to a temp file in the same directory, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".chinalist-*.tmp")
	if err != nil {
		return fmt.Errorf("chinalist: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // no-op if rename succeeded
	}()

	const maxChinaListBytes = 50 << 20 // 50MB cap
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxChinaListBytes+1))
	if err != nil {
		return fmt.Errorf("chinalist: write temp: %w", err)
	}
	if n > maxChinaListBytes {
		return fmt.Errorf("chinalist: response exceeds %d byte cap", maxChinaListBytes)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("chinalist: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("chinalist: rename to %s: %w", path, err)
	}

	// Persist ETag for next call.
	newETag := resp.Header.Get("ETag")
	if store != nil && newETag != "" {
		_ = db.UpsertRuleSourceETag(store, url, "chinalist", newETag)
	}
	return nil
}
