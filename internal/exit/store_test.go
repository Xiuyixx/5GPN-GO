package exit_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/exit"
)

// A working share URI accepted by xexit.Parse (trojan is one of the simplest).
const validURI = "trojan://pw@example.com:443?sni=example.com#node"

func newTestStore(t *testing.T) (*exit.SQLStore, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "exit_store.db")
	handle, err := db.Open(db.Config{Path: path})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return exit.NewStore(handle), handle
}

func countActive(t *testing.T, handle *sql.DB) int {
	t.Helper()
	var n int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM exits WHERE active = 1`).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

func TestSingleActiveInvariant(t *testing.T) {
	store, handle := newTestStore(t)
	ctx := context.Background()

	// Seeded: direct is active. Adding A/B/C leaves direct active.
	for _, id := range []string{"a", "b", "c"} {
		if _, err := store.Add(ctx, id, validURI); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	if got := countActive(t, handle); got != 1 {
		t.Fatalf("after adds: want 1 active, got %d", got)
	}

	// Switch across A → B → C. Exactly one active at every step.
	for _, id := range []string{"a", "b", "c", "a", "c", "b"} {
		if err := store.Switch(ctx, id); err != nil {
			t.Fatalf("Switch(%s): %v", id, err)
		}
		if got := countActive(t, handle); got != 1 {
			t.Fatalf("after Switch(%s): want 1 active, got %d", id, got)
		}
		act, err := store.Active(ctx)
		if err != nil {
			t.Fatalf("Active after Switch(%s): %v", id, err)
		}
		if act.ExitID != id {
			t.Fatalf("Active = %q, want %q", act.ExitID, id)
		}
	}
}

func TestDeleteActiveRejected(t *testing.T) {
	store, handle := newTestStore(t)
	ctx := context.Background()

	err := store.Delete(ctx, "direct")
	if !errors.Is(err, exit.ErrExitActive) {
		t.Fatalf("Delete(direct) err = %v, want ErrExitActive", err)
	}

	// Row still present.
	var n int
	if err := handle.QueryRow(`SELECT COUNT(*) FROM exits WHERE exit_id='direct'`).Scan(&n); err != nil {
		t.Fatalf("count direct: %v", err)
	}
	if n != 1 {
		t.Fatalf("direct row missing after failed delete: %d", n)
	}

	// Switch away, then delete succeeds.
	if _, err := store.Add(ctx, "wg1", validURI); err != nil {
		t.Fatalf("Add wg1: %v", err)
	}
	if err := store.Switch(ctx, "wg1"); err != nil {
		t.Fatalf("Switch wg1: %v", err)
	}
	if err := store.Delete(ctx, "direct"); err != nil {
		t.Fatalf("Delete(direct) after switch: %v", err)
	}
}

func TestAddValidatesURI(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := store.Add(ctx, "bogus", "not-a-uri://nope"); err == nil {
		t.Fatal("Add with bogus uri: want error, got nil")
	}

	e, err := store.Add(ctx, "wg1", validURI)
	if err != nil {
		t.Fatalf("Add valid: %v", err)
	}
	if e.ExitID != "wg1" || e.Active {
		t.Fatalf("added exit = %+v, want id=wg1 active=false", e)
	}
	if e.ProxyConfig == nil {
		t.Fatal("valid uri should populate ProxyConfig")
	}
	if got, _ := e.ProxyConfig["type"].(string); got != "trojan" {
		t.Fatalf("ProxyConfig type = %v, want trojan", got)
	}
}

func TestAddDirectSentinelBypassesParse(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// exit_id 'direct' already seeded — use a different id but direct:// uri.
	// Bypass must succeed even though xexit.Parse rejects direct://.
	e, err := store.Add(ctx, "direct2", "direct://")
	if err != nil {
		t.Fatalf("Add(direct2, direct://): %v", err)
	}
	if e.Protocol != "direct" {
		t.Fatalf("protocol = %q, want 'direct'", e.Protocol)
	}
	if e.ProxyConfig != nil {
		t.Fatalf("ProxyConfig should be nil for direct sentinel, got %v", e.ProxyConfig)
	}

	// Confirm xexit.Parse would have failed on direct:// — sanity check.
	if _, perr := exit.Parse("direct://"); perr == nil {
		t.Fatal("xexit.Parse(direct://) should fail; sentinel bypass is required")
	}
}

func TestActiveFirstOrdering(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// Add wg1, wg2, wg3 in order. Direct is seeded active.
	for _, id := range []string{"wg1", "wg2", "wg3"} {
		if _, err := store.Add(ctx, id, validURI); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	// Switch the middle one to active.
	if err := store.Switch(ctx, "wg2"); err != nil {
		t.Fatalf("Switch wg2: %v", err)
	}

	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("List len = %d, want 4", len(list))
	}
	if list[0].ExitID != "wg2" || !list[0].Active {
		t.Fatalf("List[0] = %+v, want id=wg2 active=true", list[0])
	}

	recs, err := store.Records(ctx)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("Records len = %d, want 4", len(recs))
	}
	if recs[0].ID != "wg2" {
		t.Fatalf("Records[0].ID = %q, want wg2", recs[0].ID)
	}
}

func TestExitIDValidation(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	bad := []string{"bad id", "has/slash", "has.dot", "", "bad$char"}
	for _, id := range bad {
		if _, err := store.Add(ctx, id, validURI); !errors.Is(err, exit.ErrInvalidExitID) {
			t.Fatalf("Add(%q) err = %v, want ErrInvalidExitID", id, err)
		}
	}

	good := []string{"wg-1", "wg_1", "WG1", "abc123"}
	for _, id := range good {
		if _, err := store.Add(ctx, id, validURI); err != nil {
			t.Fatalf("Add(%q) unexpected err: %v", id, err)
		}
	}
}

func TestDeleteMissingReturnsNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	err := store.Delete(ctx, "ghost")
	if !errors.Is(err, exit.ErrExitNotFound) {
		t.Fatalf("Delete(ghost) err = %v, want ErrExitNotFound", err)
	}
}

func TestSwitchMissingReturnsNotFound(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	err := store.Switch(ctx, "ghost")
	if !errors.Is(err, exit.ErrExitNotFound) {
		t.Fatalf("Switch(ghost) err = %v, want ErrExitNotFound", err)
	}
}

func TestActiveOnSeededDB(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	act, err := store.Active(ctx)
	if err != nil {
		t.Fatalf("Active on seeded DB: %v", err)
	}
	if act.ExitID != "direct" || !act.Active {
		t.Fatalf("seeded Active = %+v, want direct active=true", act)
	}
}
