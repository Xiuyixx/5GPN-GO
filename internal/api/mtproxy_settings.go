// Telegram MTProxy settings surface — v0.5.x rewire.
//
// The panel used to control an in-tree MTProto relay (internal/proxy/
// mtproxy) driven off panel_settings.mtproxy.* keys. That code had a
// subtle relay bug we couldn't isolate in reasonable time, so both VPS
// now run the externally-installed 9seconds/mtg 2.x service and this
// handler surface has been rewired to act as a thin controller over
// mtg.service via the internal/mtgctl package.
//
// External API paths are unchanged so the Settings.tsx card stays
// pinned to the same URLs. Response shape and semantics have moved:
//
// GET /api/v1/settings/mtproxy
//
//	Reports the systemd unit's live state. `listen` and `secret_configured`
//	are parsed from the ExecStart line of /etc/systemd/system/mtg.service;
//	`fronting_domain` is decoded from the ee-prefix fake-TLS secret so
//	the operator can confirm which SNI mtg is fronting as. The raw secret
//	is NEVER returned via GET — the one-time reveal happens on generate-
//	secret (below), matching the previous rotate flow.
//
// POST /api/v1/settings/mtproxy
//
//	Toggles the systemd unit on/off and optionally regenerates the secret
//	when the operator picks a different fronting domain. Enabling without
//	a configured secret returns 400 so the panel walks the operator to
//	the Generate button first.
//
// POST /api/v1/settings/mtproxy/generate-secret
//
//	Shells to `mtg generate-secret <domain>`, rewrites the unit's
//	ExecStart with the new secret, daemon-reloads, and restarts the
//	service if it was already active. Returns the fresh base64 secret
//	plus a copy-ready tg:// deep link. Same one-shot contract as
//	before — subsequent GETs do not include the raw value.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/mtgctl"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// MTG is the narrow interface the mtproxy handlers depend on. The
// production implementation is *mtgctl.Controller; tests inject a stub
// so they can assert argv shape and error paths without shelling out.
type MTG interface {
	Status(ctx context.Context) (string, error)
	IsActive(ctx context.Context) (bool, error)
	Enable(ctx context.Context) error
	Disable(ctx context.Context) error
	Restart(ctx context.Context) error
	GenerateSecret(ctx context.Context, frontingDomain string) (string, error)
	WriteUnit(ctx context.Context, listen, secret string) error
	ReadUnit(ctx context.Context) (listen, secret string, err error)
}

// mtproxySettingsResponse is the wire shape for GET / POST. Fields
// mirror the task contract so the panel's TypeScript interface stays
// aligned.
type mtproxySettingsResponse struct {
	Enabled          bool   `json:"enabled"`
	Listen           string `json:"listen"`
	SecretConfigured bool   `json:"secret_configured"`
	FrontingDomain   string `json:"fronting_domain"`
	ConnectLinkHint  string `json:"connect_link_hint"`
	ServiceStatus    string `json:"service_status"`
}

// mtproxyUpdateRequest is the POST body for /api/v1/settings/mtproxy.
// Both fields are pointers so the panel can send "leave untouched".
type mtproxyUpdateRequest struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	FrontingDomain *string `json:"fronting_domain,omitempty"`
}

// mtproxyGenerateSecretRequest is the POST body for generate-secret.
// Empty FrontingDomain defaults to "www.cloudflare.com" server-side.
type mtproxyGenerateSecretRequest struct {
	FrontingDomain string `json:"fronting_domain"`
}

// mtproxyGenerateSecretResponse is the one-shot reveal. `Secret` is the
// full base64-url secret the operator MUST save now.
type mtproxyGenerateSecretResponse struct {
	OK          bool   `json:"ok"`
	Secret      string `json:"secret"`
	ConnectLink string `json:"connect_link"`
}

// handleGetMTProxySettings snapshots mtg.service state. Returns 503
// when the MTG controller is not wired; otherwise 200 with a
// best-effort read of the unit + service status.
func (s *Server) handleGetMTProxySettings(w http.ResponseWriter, r *http.Request) {
	if s.MTG == nil {
		writeError(w, http.StatusServiceUnavailable, "mtg_not_wired",
			"mtg controller not wired — panel host must have 9seconds/mtg installed")
		return
	}
	resp, err := s.readMTGSnapshot(r.Context())
	if err != nil {
		writeMTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateMTProxySettings toggles the service and optionally
// regenerates the secret when the fronting domain changes.
//
// Ordering: fronting-domain regeneration happens BEFORE
// enable/disable so an operator flipping enabled=true + changing the
// domain in one shot never briefly runs the old secret.
func (s *Server) handleUpdateMTProxySettings(w http.ResponseWriter, r *http.Request) {
	if s.MTG == nil {
		writeError(w, http.StatusServiceUnavailable, "mtg_not_wired",
			"mtg controller not wired — panel host must have 9seconds/mtg installed")
		return
	}
	var req mtproxyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	ctx := r.Context()
	currentListen, currentSecret, err := s.MTG.ReadUnit(ctx)
	if err != nil {
		writeMTGError(w, err)
		return
	}
	if currentListen == "" {
		currentListen = mtgctl.DefaultListen
	}
	oldSecret := currentSecret
	currentDomain := mtgctl.DecodeFrontingDomain(currentSecret)

	// Fronting-domain rotation: only regenerate when the caller sent a
	// non-empty value AND it differs from what the current secret
	// already fronts as. Empty string means "leave as-is".
	if req.FrontingDomain != nil {
		want := strings.TrimSpace(*req.FrontingDomain)
		if want != "" && !strings.EqualFold(want, currentDomain) {
			newSecret, err := s.MTG.GenerateSecret(ctx, want)
			if err != nil {
				writeMTGError(w, err)
				return
			}
			if err := s.MTG.WriteUnit(ctx, currentListen, newSecret); err != nil {
				writeMTGError(w, err)
				return
			}
			currentSecret = newSecret
			// Restart only if the service is already active; a disabled
			// service should stay disabled after the write.
			on, err := s.MTG.IsActive(ctx)
			if err != nil {
				writeMTGError(w, restoreMTGUnit(ctx, s.MTG, currentListen, oldSecret, false, err))
				return
			}
			if on {
				if err := s.MTG.Restart(ctx); err != nil {
					writeMTGError(w, restoreMTGUnit(ctx, s.MTG, currentListen, oldSecret, true, err))
					return
				}
			}
		}
	}

	if req.Enabled != nil {
		switch *req.Enabled {
		case true:
			if strings.TrimSpace(currentSecret) == "" {
				writeError(w, http.StatusBadRequest, "secret_not_configured",
					"POST /api/v1/settings/mtproxy/generate-secret first — cannot enable without a secret")
				return
			}
			if err := s.MTG.Enable(ctx); err != nil {
				writeMTGError(w, err)
				return
			}
		case false:
			if err := s.MTG.Disable(ctx); err != nil {
				writeMTGError(w, err)
				return
			}
		}
	}
	resp, err := s.readMTGSnapshot(ctx)
	if err != nil {
		writeMTGError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGenerateMTProxySecret shells to `mtg generate-secret`, rewrites
// the unit file, and — only if the service is currently active —
// restarts it so clients switch to the new secret immediately. When
// the service is disabled we skip the restart so a fresh install can
// generate a secret, then Enable in a separate POST.
func (s *Server) handleGenerateMTProxySecret(w http.ResponseWriter, r *http.Request) {
	if s.MTG == nil {
		writeError(w, http.StatusServiceUnavailable, "mtg_not_wired",
			"mtg controller not wired — panel host must have 9seconds/mtg installed")
		return
	}
	var req mtproxyGenerateSecretRequest
	// Empty body is valid; malformed non-empty JSON is not.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	domain := strings.TrimSpace(req.FrontingDomain)
	if domain == "" {
		domain = "www.cloudflare.com"
	}

	ctx := r.Context()
	domainForLink, err := s.panelDomain(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_failed", err.Error())
		return
	}
	listen, oldSecret, err := s.MTG.ReadUnit(ctx)
	if err != nil {
		writeMTGError(w, err)
		return
	}
	if listen == "" {
		listen = mtgctl.DefaultListen
	}
	secret, err := s.MTG.GenerateSecret(ctx, domain)
	if err != nil {
		writeMTGError(w, err)
		return
	}
	if err := s.MTG.WriteUnit(ctx, listen, secret); err != nil {
		writeMTGError(w, err)
		return
	}
	on, err := s.MTG.IsActive(ctx)
	if err != nil {
		writeMTGError(w, restoreMTGUnit(ctx, s.MTG, listen, oldSecret, false, err))
		return
	}
	if on {
		if err := s.MTG.Restart(ctx); err != nil {
			writeMTGError(w, restoreMTGUnit(ctx, s.MTG, listen, oldSecret, true, err))
			return
		}
	}
	writeJSON(w, http.StatusOK, mtproxyGenerateSecretResponse{
		OK:          true,
		Secret:      secret,
		ConnectLink: buildConnectLink(domainForLink, listen, secret),
	})
}

func restoreMTGUnit(ctx context.Context, controller MTG, listen, oldSecret string, wasActive bool, primary error) error {
	if oldSecret == "" {
		return errors.Join(primary, errors.New("restore previous unit: previous secret is unavailable"))
	}
	// Compensation must survive a client disconnect or the request deadline
	// that may have caused the primary systemctl failure. The controller still
	// applies its own per-command timeout inside this bounded window.
	compCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := controller.WriteUnit(compCtx, listen, oldSecret); err != nil {
		return errors.Join(primary, fmt.Errorf("restore previous unit: %w", err))
	}
	if wasActive {
		if err := controller.Restart(compCtx); err != nil {
			return errors.Join(primary, fmt.Errorf("restart restored unit: %w", err))
		}
	}
	return primary
}

// readMTGSnapshot bundles the "state the panel renders" reads into one
// call so GET and POST return the same shape.
func (s *Server) readMTGSnapshot(ctx context.Context) (mtproxySettingsResponse, error) {
	status, err := s.MTG.Status(ctx)
	if err != nil {
		return mtproxySettingsResponse{}, err
	}
	listen, secret, err := s.MTG.ReadUnit(ctx)
	if err != nil {
		return mtproxySettingsResponse{}, err
	}
	if listen == "" {
		listen = mtgctl.DefaultListen
	}
	configured := strings.TrimSpace(secret) != ""
	domain := ""
	if configured {
		domain = mtgctl.DecodeFrontingDomain(secret)
	}
	domainForLink, err := s.panelDomain(ctx)
	if err != nil {
		return mtproxySettingsResponse{}, err
	}
	return mtproxySettingsResponse{
		Enabled:          status == "active",
		Listen:           listen,
		SecretConfigured: configured,
		FrontingDomain:   domain,
		ConnectLinkHint:  buildConnectLinkHint(domainForLink, listen),
		ServiceStatus:    status,
	}, nil
}

// panelDomain looks up settings.KeyServerDomain so the tg:// link can
// embed the operator's canonical panel hostname. Empty string is fine —
// the hint template falls back to "<panel_domain>" literal.
func (s *Server) panelDomain(ctx context.Context) (string, error) {
	if s.Settings == nil {
		return "", nil
	}
	v, err := s.Settings.GetString(ctx, settings.KeyServerDomain)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// buildConnectLinkHint returns the placeholder template shown to
// operators before they've generated a secret. Keeps the panel copy
// self-explanatory without leaking anything.
func buildConnectLinkHint(domain, listen string) string {
	host := domain
	if host == "" {
		host = "<panel_domain>"
	}
	port := portOnly(listen)
	if port == "" {
		port = "<port>"
	}
	return "clients see this proxy at tg://proxy?server=" + host + "&port=" + port + "&secret=<secret>"
}

// buildConnectLink renders the one-shot tg:// URL returned alongside a
// freshly-minted secret.
func buildConnectLink(domain, listen, secret string) string {
	host := domain
	if host == "" {
		host = "YOUR_SERVER"
	}
	port := portOnly(listen)
	q := url.Values{}
	q.Set("server", host)
	if port != "" {
		q.Set("port", port)
	}
	q.Set("secret", secret)
	return "tg://proxy?" + q.Encode()
}

// portOnly extracts the port half of "host:port" or ":port".
func portOnly(listen string) string {
	if listen == "" {
		return ""
	}
	i := strings.LastIndex(listen, ":")
	if i < 0 {
		return ""
	}
	return listen[i+1:]
}

// writeMTGError maps mtgctl errors to HTTP status codes. ErrNotInstalled
// → 503 with a specific code so the panel can render the
// "install 9seconds/mtg first" hint instead of a generic "internal
// server error" banner.
func writeMTGError(w http.ResponseWriter, err error) {
	if errors.Is(err, mtgctl.ErrNotInstalled) {
		writeError(w, http.StatusServiceUnavailable, "mtg_not_installed", err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "mtg_failed", err.Error())
}
