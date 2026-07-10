package core_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/core"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

// bootStoreForTest mirrors the concrete bootStore in cmd/5gpn/main.go. The
// production type is unexported so we recreate the smallest possible copy —
// if main.go's shape drifts, this test will still exercise the same
// db.GetActiveRuleVersion → core.Store → core.Assemble contract that the
// daemon runs at boot.
type bootStoreForTest struct {
	db    *sql.DB
	exits []config.ExitConfig
}

func (b *bootStoreForTest) ActiveRulesYAML() (string, bool, error) {
	row, err := db.GetActiveRuleVersion(b.db)
	if errors.Is(err, db.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.RulesYAML, true, nil
}

func (b *bootStoreForTest) ListExits() ([]core.ExitRecord, error) {
	out := make([]core.ExitRecord, 0, len(b.exits))
	for _, ex := range b.exits {
		m := map[string]any{}
		for k, v := range ex.Config {
			m[k] = v
		}
		out = append(out, core.ExitRecord{ID: ex.ID, Protocol: ex.Protocol, Config: m})
	}
	return out, nil
}

func openMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	handle, err := db.Open(db.Config{Path: filepath.Join(t.TempDir(), "boot.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return handle
}

// TestBootSmokeDaemonAssembleFromSQLite exercises the full boot pipeline the
// daemon runs when it starts: open + migrate the on-disk SQLite, seed an
// active rule_version, wrap the DB in the same bootStore adapter main.go
// uses, and call core.Assemble. The effective config must carry those
// seeded rules — otherwise a systemd restart would silently drop the user's
// last Apply (Risk R1, AC5).
func TestBootSmokeDaemonAssembleFromSQLite(t *testing.T) {
	handle := openMigratedDB(t)

	seedYAML := "" +
		"rules:\n" +
		"  - id: cn-suffix\n" +
		"    kind: DOMAIN-SUFFIX\n" +
		"    pattern: cn\n" +
		"    action: direct\n" +
		"    priority: 10\n" +
		"    enabled: true\n" +
		"  - id: fallback\n" +
		"    kind: MATCH\n" +
		"    action: wg1\n" +
		"    priority: 100\n" +
		"    enabled: true\n"

	// InsertSnapshot first so the rule_version FK lands cleanly.
	snapID, err := db.InsertSnapshot(handle, db.Snapshot{ConfigHash: "boot-smoke", Note: "boot-smoke"})
	if err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}
	if _, err := db.InsertRuleVersion(handle, snapID, seedYAML, true); err != nil {
		t.Fatalf("InsertRuleVersion: %v", err)
	}

	base := &config.Config{
		Exits: []config.ExitConfig{{ID: "wg1", Protocol: "wireguard"}},
	}
	store := &bootStoreForTest{db: handle, exits: base.Exits}

	effective, err := core.Assemble(base, store)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(effective.EffectiveRules) != 2 {
		t.Fatalf("expected 2 EffectiveRules after boot Assemble, got %d", len(effective.EffectiveRules))
	}
	if effective.EffectiveRules[0].ID != "cn-suffix" || effective.EffectiveRules[1].ID != "fallback" {
		t.Fatalf("boot Assemble lost rule ordering/identity: %+v", effective.EffectiveRules)
	}
	if effective.EffectiveRules[1].Action != "wg1" {
		t.Fatalf("MATCH action not preserved: %+v", effective.EffectiveRules[1])
	}

	// Base must NOT have been mutated by Assemble (Principle 2).
	if base.EffectiveRules != nil {
		t.Fatalf("Assemble mutated base.EffectiveRules: %+v", base.EffectiveRules)
	}
}

// TestBootSmokeRestartPreservesRules is the AC5 assertion: after a simulated
// "restart" (second Assemble call against the same persistent DB from a
// fresh base), the effective config still carries the last-committed rules.
func TestBootSmokeRestartPreservesRules(t *testing.T) {
	handle := openMigratedDB(t)

	seedYAML := "" +
		"rules:\n" +
		"  - id: only\n" +
		"    kind: DOMAIN\n" +
		"    pattern: example.com\n" +
		"    action: direct\n" +
		"    priority: 1\n" +
		"    enabled: true\n"
	snapID, err := db.InsertSnapshot(handle, db.Snapshot{ConfigHash: "restart-smoke", Note: "restart"})
	if err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}
	if _, err := db.InsertRuleVersion(handle, snapID, seedYAML, true); err != nil {
		t.Fatalf("InsertRuleVersion: %v", err)
	}

	// Boot #1: fresh base, DB-backed store.
	base1 := &config.Config{}
	store := &bootStoreForTest{db: handle}
	first, err := core.Assemble(base1, store)
	if err != nil {
		t.Fatalf("boot1 Assemble: %v", err)
	}

	// Simulate restart: fresh base, same store (same on-disk DB).
	base2 := &config.Config{}
	second, err := core.Assemble(base2, store)
	if err != nil {
		t.Fatalf("boot2 Assemble: %v", err)
	}

	if len(second.EffectiveRules) != 1 || second.EffectiveRules[0].ID != "only" {
		t.Fatalf("restart lost rules: %+v", second.EffectiveRules)
	}
	if first.EffectiveRules[0] != second.EffectiveRules[0] {
		t.Fatalf("restart parity violated: %+v vs %+v", first.EffectiveRules[0], second.EffectiveRules[0])
	}
}

// TestBootSmokeEmptyDBFallsBackCleanly proves the fresh-install path: no
// rule_versions, no exits — Assemble must return a nil EffectiveRules
// without erroring, so the daemon can boot and let the user run their
// first Apply.
func TestBootSmokeEmptyDBFallsBackCleanly(t *testing.T) {
	handle := openMigratedDB(t)
	store := &bootStoreForTest{db: handle}
	effective, err := core.Assemble(&config.Config{}, store)
	if err != nil {
		t.Fatalf("empty-DB Assemble: %v", err)
	}
	if effective.EffectiveRules != nil {
		t.Fatalf("fresh install must leave EffectiveRules nil, got %+v", effective.EffectiveRules)
	}
}
