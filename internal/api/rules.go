package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

type ruleDoc struct {
	Rules []rules.Rule `json:"rules"`
}

type dryRunRequest struct {
	Rules    []rules.Rule        `json:"rules"`
	Fixtures []rules.TestFixture `json:"fixtures"`
}

type dryRunResponse struct {
	Results []rules.DryRunResult `json:"results"`
	Passed  int                  `json:"passed"`
	Failed  int                  `json:"failed"`
}

type applyRequest struct {
	Rules []rules.Rule `json:"rules"`
	Note  string       `json:"note"`
}

type applyResponse struct {
	SnapshotID    int64  `json:"snapshot_id"`
	RuleVersionID int64  `json:"rule_version_id"`
	RolledBack    bool   `json:"rolled_back"`
	Health        string `json:"health"`
	Reason        string `json:"reason,omitempty"`
}

// stripRulesetExpansions drops rules that carry a GroupID from the incoming
// draft. Any rule with GroupID != "" was materialized by a previous apply's
// call to Rulesets.Expand() and persisted into rule_versions.rules_yaml,
// which handleListRules serves back to the panel verbatim. Re-appending
// them here without stripping would collide with the fresh Expand() call
// below (ruleset expansion produces deterministic IDs like "<name>-<idx>"),
// tripping RuleSet.Validate's "duplicate id" check and blocking every
// dry-run/apply after the first successful apply with rulesets registered.
func stripRulesetExpansions(in []rules.Rule) []rules.Rule {
	out := in[:0]
	for _, r := range in {
		if r.GroupID != "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	active, err := db.GetActiveRuleVersion(s.DB)
	if err == db.ErrNoRows {
		writeJSON(w, http.StatusOK, ruleDoc{Rules: []rules.Rule{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	set, err := rules.ParseYAML([]byte(active.RulesYAML))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "parse_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ruleDoc{Rules: set.Rules})
}

func (s *Server) handleDryRun(w http.ResponseWriter, r *http.Request) {
	var req dryRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// Merge registered rulesets (native rule-provider engine) with the
	// caller's manually-authored rules so dry-run matches whatever the
	// effective config will look like post-apply. Rulesets contribute
	// their cached content; ungrouped rules keep their existing shape.
	req.Rules = stripRulesetExpansions(req.Rules)
	if s.Rulesets != nil {
		if extra, err := s.Rulesets.Expand(r.Context()); err == nil && len(extra) > 0 {
			req.Rules = append(req.Rules, extra...)
		}
	}
	set := &rules.RuleSet{Rules: req.Rules}
	if err := set.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}
	activeExit := ""
	if s.BaseConfig != nil && len(s.BaseConfig.Exits) > 0 {
		activeExit = s.BaseConfig.Exits[0].ID
	}
	fallthroughTarget, _ := rules.ResolveFallthrough(set, activeExit, "PROXY")
	res := rules.DryRun(set, req.Fixtures, fallthroughTarget)
	passed := 0
	for _, r := range res {
		if r.Pass {
			passed++
		}
	}
	writeJSON(w, http.StatusOK, dryRunResponse{Results: res, Passed: passed, Failed: len(res) - passed})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// Expand registered rulesets into the effective set. The rules_yaml
	// we snapshot below is the FULL set (manual + expanded ruleset
	// entries) so the mihomo config always matches what the operator
	// saw in dry-run. GroupID tags survive so the panel can still
	// render the two-section split (Rules + Rulesets) on next load.
	req.Rules = stripRulesetExpansions(req.Rules)
	if s.Rulesets != nil {
		if extra, err := s.Rulesets.Expand(r.Context()); err == nil && len(extra) > 0 {
			req.Rules = append(req.Rules, extra...)
		}
	}
	set := &rules.RuleSet{Rules: req.Rules}
	if err := set.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	yamlBytes, err := rules.MarshalYAML(set)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal_error", err.Error())
		return
	}

	snapID, err := db.InsertSnapshot(s.DB, db.Snapshot{
		ConfigHash:  rules.HashRules(set),
		TarballPath: "", // M1 keeps snapshots in-DB; M2 adds tarball export
		Note:        req.Note,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot_error", err.Error())
		return
	}

	rvID, err := db.InsertRuleVersion(s.DB, snapID, string(yamlBytes), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rule_version_error", err.Error())
		return
	}

	actor, _ := r.Context().Value(ctxUsername).(string)

	// Determine prev snapshot (if any) so orchestrator can roll back.
	var prevSnapshot int64
	if prev, err := db.ListRuleVersions(s.DB, 2); err == nil && len(prev) >= 2 {
		// [0] is the version we just wrote; [1] is the previous.
		prevSnapshot = prev[1].SnapshotID
	}

	appRes, appErr := s.Applier.ApplyRules(r.Context(), snapID, rvID, prevSnapshot)

	result := "ok"
	if appErr != nil {
		result = "failed"
		if errors.Is(appErr, orchestrator.ErrApplyInFlight) {
			result = "in_flight"
		}
	}
	if appRes.RolledBack {
		result = "rolled_back"
		if prevSnapshot != 0 {
			if prev, err := db.ListRuleVersions(s.DB, 5); err == nil {
				for _, p := range prev {
					if p.SnapshotID == prevSnapshot {
						_ = db.SetActiveRuleVersion(s.DB, p.ID)
						break
					}
				}
			}
		}
	}

	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "rules.apply",
		Target: rules.HashRules(set),
		After:  string(yamlBytes),
		Result: result,
		IP:     clientIP(r),
	})

	// Hot-swap the DNS resolver's RuleTable to match the just-applied rule
	// set. This is a concern the handler owns separately from the
	// orchestrator apply above: if the Applier call failed, the data plane
	// never changed, so the resolver must not be touched either. A
	// successful Applier call always proceeds here — the DNS plane build
	// is independent of the mihomo/orchestrator health-check lifecycle.
	if appErr == nil {
		entry := s.rebuildAndPublish(r.Context(), set.Rules, "apply")
		if entry.Status == "failed" {
			s.Logger.Warn("rules.apply: resolver rebuild failed",
				"apply_id", entry.ID, "err", entry.Error)
		}
		if entry.Status == "pending" {
			w.Header().Set("Location", "/api/v1/applies/"+entry.ID)
			writeJSON(w, http.StatusAccepted, map[string]any{
				"apply_id": entry.ID,
				"hash":     entry.Hash,
				"status":   "pending",
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, applyResponse{
		SnapshotID:    snapID,
		RuleVersionID: rvID,
		RolledBack:    appRes.RolledBack,
		Health:        appRes.Health,
		Reason:        appRes.Reason,
	})
}

// chinalistSyncRequest allows the caller to override the URL for one-off
// refreshes. Empty url falls back to s.BaseConfig.DNS.ChinaListSource.
type chinalistSyncRequest struct {
	URL string `json:"url"`
}

type chinalistSyncResponse struct {
	URL       string `json:"url"`
	Path      string `json:"path"`
	Refreshed bool   `json:"refreshed"`
	ETag      string `json:"etag,omitempty"`
}

func (s *Server) handleChinalistSync(w http.ResponseWriter, r *http.Request) {
	if s.BaseConfig == nil {
		writeError(w, http.StatusInternalServerError, "config_missing", "base config not wired")
		return
	}
	var req chinalistSyncRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // body is optional
	url := req.URL
	if url == "" {
		url = s.BaseConfig.DNS.ChinaListSource
	}
	path := s.BaseConfig.DNS.ChinaListPath
	if url == "" || path == "" {
		writeError(w, http.StatusBadRequest, "chinalist_unconfigured",
			"dns.chinalist_source and dns.chinalist_path must both be set")
		return
	}

	before, _ := db.GetRuleSourceETag(s.DB, url)
	if err := rules.Sync(r.Context(), s.DB, url, path); err != nil {
		writeError(w, http.StatusBadGateway, "sync_failed", err.Error())
		return
	}
	after, _ := db.GetRuleSourceETag(s.DB, url)

	actor, _ := r.Context().Value(ctxUsername).(string)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "rules.chinalist.sync",
		Target: url,
		Before: before,
		After:  after,
		Result: "ok",
		IP:     clientIP(r),
	})

	writeJSON(w, http.StatusOK, chinalistSyncResponse{
		URL:       url,
		Path:      path,
		Refreshed: after != before,
		ETag:      after,
	})
}

func (s *Server) handleApplyStatus(w http.ResponseWriter, r *http.Request) {
	if s.Applier == nil {
		writeError(w, http.StatusInternalServerError, "applier_missing", "applier not wired")
		return
	}
	snap, err := s.Applier.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
