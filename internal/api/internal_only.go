// Internal-only access-gate settings surface.
//
// GET  /api/v1/settings/frontdoor/internal-only
//
//	Return the current toggle state and CIDR list, falling back to
//	the runtime default (private RFC1918 + loopback) when the CIDR
//	key is unset.
//
// POST /api/v1/settings/frontdoor/internal-only
//
//	Persist a new toggle state and/or CIDR list. Validation runs
//	BEFORE any write so a bad CIDR entry can't half-apply. On a
//	successful write we call Gate.Refresh so the change takes effect
//	live — no daemon restart required. The in-process sniforward and
//	quicforward listeners share the same *access.Gate. The external
//	mtg.service is outside this process and is not covered.
//
// The middleware itself lives in internal_only_middleware.go and is
// wired into Router() ordered after cors so preflight OPTIONS get a
// proper CORS response before the gate rejects the actual request
// from a disallowed IP.
package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// internalOnlyDoc is the POST body. Pointer fields let us tell
// "field absent" from "field set to zero value" so a partial update
// leaves the other field alone.
type internalOnlyDoc struct {
	Enabled *bool   `json:"enabled,omitempty"`
	CIDRs   *string `json:"cidrs,omitempty"`
}

// internalOnlyResponse extends the doc with an always-populated
// (non-pointer) view of current state.
type internalOnlyResponse struct {
	Enabled bool   `json:"enabled"`
	CIDRs   string `json:"cidrs"`
}

// handleGetInternalOnly returns the current internal-only settings.
func (s *Server) handleGetInternalOnly(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings store not wired")
		return
	}
	resp, err := s.readInternalOnly(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateInternalOnly writes new values, validates first, and
// live-applies via Gate.Refresh so the middleware and the proxy
// accept-time checks pick up the change on the very next connection.
func (s *Server) handleUpdateInternalOnly(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings store not wired")
		return
	}
	var req internalOnlyDoc
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	ctx := r.Context()
	actor := actorFromCtx(r)
	effective, err := s.readInternalOnly(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	values := map[string]any{}
	if req.Enabled != nil {
		effective.Enabled = *req.Enabled
		values[settings.KeyFrontdoorInternalOnlyEnabled] = *req.Enabled
	}
	if req.CIDRs != nil {
		effective.CIDRs = *req.CIDRs
		values[settings.KeyFrontdoorInternalCIDRs] = *req.CIDRs
	}
	if err := access.ValidateCIDRs(effective.CIDRs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	if err := s.Settings.SetMany(ctx, values, actor); err != nil {
		writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	if s.Gate != nil {
		if err := s.Gate.Configure(effective.Enabled, effective.CIDRs); err != nil {
			writeError(w, http.StatusInternalServerError, "apply_failed", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, effective)
}

func (s *Server) readInternalOnly(ctx context.Context) (internalOnlyResponse, error) {
	enabled, err := s.Settings.GetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled)
	if err != nil {
		return internalOnlyResponse{}, err
	}
	cidrs, err := s.Settings.GetString(ctx, settings.KeyFrontdoorInternalCIDRs)
	if err != nil {
		return internalOnlyResponse{}, err
	}
	if cidrs == "" {
		cidrs = access.DefaultInternalCIDRs
	}
	return internalOnlyResponse{Enabled: enabled, CIDRs: cidrs}, nil
}
