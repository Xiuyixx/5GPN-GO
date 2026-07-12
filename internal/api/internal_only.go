// Internal-only access-gate settings surface.
//
// GET  /api/v1/settings/frontdoor/internal-only
//   Return the current toggle state and CIDR list, falling back to
//   the runtime default (private RFC1918 + loopback) when the CIDR
//   key is unset.
//
// POST /api/v1/settings/frontdoor/internal-only
//   Persist a new toggle state and/or CIDR list. Validation runs
//   BEFORE any write so a bad CIDR entry can't half-apply. On a
//   successful write we call Gate.Refresh so the change takes effect
//   live — no daemon restart required. The proxy listeners
//   (mtproxy/sniforward/quicforward) share the same *access.Gate,
//   so their accept-time checks pick up the new policy on the next
//   incoming connection.
//
// The middleware itself lives in internal_only_middleware.go and is
// wired into Router() ordered after cors so preflight OPTIONS get a
// proper CORS response before the gate rejects the actual request
// from a disallowed IP.
package api

import (
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
	writeJSON(w, http.StatusOK, s.readInternalOnly(r))
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
	if req.CIDRs != nil {
		if err := access.ValidateCIDRs(*req.CIDRs); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
			return
		}
	}

	ctx := r.Context()
	actor := actorFromCtx(r)

	if req.Enabled != nil {
		if err := s.Settings.SetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled, *req.Enabled, actor); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	if req.CIDRs != nil {
		if err := s.Settings.SetString(ctx, settings.KeyFrontdoorInternalCIDRs, *req.CIDRs, actor); err != nil {
			writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}

	// Live-apply: swap the gate's atomic snapshot so the middleware
	// and every proxy's accept-time check see the new policy on the
	// next call. Errors here are non-fatal — the DB write is what
	// persists across boots, and Refresh only fails on a settings.Get
	// error (SQL-level), not on missing keys.
	if s.Gate != nil {
		_ = s.Gate.Refresh(ctx)
	}

	writeJSON(w, http.StatusOK, s.readInternalOnly(r))
}

func (s *Server) readInternalOnly(r *http.Request) internalOnlyResponse {
	ctx := r.Context()
	enabled, _ := s.Settings.GetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled)
	cidrs, _ := s.Settings.GetString(ctx, settings.KeyFrontdoorInternalCIDRs)
	if cidrs == "" {
		cidrs = access.DefaultInternalCIDRs
	}
	return internalOnlyResponse{Enabled: enabled, CIDRs: cidrs}
}
