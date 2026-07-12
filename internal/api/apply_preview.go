package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// applyPreviewRequest is the candidate ruleset a caller wants to preview —
// same shape as applyRequest, minus the audit Note (a preview never writes
// an audit entry or touches the DB).
type applyPreviewRequest struct {
	Rules []rules.Rule `json:"rules"`
}

// applyPreviewSample is one concrete example of a changed rule, surfaced so
// a diff modal can show a few real entries instead of bare counts.
type applyPreviewSample struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	Action  string `json:"action"`
}

// applyPreviewSampleCap bounds how many examples handleApplyPreview
// collects per bucket (added_block, removed_direct, etc).
const applyPreviewSampleCap = 5

// applyPreviewResponse is the static diff between whatever RuleTable is
// currently published on s.Resolver and the candidate table
// resolver.BuildTable(req.Rules) would produce. ChangedProxy counts
// entries present in both tables whose action changed AND whose new
// (candidate) action is "proxy" — the transition most worth flagging,
// since a rule that used to explicitly block/direct traffic silently
// falling through to the proxy default is the shape of regression an
// operator most needs to catch before confirming a real apply.
type applyPreviewResponse struct {
	Hash          string                          `json:"hash"`
	AddedBlock    int                             `json:"added_block"`
	AddedDirect   int                             `json:"added_direct"`
	AddedProxy    int                             `json:"added_proxy"`
	RemovedBlock  int                             `json:"removed_block"`
	RemovedDirect int                             `json:"removed_direct"`
	RemovedProxy  int                             `json:"removed_proxy"`
	ChangedProxy  int                             `json:"changed_proxy"`
	Total         int                             `json:"total"`
	Sample        map[string][]applyPreviewSample `json:"sample,omitempty"`
}

// handleApplyPreview is POST /api/v1/rules/apply/preview — a read-only
// counterpart to handleApply. It expands rulesets exactly like
// handleApply/handleDryRun do, builds a candidate resolver.RuleTable off
// req.Rules, diffs it against whatever s.Resolver currently has published,
// and returns counts. It never calls Store.Publish, never writes to the
// DB, and never records an audit entry — running this endpoint has zero
// side effects on the live resolver or the rest of the system.
func (s *Server) handleApplyPreview(w http.ResponseWriter, r *http.Request) {
	var req applyPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var err error
	req.Rules, err = s.expandRulesets(r.Context(), req.Rules)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ruleset_expand_failed", err.Error())
		return
	}
	set := &rules.RuleSet{Rules: req.Rules}
	if err := set.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	candidate, err := resolver.BuildTable(req.Rules)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build_failed", err.Error())
		return
	}

	// Nil-safe baseline: no Resolver wired in, or a Resolver that has never
	// had Publish() called yet, both collapse to "empty table" so a fresh
	// install's first preview still reports every candidate entry as added.
	var baseline *resolver.RuleTable
	if s.Resolver != nil {
		baseline = s.Resolver.Load()
	}

	resp := diffRuleTables(baseline, candidate)
	resp.Hash = hex.EncodeToString(candidate.Hash[:])
	writeJSON(w, http.StatusOK, resp)
}

// diffRuleTables set-diffs two compiled RuleTable snapshots via their
// exported Entries() maps. baseline may be nil (treated as empty).
func diffRuleTables(baseline, candidate *resolver.RuleTable) applyPreviewResponse {
	var resp applyPreviewResponse
	baseEntries := baseline.Entries()
	candEntries := candidate.Entries()

	samples := make(map[string][]applyPreviewSample)
	addSample := func(bucket string, e resolver.Entry) {
		if len(samples[bucket]) >= applyPreviewSampleCap {
			return
		}
		samples[bucket] = append(samples[bucket], applyPreviewSample{
			Kind: e.Kind, Pattern: e.Pattern, Action: e.Action,
		})
	}

	for key, ce := range candEntries {
		be, existed := baseEntries[key]
		if !existed {
			switch ce.Action {
			case string(resolver.ActionBlock):
				resp.AddedBlock++
				addSample("added_block", ce)
			case string(resolver.ActionDirect):
				resp.AddedDirect++
				addSample("added_direct", ce)
			default:
				resp.AddedProxy++
				addSample("added_proxy", ce)
			}
			continue
		}
		if be.Action != ce.Action && ce.Action == string(resolver.ActionProxy) {
			resp.ChangedProxy++
			addSample("changed_proxy", ce)
		}
	}
	for key, be := range baseEntries {
		if _, stillPresent := candEntries[key]; stillPresent {
			continue
		}
		switch be.Action {
		case string(resolver.ActionBlock):
			resp.RemovedBlock++
			addSample("removed_block", be)
		case string(resolver.ActionDirect):
			resp.RemovedDirect++
			addSample("removed_direct", be)
		default:
			resp.RemovedProxy++
			addSample("removed_proxy", be)
		}
	}

	resp.Total = resp.AddedBlock + resp.AddedDirect + resp.AddedProxy +
		resp.RemovedBlock + resp.RemovedDirect + resp.RemovedProxy + resp.ChangedProxy
	if len(samples) > 0 {
		resp.Sample = samples
	}
	return resp
}
