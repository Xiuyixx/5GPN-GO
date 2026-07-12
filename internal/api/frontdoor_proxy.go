// Path-B transparent-forwarder settings surface.
//
// GET /api/v1/settings/frontdoor/proxy
//
//	Return the current values of all path-B keys plus a computed
//	"server_ip_effective" (the value the daemon would actually use for
//	spoofed answers on next boot: explicit override wins, else the
//	auto-discovered egress IP).
//
// POST /api/v1/settings/frontdoor/proxy
//
//	Write the whole config in one shot. All fields are optional; a
//	missing field leaves the existing value untouched. A single write
//	never crosses the "port re-shuffle" boundary silently — enabling
//	SNI/QUIC forward requires a daemon restart to move the panel
//	secondary bind off :443, and the response includes
//	restart_required=true so the UI can show the operator a banner.
//
// POST /api/v1/settings/frontdoor/proxy/preflight
//
//	Non-mutating probe: verifies the egress IP is discoverable and
//	returns what the daemon would use. Useful to run before enabling
//	the toggle so the operator learns "server_ip could not be
//	auto-discovered" without first flipping the switch.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// proxySettingsDoc is the on-the-wire shape for the GET/POST body.
// Pointer fields let POST distinguish "not sent" from "set to zero".
type proxySettingsDoc struct {
	SpoofEnabled       *bool   `json:"spoof_enabled,omitempty"`
	SpoofScope         *string `json:"spoof_scope,omitempty"`
	SpoofServerIP      *string `json:"spoof_server_ip,omitempty"`
	SpoofAllowCIDR     *string `json:"spoof_allow_cidr,omitempty"`
	SNIForwardEnabled  *bool   `json:"sni_forward_enabled,omitempty"`
	QUICForwardEnabled *bool   `json:"quic_forward_enabled,omitempty"`
	PanelBackendTCP    *string `json:"panel_backend_tcp,omitempty"`
	PanelBackendUDP    *string `json:"panel_backend_udp,omitempty"`
}

// proxySettingsResponse extends the doc with computed fields the panel
// UI needs to render current state without a second round-trip.
type proxySettingsResponse struct {
	SpoofEnabled       bool   `json:"spoof_enabled"`
	SpoofScope         string `json:"spoof_scope"`
	SpoofServerIP      string `json:"spoof_server_ip"`
	SpoofAllowCIDR     string `json:"spoof_allow_cidr"`
	SNIForwardEnabled  bool   `json:"sni_forward_enabled"`
	QUICForwardEnabled bool   `json:"quic_forward_enabled"`
	PanelBackendTCP    string `json:"panel_backend_tcp"`
	PanelBackendUDP    string `json:"panel_backend_udp"`

	// ServerIPEffective is the IP the daemon would actually spoof
	// answers to on next boot: SpoofServerIP if non-empty, else the
	// autodetected egress IP.
	ServerIPEffective string `json:"server_ip_effective"`
	// ServerIPAutodetected is what discoverEgressIP returned when
	// this handler ran. Useful for the UI to show "using route-table
	// default: 177.0.143.27" as a hint under the input.
	ServerIPAutodetected string `json:"server_ip_autodetected"`
	// RestartRequired flags that a change to sni/quic forward flags
	// won't take effect until the daemon is restarted (the port
	// re-shuffle happens at boot).
	RestartRequired bool `json:"restart_required,omitempty"`
}

// proxyPreflightResponse reports on the auto-discovery path so the
// UI can show a green/red indicator next to the "Server IP (auto)"
// field before the operator enables the toggle.
type proxyPreflightResponse struct {
	OK                   bool      `json:"ok"`
	ServerIPAutodetected string    `json:"server_ip_autodetected,omitempty"`
	CheckedAt            time.Time `json:"checked_at"`
	Error                string    `json:"error,omitempty"`
}

// handleGetFrontdoorProxy returns the current path-B settings.
func (s *Server) handleGetFrontdoorProxy(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings store not wired")
		return
	}
	resp, err := s.readProxySettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateFrontdoorProxy writes the settings and returns the new
// effective state plus restart_required.
func (s *Server) handleUpdateFrontdoorProxy(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings store not wired")
		return
	}
	var req proxySettingsDoc
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := validateProxySettings(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	actor := actorFromCtx(r)
	ctx := r.Context()

	prev, err := s.readProxySettings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	resp := prev
	values := map[string]any{}
	applyBool := func(field *bool, key string, dst *bool) {
		if field != nil {
			*dst = *field
			values[key] = *field
		}
	}
	applyString := func(field *string, key string, dst *string) {
		if field != nil {
			*dst = *field
			values[key] = *field
		}
	}
	applyBool(req.SpoofEnabled, settings.KeyFrontdoorSpoofEnabled, &resp.SpoofEnabled)
	if req.SpoofScope != nil {
		// validateProxySettings already proved this succeeds. Persist the
		// canonical value so every later reader observes the same semantics.
		canonical, _ := canonicalSpoofScope(*req.SpoofScope)
		resp.SpoofScope = canonical
		values[settings.KeyFrontdoorSpoofScope] = canonical
	}
	applyString(req.SpoofServerIP, settings.KeyFrontdoorSpoofServerIP, &resp.SpoofServerIP)
	applyString(req.SpoofAllowCIDR, settings.KeyFrontdoorSpoofAllowCIDR, &resp.SpoofAllowCIDR)
	applyBool(req.SNIForwardEnabled, settings.KeyFrontdoorSNIForwardEnabled, &resp.SNIForwardEnabled)
	applyBool(req.QUICForwardEnabled, settings.KeyFrontdoorQUICForwardEnabled, &resp.QUICForwardEnabled)
	applyString(req.PanelBackendTCP, settings.KeyFrontdoorPanelBackendTCP, &resp.PanelBackendTCP)
	applyString(req.PanelBackendUDP, settings.KeyFrontdoorPanelBackendUDP, &resp.PanelBackendUDP)
	resp.ServerIPEffective = resp.SpoofServerIP
	if resp.ServerIPEffective == "" {
		resp.ServerIPEffective = resp.ServerIPAutodetected
	}
	policy, err := spoofPolicyFromSettings(resp)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	if err := s.Settings.SetMany(ctx, values, actor); err != nil {
		writeError(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	if s.LiveResolver != nil {
		s.LiveResolver.SetSpoofPolicy(policy)
	}
	if (req.SNIForwardEnabled != nil && *req.SNIForwardEnabled != prev.SNIForwardEnabled) ||
		(req.QUICForwardEnabled != nil && *req.QUICForwardEnabled != prev.QUICForwardEnabled) ||
		(req.PanelBackendTCP != nil && *req.PanelBackendTCP != prev.PanelBackendTCP) ||
		(req.PanelBackendUDP != nil && *req.PanelBackendUDP != prev.PanelBackendUDP) {
		resp.RestartRequired = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleFrontdoorProxyPreflight probes the auto-discovery path so the
// UI can pre-flight before the operator commits.
func (s *Server) handleFrontdoorProxyPreflight(w http.ResponseWriter, r *http.Request) {
	ip := discoverEgressIPForAPI()
	res := proxyPreflightResponse{CheckedAt: time.Now()}
	if ip == "" {
		res.OK = false
		res.Error = "auto-discovery failed: could not determine egress IP from routing table"
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.OK = true
	res.ServerIPAutodetected = ip
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) readProxySettings(ctx context.Context) (proxySettingsResponse, error) {
	spoofOn, err := s.Settings.GetBool(ctx, settings.KeyFrontdoorSpoofEnabled)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	scope, err := s.Settings.GetString(ctx, settings.KeyFrontdoorSpoofScope)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	if scope == "" {
		scope = string(resolver.SpoofScopeAll)
	}
	scope, err = canonicalSpoofScope(scope)
	if err != nil {
		return proxySettingsResponse{}, fmt.Errorf("stored spoof_scope: %w", err)
	}
	serverIP, err := s.Settings.GetString(ctx, settings.KeyFrontdoorSpoofServerIP)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	allowCIDR, err := s.Settings.GetString(ctx, settings.KeyFrontdoorSpoofAllowCIDR)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	sniOn, err := s.Settings.GetBool(ctx, settings.KeyFrontdoorSNIForwardEnabled)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	quicOn, err := s.Settings.GetBool(ctx, settings.KeyFrontdoorQUICForwardEnabled)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	tcpBackend, err := s.Settings.GetString(ctx, settings.KeyFrontdoorPanelBackendTCP)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	if tcpBackend == "" {
		tcpBackend = "127.0.0.1:8444"
	}
	udpBackend, err := s.Settings.GetString(ctx, settings.KeyFrontdoorPanelBackendUDP)
	if err != nil {
		return proxySettingsResponse{}, err
	}
	if udpBackend == "" {
		udpBackend = "127.0.0.1:8445"
	}

	auto := discoverEgressIPForAPI()
	effective := serverIP
	if effective == "" {
		effective = auto
	}
	return proxySettingsResponse{
		SpoofEnabled:         spoofOn,
		SpoofScope:           scope,
		SpoofServerIP:        serverIP,
		SpoofAllowCIDR:       allowCIDR,
		SNIForwardEnabled:    sniOn,
		QUICForwardEnabled:   quicOn,
		PanelBackendTCP:      tcpBackend,
		PanelBackendUDP:      udpBackend,
		ServerIPEffective:    effective,
		ServerIPAutodetected: auto,
	}, nil
}

// validateProxySettings enforces field-level constraints before any
// setting is written. Failing here means no partial writes happen.
func validateProxySettings(req proxySettingsDoc) error {
	if req.SpoofScope != nil {
		if _, err := canonicalSpoofScope(*req.SpoofScope); err != nil {
			return err
		}
	}
	if req.SpoofServerIP != nil && *req.SpoofServerIP != "" {
		if net.ParseIP(strings.TrimSpace(*req.SpoofServerIP)) == nil {
			return fmt.Errorf("spoof_server_ip is not a valid IP")
		}
	}
	if req.SpoofAllowCIDR != nil && *req.SpoofAllowCIDR != "" {
		for _, c := range strings.Split(*req.SpoofAllowCIDR, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if _, _, err := net.ParseCIDR(c); err != nil {
				return fmt.Errorf("spoof_allow_cidr: %q is not a valid CIDR: %v", c, err)
			}
		}
	}
	if req.PanelBackendTCP != nil && *req.PanelBackendTCP != "" {
		if _, _, err := net.SplitHostPort(*req.PanelBackendTCP); err != nil {
			return fmt.Errorf("panel_backend_tcp must be host:port: %v", err)
		}
	}
	if req.PanelBackendUDP != nil && *req.PanelBackendUDP != "" {
		if _, _, err := net.SplitHostPort(*req.PanelBackendUDP); err != nil {
			return fmt.Errorf("panel_backend_udp must be host:port: %v", err)
		}
	}
	return nil
}

// canonicalSpoofScope is the single API-side normalization boundary for the
// setting. Accepted casing and surrounding whitespace never reach SQLite or
// the live resolver; invalid values fail closed instead of widening to "all".
func canonicalSpoofScope(raw string) (string, error) {
	scope := strings.ToLower(strings.TrimSpace(raw))
	switch scope {
	case string(resolver.SpoofScopeAll), string(resolver.SpoofScopePrivateOnly):
		return scope, nil
	default:
		return "", fmt.Errorf("spoof_scope must be %q or %q", resolver.SpoofScopeAll, resolver.SpoofScopePrivateOnly)
	}
}

func spoofPolicyFromSettings(cfg proxySettingsResponse) (*resolver.SpoofPolicy, error) {
	if !cfg.SpoofEnabled {
		return nil, nil
	}
	ip := net.ParseIP(strings.TrimSpace(cfg.ServerIPEffective))
	if ip == nil {
		return nil, errors.New("spoof: server_ip not set and autodetect failed")
	}
	rawScope := cfg.SpoofScope
	if strings.TrimSpace(rawScope) == "" {
		rawScope = string(resolver.SpoofScopeAll)
	}
	canonical, err := canonicalSpoofScope(rawScope)
	if err != nil {
		return nil, err
	}
	scope := resolver.SpoofScope(canonical)
	var cidrs []*net.IPNet
	if cfg.SpoofAllowCIDR != "" {
		for _, raw := range strings.Split(cfg.SpoofAllowCIDR, ",") {
			_, cidr, err := net.ParseCIDR(strings.TrimSpace(raw))
			if err != nil {
				return nil, fmt.Errorf("spoof_allow_cidr %q: %w", raw, err)
			}
			cidrs = append(cidrs, cidr)
		}
	}
	policy := &resolver.SpoofPolicy{
		Scope:     scope,
		AllowCIDR: cidrs,
		TTL:       60,
	}
	if v4 := ip.To4(); v4 != nil {
		policy.ServerIP4 = v4
	} else {
		policy.ServerIP6 = ip
	}
	return policy, nil
}

// discoverEgressIPForAPI is the api-package's clone of the cmd/5gpn
// helper of the same name. Duplicating it here (~10 lines) is
// preferable to a cross-package import from cmd/5gpn/main.go, which
// Go doesn't allow anyway.
func discoverEgressIPForAPI() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil || la.IP.IsUnspecified() {
		return ""
	}
	if v4 := la.IP.To4(); v4 != nil {
		return v4.String()
	}
	return la.IP.String()
}
