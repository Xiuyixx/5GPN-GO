// Settings surface: currently exposes GET/POST /api/v1/settings/tgbot for
// live Telegram-bot control. Other Settings sections (updater, iOS, panel
// password) live in their own files.

package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

type tgbotStatusResponse struct {
	Enabled     bool   `json:"enabled"`
	AdminCount  int    `json:"admin_count"`
	TokenMasked string `json:"token_masked,omitempty"`
}

type tgbotUpdateRequest struct {
	Token        string  `json:"token"`          // empty = disable + stop
	AdminChatIDs []int64 `json:"admin_chat_ids"` // required when Token is non-empty
}

// handleGetTgbotSettings is idempotent status. Returns 200 with
// enabled=false when no manager is wired or bot is off. Never leaks the
// raw token — only the masked form.
func (s *Server) handleGetTgbotSettings(w http.ResponseWriter, r *http.Request) {
	if s.TGBot == nil {
		writeJSON(w, http.StatusOK, tgbotStatusResponse{Enabled: false})
		return
	}
	st := s.TGBot.Status()
	writeJSON(w, http.StatusOK, tgbotStatusResponse{
		Enabled:     st.Enabled,
		AdminCount:  st.AdminCount,
		TokenMasked: st.TokenMasked,
	})
}

// handleUpdateTgbotSettings applies token + admin ids and reloads the bot
// in-process. Empty token = Stop. Returns the new masked status on
// success.
func (s *Server) handleUpdateTgbotSettings(w http.ResponseWriter, r *http.Request) {
	if s.TGBot == nil {
		writeError(w, http.StatusServiceUnavailable, "tgbot_unavailable",
			"TG bot manager not wired at daemon boot")
		return
	}
	var req tgbotUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	// Enforce plan security: refuse to enable the bot without a whitelist.
	// Stop path (empty token) is always allowed.
	if token != "" && len(req.AdminChatIDs) == 0 {
		writeError(w, http.StatusBadRequest, "no_admins",
			"admin_chat_ids required when token is set")
		return
	}
	if err := s.TGBot.Update(r.Context(), token, req.AdminChatIDs); err != nil {
		writeError(w, http.StatusBadGateway, "tgbot_update_failed", err.Error())
		return
	}
	st := s.TGBot.Status()
	writeJSON(w, http.StatusOK, tgbotStatusResponse{
		Enabled:     st.Enabled,
		AdminCount:  st.AdminCount,
		TokenMasked: st.TokenMasked,
	})
}
