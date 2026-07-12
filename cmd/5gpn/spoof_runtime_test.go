package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

func TestApplySpoofSettingsTrimsScope(t *testing.T) {
	handle, err := db.Open(db.Config{Path: filepath.Join(t.TempDir(), "spoof.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatal(err)
	}
	sset := settings.New(handle)
	if err := sset.SetMany(context.Background(), map[string]any{
		settings.KeyFrontdoorSpoofEnabled:  true,
		settings.KeyFrontdoorSpoofServerIP: "203.0.113.10",
		settings.KeyFrontdoorSpoofScope:    "  PRIVATE_ONLY  ",
	}, "test"); err != nil {
		t.Fatal(err)
	}

	res := resolver.NewResolver(&resolver.Store{}, resolver.NewUpstream(), nil)
	t.Cleanup(res.Upstream.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applySpoofSettings(context.Background(), res, sset, "panel.example", logger); err != nil {
		t.Fatal(err)
	}
	policy := res.Spoof.Load()
	if policy == nil || policy.Scope != resolver.SpoofScopePrivateOnly {
		t.Fatalf("policy=%+v", policy)
	}
}
