package db

import (
	"path/filepath"
	"sort"
	"testing"
)

func TestOpenCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer handle.Close()
	if err := Migrate(handle); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := handle.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestMigrateAppliesAllTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer handle.Close()
	if err := Migrate(handle); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	want := []string{
		"audit_log",
		"bot_sessions",
		"metrics_snapshot",
		"panel_sessions",
		"panel_users",
		"rule_sources",
		"rule_test_fixtures",
		"rule_versions",
		"snapshots",
	}
	rows, err := handle.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'goose_%'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		seen[name] = true
	}
	sort.Strings(want)
	for _, w := range want {
		if !seen[w] {
			t.Errorf("missing table %q", w)
		}
	}
}

func TestPanelUsersUnique(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer handle.Close()
	if err := Migrate(handle); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := handle.Exec(`INSERT INTO panel_users(username, bcrypt_hash) VALUES('admin','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`INSERT INTO panel_users(username, bcrypt_hash) VALUES('admin','y')`); err == nil {
		t.Fatal("expected UNIQUE constraint violation on duplicate username")
	}
}

func TestRuleVersionsOnlyOneActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer handle.Close()
	if err := Migrate(handle); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := handle.Exec(`INSERT INTO snapshots(config_hash, tarball_path) VALUES('hash1','p1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`INSERT INTO snapshots(config_hash, tarball_path) VALUES('hash2','p2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`INSERT INTO rule_versions(snapshot_id, rules_yaml, active) VALUES(1,'a: 1',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`INSERT INTO rule_versions(snapshot_id, rules_yaml, active) VALUES(2,'b: 2',1)`); err == nil {
		t.Fatal("expected partial-unique index to reject second active=1 row")
	}
}
