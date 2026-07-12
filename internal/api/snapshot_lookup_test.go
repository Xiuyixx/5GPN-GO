package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

func TestRollbackSnapshotOlderThanRecent500Versions(t *testing.T) {
	srv, token := s2Setup(t)
	tx, err := srv.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	const rulesYAML = `rules:
  - id: fallback
    kind: MATCH
    pattern: ""
    action: direct
    priority: 100
    enabled: true
`
	var targetSnapshotID, targetRuleID int64
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 501; i++ {
		created := base.Add(time.Duration(i) * time.Second)
		res, err := tx.Exec(
			`INSERT INTO snapshots(created_at, config_hash, tarball_path) VALUES(?, ?, ?)`,
			created, fmt.Sprintf("rollback-history-%03d", i), fmt.Sprintf("snapshot-%03d.tar", i),
		)
		if err != nil {
			t.Fatalf("insert snapshot %d: %v", i, err)
		}
		snapshotID, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		active := 0
		if i == 500 {
			active = 1
		}
		res, err = tx.Exec(
			`INSERT INTO rule_versions(snapshot_id, rules_yaml, created_at, active) VALUES(?, ?, ?, ?)`,
			snapshotID, rulesYAML, created, active,
		)
		if err != nil {
			t.Fatalf("insert rule version %d: %v", i, err)
		}
		ruleID, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			targetSnapshotID, targetRuleID = snapshotID, ruleID
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	recent, err := db.ListRuleVersions(srv.DB, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range recent {
		if version.ID == targetRuleID {
			t.Fatal("test target unexpectedly remained inside the recent-500 window")
		}
	}

	rr := authPost(t, srv,
		fmt.Sprintf("/api/v1/snapshots/%d/rollback", targetSnapshotID), token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("rollback old snapshot: got %d: %s", rr.Code, rr.Body.String())
	}
	response := decode[map[string]any](t, rr)
	if got := int64(response["rule_version_id"].(float64)); got != targetRuleID {
		t.Fatalf("rolled back rule_version_id = %d, want %d", got, targetRuleID)
	}
	active, err := db.GetActiveRuleVersion(srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != targetRuleID {
		t.Fatalf("active rule version = %d, want old target %d", active.ID, targetRuleID)
	}
}
