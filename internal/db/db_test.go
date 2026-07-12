package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestOpenForcesPrivateDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if _, err := handle.Exec(`CREATE TABLE permission_probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", filepath.Base(candidate), got)
		}
	}
}

func TestOpenCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := handle.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestReferenceSchemaExecutes(t *testing.T) {
	raw, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := Open(Config{Path: filepath.Join(t.TempDir(), "reference.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if _, err := handle.Exec(string(raw)); err != nil {
		t.Fatalf("execute schema.sql: %v", err)
	}
	var table string
	if err := handle.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type = 'index' AND name = 'idx_panel_sessions_active'`).Scan(&table); err != nil {
		t.Fatalf("panel session index missing: %v", err)
	}
	if table != "panel_sessions" {
		t.Fatalf("panel session index table = %q, want panel_sessions", table)
	}
}

func TestMigrateAppliesAllTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	want := []string{
		"audit_log",
		"bot_sessions",
		"metrics_snapshot",
		"panel_sessions",
		"panel_settings",
		"panel_users",
		"rule_sources",
		"rule_test_fixtures",
		"rule_versions",
		"rulesets",
		"snapshots",
	}
	rows, err := handle.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'goose_%'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
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
	defer func() { _ = handle.Close() }()
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
	defer func() { _ = handle.Close() }()
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

func TestSetActiveRuleVersionMissingPreservesCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatal(err)
	}
	snapID, err := InsertSnapshot(handle, Snapshot{ConfigHash: "active-preserved"})
	if err != nil {
		t.Fatal(err)
	}
	ruleID, err := InsertRuleVersion(handle, snapID, "rules: []", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetActiveRuleVersion(handle, ruleID+999); !errors.Is(err, ErrNoRows) {
		t.Fatalf("missing version error = %v, want ErrNoRows", err)
	}
	active, err := GetActiveRuleVersion(handle)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != ruleID {
		t.Fatalf("active version changed to %d, want %d", active.ID, ruleID)
	}
}

func TestGetRuleVersionBySnapshotBeyondRecentHistoryWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatal(err)
	}

	var targetSnapshotID, targetRuleID int64
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 501; i++ {
		snapshotID, err := InsertSnapshot(handle, Snapshot{
			ConfigHash:  fmt.Sprintf("history-%03d", i),
			TarballPath: fmt.Sprintf("snapshot-%03d.tar", i),
		})
		if err != nil {
			t.Fatalf("InsertSnapshot(%d): %v", i, err)
		}
		ruleID, err := InsertRuleVersion(handle, snapshotID, fmt.Sprintf("rules: []\n# version %d", i), false)
		if err != nil {
			t.Fatalf("InsertRuleVersion(%d): %v", i, err)
		}
		if _, err := handle.Exec(`UPDATE rule_versions SET created_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Second), ruleID); err != nil {
			t.Fatalf("set created_at(%d): %v", i, err)
		}
		if i == 0 {
			targetSnapshotID, targetRuleID = snapshotID, ruleID
		}
	}

	recent, err := ListRuleVersions(handle, 500)
	if err != nil {
		t.Fatalf("ListRuleVersions: %v", err)
	}
	for _, version := range recent {
		if version.ID == targetRuleID {
			t.Fatal("test target unexpectedly remained inside the recent-500 window")
		}
	}

	got, err := GetRuleVersionBySnapshot(handle, targetSnapshotID)
	if err != nil {
		t.Fatalf("GetRuleVersionBySnapshot: %v", err)
	}
	if got.ID != targetRuleID || got.SnapshotID != targetSnapshotID {
		t.Fatalf("direct lookup = %+v, want rule=%d snapshot=%d", got, targetRuleID, targetSnapshotID)
	}
}

func TestInsertSnapshotReusesIdenticalHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatal(err)
	}
	first, err := InsertSnapshot(handle, Snapshot{ConfigHash: "same-hash", Note: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := InsertSnapshot(handle, Snapshot{ConfigHash: "same-hash", Note: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("duplicate hash returned snapshot %d, want %d", second, first)
	}
	got, err := GetSnapshotByID(handle, first)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "first" {
		t.Fatalf("idempotent retry overwrote original snapshot metadata: %+v", got)
	}
}

func TestApplyStatusAcceptsUnrecoveredFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed-status.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatal(err)
	}
	snapshotID, err := InsertSnapshot(handle, Snapshot{ConfigHash: "failed-status"})
	if err != nil {
		t.Fatal(err)
	}
	statusID, err := InsertApplyStatus(handle, snapshotID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateApplyStatus(handle, statusID, "failed", "restore failed"); err != nil {
		t.Fatal(err)
	}
	status, err := LatestApplyStatus(handle)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status.State != "failed" || status.Reason != "restore failed" {
		t.Fatalf("failed status not persisted: %+v", status)
	}
}

func TestUpdateApplyStatusRejectsMissingRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-status.db")
	handle, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if err := Migrate(handle); err != nil {
		t.Fatal(err)
	}
	if err := UpdateApplyStatus(handle, 999999, "failed", "missing"); err == nil {
		t.Fatal("UpdateApplyStatus missing row returned nil error")
	}
}
