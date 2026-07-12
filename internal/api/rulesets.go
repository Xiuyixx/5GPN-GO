// Rulesets endpoints: registered rule providers with cached content +
// last-sync metadata. The panel's Rules page reads GET /rulesets to
// render the "Rulesets" section; the Import dialog POSTs new sources
// here; the sync button hits POST /rulesets/{name}/sync.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/netguard"
	"github.com/Xiuyixx/5GPN-Go/internal/rulesets"
)

// rulesetView is the JSON shape returned to the panel. Excludes the raw
// content blob so payloads stay small; the panel never needs the raw
// body directly.
type rulesetView struct {
	Name         string `json:"name"`
	SourceURL    string `json:"source_url"`
	Kind         string `json:"kind"`
	Action       string `json:"action"`
	Priority     int    `json:"priority"`
	Enabled      bool   `json:"enabled"`
	RuleCount    int    `json:"rule_count"`
	LastSyncedAt *int64 `json:"last_synced_at,omitempty"` // unix epoch, nullable
	LastError    string `json:"last_error,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	CreatedBy    string `json:"created_by,omitempty"`
}

func toView(r rulesets.Ruleset) rulesetView {
	v := rulesetView{
		Name:      r.Name,
		SourceURL: r.SourceURL,
		Kind:      r.Kind,
		Action:    r.Action,
		Priority:  r.Priority,
		Enabled:   r.Enabled,
		RuleCount: r.RuleCount,
		LastError: r.LastError,
		CreatedAt: r.CreatedAt.Unix(),
		CreatedBy: r.CreatedBy,
	}
	if r.LastSyncedAt != nil {
		ts := r.LastSyncedAt.Unix()
		v.LastSyncedAt = &ts
	}
	return v
}

// rulesetRegisterRequest is the shape POST /api/v1/rulesets accepts. Kind
// is optional — the parser auto-detects (Clash / gfwlist / base64) but the
// caller can pin it. Name is optional; auto-generated when empty.
type rulesetRegisterRequest struct {
	Name      string `json:"name"`
	SourceURL string `json:"source_url"`
	Kind      string `json:"kind"`     // "clash" | "gfwlist" | ""
	Action    string `json:"action"`   // policy imported non-terminals get rewritten to
	Priority  int    `json:"priority"` // default 500 when zero
	Enabled   *bool  `json:"enabled"`  // default true when nil
}

var nameSafe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// handleListRulesets returns every ruleset for panel rendering.
func (s *Server) handleListRulesets(w http.ResponseWriter, r *http.Request) {
	if s.Rulesets == nil {
		writeJSON(w, http.StatusOK, map[string]any{"rulesets": []rulesetView{}})
		return
	}
	list, err := s.Rulesets.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_error", err.Error())
		return
	}
	out := make([]rulesetView, 0, len(list))
	for _, rs := range list {
		out = append(out, toView(rs))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rulesets": out})
}

// handleRegisterRuleset upserts a ruleset row + kicks off an immediate
// sync so the operator's UI shows a rule_count within seconds of
// creation. Sync failure is surfaced as last_error on the row, not as
// an HTTP error, so the row is registered either way.
func (s *Server) handleRegisterRuleset(w http.ResponseWriter, r *http.Request) {
	if s.Rulesets == nil || s.RulesetSyncer == nil {
		writeError(w, http.StatusServiceUnavailable, "rulesets_unavailable",
			"rulesets store not wired at daemon boot")
		return
	}
	var req rulesetRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SourceURL = strings.TrimSpace(req.SourceURL)
	req.Action = strings.TrimSpace(req.Action)
	if req.SourceURL == "" {
		writeError(w, http.StatusBadRequest, "empty_url", "source_url is required")
		return
	}
	if _, err := netguard.ValidateHTTPURL(req.SourceURL); err != nil {
		writeError(w, http.StatusBadRequest, "bad_url", err.Error())
		return
	}
	if req.Action == "" {
		writeError(w, http.StatusBadRequest, "empty_action", "action is required")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "rs-" + randomHexShort(4)
	}
	if !nameSafe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "bad_name",
			"name must be 1..64 chars from [A-Za-z0-9._-]")
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = rulesets.KindClash // auto-detector will override at sync time via the parser
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.Rulesets.Upsert(r.Context(), rulesets.Ruleset{
		Name:      name,
		SourceURL: req.SourceURL,
		Kind:      kind,
		Action:    req.Action,
		Priority:  req.Priority,
		Enabled:   enabled,
		CreatedBy: actorFromCtx(r),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "upsert_error", err.Error())
		return
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actorFromCtx(r), Action: "rulesets.register", Target: name,
		Result: "ok", After: req.SourceURL, IP: clientIP(r),
	})

	// Fire an initial sync so the card renders a real rule_count and
	// last_synced_at right away. Detached so the response returns fast;
	// the panel polls list on next refresh.
	logger := s.Logger
	go func(name string) {
		syncCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.RulesetSyncer.SyncOne(syncCtx, name); err != nil && logger != nil {
			logger.Warn("rulesets.register: initial sync failed", "name", name, "err", err)
		}
	}(name)

	rs, err := s.Rulesets.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toView(*rs))
}

// handleSyncRuleset forces a fresh fetch for one ruleset. Called by
// the panel's "Sync now" button.
func (s *Server) handleSyncRuleset(w http.ResponseWriter, r *http.Request) {
	if s.Rulesets == nil || s.RulesetSyncer == nil {
		writeError(w, http.StatusServiceUnavailable, "rulesets_unavailable",
			"rulesets store not wired at daemon boot")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "empty_name", "name path param required")
		return
	}
	if err := s.RulesetSyncer.SyncOne(r.Context(), name); err != nil {
		writeError(w, http.StatusBadGateway, "sync_failed", err.Error())
		return
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actorFromCtx(r), Action: "rulesets.sync", Target: name,
		Result: "ok", IP: clientIP(r),
	})
	rs, err := s.Rulesets.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toView(*rs))
}

// handleToggleRuleset flips enabled without touching content or the
// sync schedule.
func (s *Server) handleToggleRuleset(w http.ResponseWriter, r *http.Request) {
	if s.Rulesets == nil {
		writeError(w, http.StatusServiceUnavailable, "rulesets_unavailable",
			"rulesets store not wired at daemon boot")
		return
	}
	name := chi.URLParam(r, "name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.Rulesets.SetEnabled(r.Context(), name, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "toggle_error", err.Error())
		return
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actorFromCtx(r), Action: "rulesets.toggle", Target: name,
		Result: enabledStr(body.Enabled), IP: clientIP(r),
	})
	rs, err := s.Rulesets.Get(r.Context(), name)
	if err != nil {
		if errors.Is(err, rulesets.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "ruleset not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "read_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toView(*rs))
}

// handleDeleteRuleset removes a ruleset row outright.
func (s *Server) handleDeleteRuleset(w http.ResponseWriter, r *http.Request) {
	if s.Rulesets == nil {
		writeError(w, http.StatusServiceUnavailable, "rulesets_unavailable",
			"rulesets store not wired at daemon boot")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "empty_name", "name path param required")
		return
	}
	if err := s.Rulesets.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "delete_error", err.Error())
		return
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actorFromCtx(r), Action: "rulesets.delete", Target: name,
		Result: "ok", IP: clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func enabledStr(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
