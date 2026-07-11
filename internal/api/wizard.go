// Wizard endpoints — GET/POST /api/v1/settings/panel and the wizard.complete
// signal surfaced on /api/v1/bootstrap. These wire the settings.Store into
// the HTTP surface so the panel wizard (v0.2.5) can round-trip config
// without touching /etc/5gpn/config.yaml (which is owned by the installer
// / operator and gated by ProtectSystem=strict on the deployed unit).

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
	"github.com/Xiuyixx/5GPN-Go/internal/tgbot"
)

// panelSettingsResponse is the shape returned by GET /api/v1/settings/panel.
// It carries every panel-managed setting AND the wizard.complete flag so a
// single GET is enough to prefill the wizard form + gate its routing.
//
// Token secrets are never leaked back: TGBot.Token is echoed as
// TokenSet + TokenMasked only. ACME email is echoed verbatim (not a secret).
type panelSettingsResponse struct {
	Server struct {
		Domain    string `json:"domain"`
		PanelBind string `json:"panel_bind"`
		PanelPort int    `json:"panel_port"`
	} `json:"server"`
	TLS struct {
		ACMEEnabled bool   `json:"acme_enabled"`
		ACMEEmail   string `json:"acme_email"`
	} `json:"tls"`
	TGBot struct {
		TokenSet     bool    `json:"token_set"`
		TokenMasked  string  `json:"token_masked,omitempty"`
		AdminChatIDs []int64 `json:"admin_chat_ids"`
	} `json:"tgbot"`
	WAShim struct {
		Enabled   bool     `json:"enabled"`
		Listen    string   `json:"listen"`
		Port      int      `json:"port"`
		Backend   string   `json:"backend"`
		WAHost    string   `json:"wa_host"`
		AllowCIDR []string `json:"allow_cidr"`
	} `json:"washim"`
	Wizard struct {
		Complete bool `json:"complete"`
	} `json:"wizard"`
}

// panelSettingsUpdate mirrors the response for POST. Every field is a
// pointer so callers can send partial updates — a missing field means
// "leave the DB row as-is". Token is a raw string (not masked) because
// the caller is providing the new value.
type panelSettingsUpdate struct {
	Server *struct {
		Domain    *string `json:"domain"`
		PanelBind *string `json:"panel_bind"`
		PanelPort *int    `json:"panel_port"`
	} `json:"server,omitempty"`
	TLS *struct {
		ACMEEnabled *bool   `json:"acme_enabled"`
		ACMEEmail   *string `json:"acme_email"`
	} `json:"tls,omitempty"`
	TGBot *struct {
		Token        *string  `json:"token"`
		AdminChatIDs *[]int64 `json:"admin_chat_ids"`
	} `json:"tgbot,omitempty"`
	WAShim *struct {
		Enabled   *bool     `json:"enabled"`
		Listen    *string   `json:"listen"`
		Port      *int      `json:"port"`
		Backend   *string   `json:"backend"`
		WAHost    *string   `json:"wa_host"`
		AllowCIDR *[]string `json:"allow_cidr"`
	} `json:"washim,omitempty"`
	Wizard *struct {
		Complete *bool `json:"complete"`
	} `json:"wizard,omitempty"`
}

// handleGetPanelSettings returns every panel-managed setting the wizard
// needs to prefill, plus the wizard.complete flag.
func (s *Server) handleGetPanelSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable",
			"settings store not wired at daemon boot")
		return
	}
	resp, err := readPanelSettings(r.Context(), s.Settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdatePanelSettings applies a partial update. Every field that
// arrives with a non-nil pointer is written to the store; the rest are
// left untouched. On success returns the fresh snapshot so the panel can
// update its cached form state.
func (s *Server) handleUpdatePanelSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable",
			"settings store not wired at daemon boot")
		return
	}
	var req panelSettingsUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	actor := actorFromCtx(r)
	ctx := r.Context()

	if req.Server != nil {
		if v := req.Server.Domain; v != nil {
			if err := s.Settings.SetString(ctx, settings.KeyServerDomain, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.Server.PanelBind; v != nil {
			if err := s.Settings.SetString(ctx, settings.KeyServerPanelBind, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.Server.PanelPort; v != nil {
			if *v < 1 || *v > 65535 {
				writeError(w, http.StatusBadRequest, "bad_port", "panel_port must be 1..65535")
				return
			}
			if err := s.Settings.SetInt(ctx, settings.KeyServerPanelPort, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
	}
	if req.TLS != nil {
		if v := req.TLS.ACMEEnabled; v != nil {
			if err := s.Settings.SetBool(ctx, settings.KeyTLSACMEEnabled, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.TLS.ACMEEmail; v != nil {
			if err := s.Settings.SetString(ctx, settings.KeyTLSACMEEmail, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
	}
	if req.TGBot != nil {
		// Token+admin ids drive the live Manager AND get persisted so the
		// bot survives a daemon restart. Empty token = disable.
		newToken := ""
		if v := req.TGBot.Token; v != nil {
			newToken = *v
			if err := s.Settings.SetString(ctx, settings.KeyTGBotToken, newToken, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		newIDs := []int64(nil)
		if v := req.TGBot.AdminChatIDs; v != nil {
			newIDs = *v
			if err := s.Settings.SetJSON(ctx, settings.KeyTGBotAdminChats, newIDs, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		// If the caller sent either field, hot-reload the running bot.
		if req.TGBot.Token != nil || req.TGBot.AdminChatIDs != nil {
			if s.TGBot != nil {
				// Merge partial: fill missing from the current DB snapshot.
				token, ids := effectiveTGBot(ctx, s.Settings, req.TGBot.Token, req.TGBot.AdminChatIDs)
				if err := s.TGBot.Update(ctx, token, ids); err != nil {
					writeError(w, http.StatusBadGateway, "tgbot_update_failed", err.Error())
					return
				}
			}
		}
	}
	if req.WAShim != nil {
		if v := req.WAShim.Enabled; v != nil {
			if err := s.Settings.SetBool(ctx, settings.KeyWAShimEnabled, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.WAShim.Listen; v != nil {
			if err := s.Settings.SetString(ctx, settings.KeyWAShimListen, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.WAShim.Port; v != nil {
			if *v < 1 || *v > 65535 {
				writeError(w, http.StatusBadRequest, "bad_port", "washim.port must be 1..65535")
				return
			}
			if err := s.Settings.SetInt(ctx, settings.KeyWAShimPort, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.WAShim.Backend; v != nil {
			if err := s.Settings.SetString(ctx, settings.KeyWAShimBackend, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.WAShim.WAHost; v != nil {
			if err := s.Settings.SetString(ctx, settings.KeyWAShimWAHost, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
		if v := req.WAShim.AllowCIDR; v != nil {
			if err := s.Settings.SetJSON(ctx, settings.KeyWAShimAllowCIDR, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
	}
	if req.Wizard != nil {
		if v := req.Wizard.Complete; v != nil {
			if err := s.Settings.SetBool(ctx, settings.KeyWizardComplete, *v, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "write_error", err.Error())
				return
			}
		}
	}

	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actor, Action: "panel.settings.update", Result: "ok", IP: clientIP(r),
	})

	resp, err := readPanelSettings(ctx, s.Settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// readPanelSettings hydrates the response shape from the store. Missing
// keys become zero values so first-time reads return a well-shaped
// object the panel can render.
func readPanelSettings(ctx context.Context, store *settings.Store) (*panelSettingsResponse, error) {
	out := &panelSettingsResponse{}

	if v, err := store.GetString(ctx, settings.KeyServerDomain); err != nil {
		return nil, err
	} else {
		out.Server.Domain = v
	}
	if v, err := store.GetString(ctx, settings.KeyServerPanelBind); err != nil {
		return nil, err
	} else {
		out.Server.PanelBind = v
	}
	if v, err := store.GetInt(ctx, settings.KeyServerPanelPort); err != nil {
		return nil, err
	} else {
		out.Server.PanelPort = v
	}
	if v, err := store.GetBool(ctx, settings.KeyTLSACMEEnabled); err != nil {
		return nil, err
	} else {
		out.TLS.ACMEEnabled = v
	}
	if v, err := store.GetString(ctx, settings.KeyTLSACMEEmail); err != nil {
		return nil, err
	} else {
		out.TLS.ACMEEmail = v
	}
	if v, err := store.GetString(ctx, settings.KeyTGBotToken); err != nil {
		return nil, err
	} else {
		out.TGBot.TokenSet = v != ""
		out.TGBot.TokenMasked = tgbot.MaskToken(v)
	}
	var ids []int64
	if err := store.GetJSON(ctx, settings.KeyTGBotAdminChats, &ids); err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, err
	}
	out.TGBot.AdminChatIDs = ids
	if v, err := store.GetBool(ctx, settings.KeyWAShimEnabled); err != nil {
		return nil, err
	} else {
		out.WAShim.Enabled = v
	}
	if v, err := store.GetString(ctx, settings.KeyWAShimListen); err != nil {
		return nil, err
	} else {
		out.WAShim.Listen = v
	}
	if v, err := store.GetInt(ctx, settings.KeyWAShimPort); err != nil {
		return nil, err
	} else {
		out.WAShim.Port = v
	}
	if v, err := store.GetString(ctx, settings.KeyWAShimBackend); err != nil {
		return nil, err
	} else {
		out.WAShim.Backend = v
	}
	if v, err := store.GetString(ctx, settings.KeyWAShimWAHost); err != nil {
		return nil, err
	} else {
		out.WAShim.WAHost = v
	}
	var cidrs []string
	if err := store.GetJSON(ctx, settings.KeyWAShimAllowCIDR, &cidrs); err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, err
	}
	out.WAShim.AllowCIDR = cidrs
	if v, err := store.GetBool(ctx, settings.KeyWizardComplete); err != nil {
		return nil, err
	} else {
		out.Wizard.Complete = v
	}
	return out, nil
}

// effectiveTGBot combines a partial update with the current DB snapshot
// so a caller can change JUST the token or JUST the admin ids without
// losing the other field.
func effectiveTGBot(ctx context.Context, store *settings.Store, tokenPatch *string, idsPatch *[]int64) (string, []int64) {
	token := ""
	if tokenPatch != nil {
		token = *tokenPatch
	} else {
		token, _ = store.GetString(ctx, settings.KeyTGBotToken)
	}
	var ids []int64
	if idsPatch != nil {
		ids = *idsPatch
	} else {
		_ = store.GetJSON(ctx, settings.KeyTGBotAdminChats, &ids)
	}
	return token, ids
}
