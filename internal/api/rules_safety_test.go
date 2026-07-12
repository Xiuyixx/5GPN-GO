package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
	"github.com/Xiuyixx/5GPN-Go/internal/rulesets"
)

func TestTextImportRulesRemainManualAndReachActiveVersion(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := authPost(t, srv, "/api/v1/rules/import", token, map[string]any{
		"text":   "DOMAIN,imported.example,PROXY\n",
		"action": "direct",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rr.Code, rr.Body.String())
	}
	var imported importResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Rules) != 1 {
		t.Fatalf("imported rules=%d, want 1: %s", len(imported.Rules), rr.Body.String())
	}
	if imported.Rules[0].GroupID != "" || imported.GroupID != "" {
		t.Fatalf("one-shot import was tagged as managed ruleset: %+v", imported)
	}

	rr = authPost(t, srv, "/api/v1/rules/apply", token, map[string]any{
		"rules": imported.Rules,
		"note":  "import regression",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("apply imported rule: %d %s", rr.Code, rr.Body.String())
	}
	active, err := db.GetActiveRuleVersion(srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	set, err := rules.ParseYAML([]byte(active.RulesYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rules) != 1 || set.Rules[0].Pattern != "imported.example" {
		t.Fatalf("active rules lost imported entry: %+v", set.Rules)
	}
}

func TestHistoricalImportedGroupRemainsManual(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	srv.Rulesets = rulesets.New(srv.DB)
	historical := rules.Rule{
		ID: "old-import", Kind: rules.KindDomain, Pattern: "kept.example",
		Action: "direct", Priority: 10, Enabled: true, GroupID: "import-legacy-batch",
	}
	rr := authPost(t, srv, "/api/v1/rules/apply", token, map[string]any{
		"rules": []rules.Rule{historical},
		"note":  "upgrade compatibility",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("apply historical import: %d %s", rr.Code, rr.Body.String())
	}
	active, err := db.GetActiveRuleVersion(srv.DB)
	if err != nil {
		t.Fatal(err)
	}
	set, err := rules.ParseYAML([]byte(active.RulesYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rules) != 1 || set.Rules[0].ID != historical.ID || set.Rules[0].GroupID != historical.GroupID {
		t.Fatalf("historical grouped import was stripped: %+v", set.Rules)
	}
}

func TestRepeatedIdenticalApplyIsIdempotent(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	body := map[string]any{
		"rules": []map[string]any{{
			"id": "same", "kind": "DOMAIN", "pattern": "same.example",
			"action": "direct", "priority": 10, "enabled": true,
		}},
		"note": "same rules",
	}
	var snapshotID float64
	for i := 0; i < 2; i++ {
		rr := authPost(t, srv, "/api/v1/rules/apply", token, body)
		if rr.Code != http.StatusOK {
			t.Fatalf("apply %d: %d %s", i+1, rr.Code, rr.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			snapshotID, _ = response["snapshot_id"].(float64)
		} else if response["snapshot_id"] != snapshotID {
			t.Fatalf("identical apply did not reuse snapshot: first=%v second=%v", snapshotID, response["snapshot_id"])
		}
	}
}

func TestRulesetExpandDatabaseFailureFailsClosed(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	srv.Rulesets = rulesets.New(srv.DB)
	if _, err := srv.DB.Exec(`DROP TABLE rulesets`); err != nil {
		t.Fatal(err)
	}
	rr := authPost(t, srv, "/api/v1/rules/dry-run", token, map[string]any{
		"rules":    []any{},
		"fixtures": []any{},
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("ruleset failure did not fail closed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestSnapshotListReportsDatabaseActiveVersion(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	for i, domain := range []string{"first.example", "second.example"} {
		rr := authPost(t, srv, "/api/v1/rules/apply", token, map[string]any{
			"rules": []map[string]any{{
				"id": "r", "kind": "DOMAIN", "pattern": domain,
				"action": "direct", "priority": 10, "enabled": true,
			}},
			"note": domain,
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("apply %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	rr := authGet(t, srv, "/api/v1/snapshots", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Snapshots []snapshotEntry `json:"snapshots"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	activeCount := 0
	for _, snap := range body.Snapshots {
		if snap.Active {
			activeCount++
			if !strings.Contains(snap.Note, "second.example") {
				t.Fatalf("wrong snapshot marked active: %+v", snap)
			}
		}
	}
	if activeCount != 1 {
		t.Fatalf("active snapshot count=%d, want 1: %+v", activeCount, body.Snapshots)
	}
}

func TestSnapshotListMarksRulelessSnapshotsNonRollbackable(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rulesSnapshotID, err := db.InsertSnapshot(srv.DB, db.Snapshot{
		ConfigHash: "rollbackable-contract-rules",
		Note:       "rules apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertRuleVersion(srv.DB, rulesSnapshotID, "rules: []\n", false); err != nil {
		t.Fatal(err)
	}
	exitSnapshotID, err := db.InsertSnapshot(srv.DB, db.Snapshot{
		ConfigHash: "rollbackable-contract-exit-switch",
		Note:       "exit-switch:direct",
	})
	if err != nil {
		t.Fatal(err)
	}

	rr := authGet(t, srv, "/api/v1/snapshots", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Snapshots []snapshotEntry `json:"snapshots"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rollbackable := make(map[int64]bool, len(body.Snapshots))
	for _, snapshot := range body.Snapshots {
		rollbackable[snapshot.ID] = snapshot.Rollbackable
	}
	if !rollbackable[rulesSnapshotID] {
		t.Fatal("snapshot with a rule_version was not marked rollbackable")
	}
	if rollbackable[exitSnapshotID] {
		t.Fatal("exit-switch snapshot without a rule_version was marked rollbackable")
	}
}

func TestFetchRulesetRejectsMetadataAddress(t *testing.T) {
	if _, err := fetchRuleset(t.Context(), "http://169.254.169.254/latest/meta-data", 0, 1024); err == nil {
		t.Fatal("metadata destination accepted")
	}
}
