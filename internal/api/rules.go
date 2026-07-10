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
	Rules    []rules.Rule         `json:"rules"`
	Fixtures []rules.TestFixture  `json:"fixtures"`
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
