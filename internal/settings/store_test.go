package settings

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := db.Open(db.Config{Path: path})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return New(handle)
}

func TestGetMissingKey(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), "does.not.exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetThenGetString(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyServerDomain, "panel.example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetString(ctx, KeyServerDomain)
	if err != nil {
		t.Fatal(err)
	}
	if got != "panel.example.com" {
		t.Errorf("got %q, want panel.example.com", got)
	}
}

func TestGetStringMissingReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetString(context.Background(), "server.domain")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
}

func TestSetBool(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetBool(ctx, KeyTLSACMEEnabled, true, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBool(ctx, KeyTLSACMEEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("expected true")
	}
	// Overwrite
	if err := s.SetBool(ctx, KeyTLSACMEEnabled, false, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetBool(ctx, KeyTLSACMEEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Errorf("expected false after overwrite")
	}
}

func TestSetJSONRoundTripSlice(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	want := []int64{61361176, 12345}
	if err := s.SetJSON(ctx, KeyTGBotAdminChats, want, "admin"); err != nil {
		t.Fatal(err)
	}
	var got []int64
	if err := s.GetJSON(ctx, KeyTGBotAdminChats, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSnapshotIncludesAllKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.SetString(ctx, KeyServerDomain, "a.example", "admin")
	_ = s.SetBool(ctx, KeyTLSACMEEnabled, true, "admin")
	_ = s.SetInt(ctx, KeyServerPanelPort, 8443, "admin")

	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 3 {
		t.Fatalf("expected 3 rows, got %d: %v", len(snap), snap)
	}
	for _, k := range []string{KeyServerDomain, KeyTLSACMEEnabled, KeyServerPanelPort} {
		if _, ok := snap[k]; !ok {
			t.Errorf("missing key %q in snapshot", k)
		}
	}
}

func TestDeleteRemovesRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyServerDomain, "a.example", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, KeyServerDomain); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get(ctx, KeyServerDomain)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSetOverwritesExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyServerDomain, "a.example", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetString(ctx, KeyServerDomain, "b.example", "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetString(ctx, KeyServerDomain)
	if err != nil {
		t.Fatal(err)
	}
	if got != "b.example" {
		t.Errorf("got %q, want b.example", got)
	}
}

func TestGetJSONMissingReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	var got []int64
	err := s.GetJSON(context.Background(), KeyTGBotAdminChats, &got)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetManyRollsBackEveryKeyOnWriteFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyServerDomain, "before.example", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_bad_setting
		BEFORE INSERT ON panel_settings WHEN NEW.key = 'reject.me'
		BEGIN SELECT RAISE(ABORT, 'rejected for test'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.SetMany(ctx, map[string]any{
		KeyServerDomain: "after.example",
		"reject.me":     true,
	}, "test")
	if err == nil {
		t.Fatal("SetMany unexpectedly succeeded")
	}
	got, err := s.GetString(ctx, KeyServerDomain)
	if err != nil {
		t.Fatal(err)
	}
	if got != "before.example" {
		t.Fatalf("transaction partially committed: domain=%q", got)
	}
}

func TestSetManyRejectsUnencodableValueBeforeWriting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyServerDomain, "before.example", "test"); err != nil {
		t.Fatal(err)
	}
	err := s.SetMany(ctx, map[string]any{
		KeyServerDomain: "after.example",
		"bad.value":     func() {},
	}, "test")
	if err == nil {
		t.Fatal("SetMany unexpectedly encoded a function")
	}
	got, _ := s.GetString(ctx, KeyServerDomain)
	if got != "before.example" {
		t.Fatalf("encoding failure partially committed: domain=%q", got)
	}
}
