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
	"fmt"
	"net"
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
	// IOS surfaces the Phase 8 preflight-gated profile toggle state so the
	// wizard/settings page can render "Enable iOS profile" as disabled
	// until a preflight has passed, without a second round-trip. The
	// toggle itself is written via POST /api/v1/settings/ios/profile-enabled
	// (which enforces the preflight gate server-side), not through this
	// endpoint's update path.
	IOS struct {
		ProfileEnabled     bool   `json:"profile_enabled"`
		FallbackDoT        string `json:"fallback_dot"`
		PreflightLastAt    *int64 `json:"preflight_last_at,omitempty"`
		PreflightLastError string `json:"preflight_last_error,omitempty"`
	} `json:"ios"`
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
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	actor := actorFromCtx(r)
	ctx := r.Context()
	values := map[string]any{}
	if req.Server != nil {
		if v := req.Server.Domain; v != nil {
			values[settings.KeyServerDomain] = *v
		}
		if v := req.Server.PanelBind; v != nil {
			values[settings.KeyServerPanelBind] = *v
		}
		if v := req.Server.PanelPort; v != nil {
			if *v < 1 || *v > 65535 {
				writeError(w, http.StatusBadRequest, "bad_port", "panel_port must be 1..65535")
				return
			}
			values[settings.KeyServerPanelPort] = *v
		}
	}
	if req.TLS != nil {
		if v := req.TLS.ACMEEnabled; v != nil {
			values[settings.KeyTLSACMEEnabled] = *v
		}
		if v := req.TLS.ACMEEmail; v != nil {
			values[settings.KeyTLSACMEEmail] = *v
		}
	}
	if req.WAShim != nil {
		if v := req.WAShim.Enabled; v != nil {
			values[settings.KeyWAShimEnabled] = *v
		}
		if v := req.WAShim.Listen; v != nil {
			values[settings.KeyWAShimListen] = *v
		}
		if v := req.WAShim.Port; v != nil {
			if *v < 1 || *v > 65535 {
				writeError(w, http.StatusBadRequest, "bad_port", "washim.port must be 1..65535")
				return
			}
			values[settings.KeyWAShimPort] = *v
		}
		if v := req.WAShim.Backend; v != nil {
			values[settings.KeyWAShimBackend] = *v
		}
		if v := req.WAShim.WAHost; v != nil {
			values[settings.KeyWAShimWAHost] = *v
		}
		if v := req.WAShim.AllowCIDR; v != nil {
			if len(*v) == 0 {
				writeError(w, http.StatusBadRequest, "invalid_cidr", "washim.allow_cidr must not be empty")
				return
			}
			for _, raw := range *v {
				if _, _, err := net.ParseCIDR(raw); err != nil {
					writeError(w, http.StatusBadRequest, "invalid_cidr", fmt.Sprintf("washim.allow_cidr %q: %v", raw, err))
					return
				}
			}
			values[settings.KeyWAShimAllowCIDR] = *v
		}
	}
	if req.Wizard != nil {
		if v := req.Wizard.Complete; v != nil {
			values[settings.KeyWizardComplete] = *v
		}
	}

	// Validate Telegram while the previous bot remains live. The Manager swaps
	// runtime state only after the settings transaction commits successfully.
	tgbotWarning := ""
	settingsCommitted := false
	if req.TGBot != nil && (req.TGBot.Token != nil || req.TGBot.AdminChatIDs != nil) {
		token, ids, err := effectiveTGBot(ctx, s.Settings, req.TGBot.Token, req.TGBot.AdminChatIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_error", err.Error())
			return
		}
		if req.TGBot.Token != nil {
			values[settings.KeyTGBotToken] = *req.TGBot.Token
		}
		if req.TGBot.AdminChatIDs != nil {
			values[settings.KeyTGBotAdminChats] = *req.TGBot.AdminChatIDs
		}
		if s.TGBot != nil {
			var persistErr error
			if err := s.TGBot.UpdateWithCommit(ctx, token, ids, func() error {
				persistErr = s.Settings.SetMany(ctx, values, actor)
				return persistErr
			}); err != nil {
				if persistErr != nil {
					writeError(w, http.StatusInternalServerError, "write_error", persistErr.Error())
					return
				}
				tgbotWarning = err.Error()
				s.Logger.Warn("wizard: tgbot hot-reload rejected; settings not persisted", "actor", actor, "err", err)
				delete(values, settings.KeyTGBotToken)
				delete(values, settings.KeyTGBotAdminChats)
			} else {
				settingsCommitted = true
			}
		}
	}
	if !settingsCommitted {
		if err := s.Settings.SetMany(ctx, values, actor); err != nil {
			writeError(w, http.StatusInternalServerError, "write_error", err.Error())
			return
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
	// If TG bot hot-reload failed but every other field landed, surface
	// the reason to the caller so the wizard can render a warning banner
	// instead of a red error alert. The wizard save is still considered
	// successful and wizard.complete is preserved as requested.
	if tgbotWarning != "" {
		writeJSON(w, http.StatusOK, struct {
			*panelSettingsResponse
			TGBotWarning string `json:"tgbot_warning"`
		}{panelSettingsResponse: resp, TGBotWarning: tgbotWarning})
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
	if v, err := store.GetBool(ctx, settings.KeyFrontdoorIOSProfileEnabled); err != nil {
		return nil, err
	} else {
		out.IOS.ProfileEnabled = v
	}
	if v, err := store.GetString(ctx, settings.KeyFrontdoorFallbackDoT); err != nil {
		return nil, err
	} else {
		out.IOS.FallbackDoT = v
	}
	var preflightLastAt int64
	if err := store.GetJSON(ctx, settings.KeyFrontdoorPreflightLastAt, &preflightLastAt); err != nil {
		if !errors.Is(err, settings.ErrNotFound) {
			return nil, err
		}
	} else {
		out.IOS.PreflightLastAt = &preflightLastAt
	}
	if v, err := store.GetString(ctx, settings.KeyFrontdoorPreflightLastError); err != nil {
		return nil, err
	} else {
		out.IOS.PreflightLastError = v
	}
	return out, nil
}

// effectiveTGBot combines a partial update with the current DB snapshot
// so a caller can change JUST the token or JUST the admin ids without
// losing the other field.
func effectiveTGBot(ctx context.Context, store *settings.Store, tokenPatch *string, idsPatch *[]int64) (string, []int64, error) {
	token := ""
	if tokenPatch != nil {
		token = *tokenPatch
	} else {
		var err error
		token, err = store.GetString(ctx, settings.KeyTGBotToken)
		if err != nil {
			return "", nil, err
		}
	}
	var ids []int64
	if idsPatch != nil {
		ids = *idsPatch
	} else {
		if err := store.GetJSON(ctx, settings.KeyTGBotAdminChats, &ids); err != nil && !errors.Is(err, settings.ErrNotFound) {
			return "", nil, err
		}
	}
	return token, ids, nil
}
