// Path-B transparent-forwarder settings surface.
//
// GET /api/v1/settings/frontdoor/proxy
//   Return the current values of all path-B keys plus a computed
//   "server_ip_effective" (the value the daemon would actually use for
//   spoofed answers on next boot: explicit override wins, else the
//   auto-discovered egress IP).
//
// POST /api/v1/settings/frontdoor/proxy
//   Write the whole config in one shot. All fields are optional; a
//   missing field leaves the existing value untouched. A single write
//   never crosses the "port re-shuffle" boundary silently — enabling
//   SNI/QUIC forward requires a daemon restart to move the panel
//   secondary bind off :443, and the response includes
//   restart_required=true so the UI can show the operator a banner.
//
// POST /api/v1/settings/frontdoor/proxy/preflight
//   Non-mutating probe: verifies the egress IP is discoverable and
//   returns what the daemon would use. Useful to run before enabling
//   the toggle so the operator learns "server_ip could not be
//   auto-discovered" without first flipping the switch.
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
	resp := s.readProxySettings(r.Context())
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

	actor := actorFromCtx(r)
	ctx := r.Context()

	prev := s.readProxySettings(ctx)

	if req.SpoofEnabled != nil {
		_ = s.Settings.SetBool(ctx, settings.KeyFrontdoorSpoofEnabled, *req.SpoofEnabled, actor)
	}
	if req.SpoofScope != nil {
		_ = s.Settings.SetString(ctx, settings.KeyFrontdoorSpoofScope, *req.SpoofScope, actor)
	}
	if req.SpoofServerIP != nil {
		_ = s.Settings.SetString(ctx, settings.KeyFrontdoorSpoofServerIP, *req.SpoofServerIP, actor)
	}
	if req.SpoofAllowCIDR != nil {
		_ = s.Settings.SetString(ctx, settings.KeyFrontdoorSpoofAllowCIDR, *req.SpoofAllowCIDR, actor)
	}
	if req.SNIForwardEnabled != nil {
		_ = s.Settings.SetBool(ctx, settings.KeyFrontdoorSNIForwardEnabled, *req.SNIForwardEnabled, actor)
	}
	if req.QUICForwardEnabled != nil {
		_ = s.Settings.SetBool(ctx, settings.KeyFrontdoorQUICForwardEnabled, *req.QUICForwardEnabled, actor)
	}
	if req.PanelBackendTCP != nil {
		_ = s.Settings.SetString(ctx, settings.KeyFrontdoorPanelBackendTCP, *req.PanelBackendTCP, actor)
	}
	if req.PanelBackendUDP != nil {
		_ = s.Settings.SetString(ctx, settings.KeyFrontdoorPanelBackendUDP, *req.PanelBackendUDP, actor)
	}

	// Hot-apply spoof to the running resolver so operators don't have
	// to restart to change spoof toggle / scope / IP. sni_forward and
	// quic_forward flags require a restart (they own :443 sockets
	// that are decided at boot) — flag that in the response.
	if s.LiveResolver != nil {
		_ = applyLiveSpoofPolicy(ctx, s.Settings, s.LiveResolver)
	}

	resp := s.readProxySettings(ctx)
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

func (s *Server) readProxySettings(ctx context.Context) proxySettingsResponse {
	spoofOn, _ := s.Settings.GetBool(ctx, settings.KeyFrontdoorSpoofEnabled)
	scope, _ := s.Settings.GetString(ctx, settings.KeyFrontdoorSpoofScope)
	if scope == "" {
		scope = string(resolver.SpoofScopeAll)
	}
	serverIP, _ := s.Settings.GetString(ctx, settings.KeyFrontdoorSpoofServerIP)
	allowCIDR, _ := s.Settings.GetString(ctx, settings.KeyFrontdoorSpoofAllowCIDR)
	sniOn, _ := s.Settings.GetBool(ctx, settings.KeyFrontdoorSNIForwardEnabled)
	quicOn, _ := s.Settings.GetBool(ctx, settings.KeyFrontdoorQUICForwardEnabled)
	tcpBackend, _ := s.Settings.GetString(ctx, settings.KeyFrontdoorPanelBackendTCP)
	if tcpBackend == "" {
		tcpBackend = "127.0.0.1:8444"
	}
	udpBackend, _ := s.Settings.GetString(ctx, settings.KeyFrontdoorPanelBackendUDP)
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
	}
}

// validateProxySettings enforces field-level constraints before any
// setting is written. Failing here means no partial writes happen.
func validateProxySettings(req proxySettingsDoc) error {
	if req.SpoofScope != nil {
		s := strings.ToLower(strings.TrimSpace(*req.SpoofScope))
		if s != string(resolver.SpoofScopeAll) && s != string(resolver.SpoofScopePrivateOnly) {
			return fmt.Errorf("spoof_scope must be %q or %q", resolver.SpoofScopeAll, resolver.SpoofScopePrivateOnly)
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

// applyLiveSpoofPolicy reads the current spoof settings and publishes
// them to the running resolver via SetSpoofPolicy. Called on every
// POST so operators can flip spoof scope / IP / CIDR without a
// restart. Errors are returned but callers typically log-and-continue
// since the write to the DB is what persists across boots.
func applyLiveSpoofPolicy(ctx context.Context, sset *settings.Store, res *resolver.Resolver) error {
	on, _ := sset.GetBool(ctx, settings.KeyFrontdoorSpoofEnabled)
	if !on {
		res.SetSpoofPolicy(nil)
		return nil
	}
	ipStr, _ := sset.GetString(ctx, settings.KeyFrontdoorSpoofServerIP)
	if ipStr == "" {
		ipStr = discoverEgressIPForAPI()
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return errors.New("spoof: server_ip not set and autodetect failed")
	}
	scopeStr, _ := sset.GetString(ctx, settings.KeyFrontdoorSpoofScope)
	scope := resolver.SpoofScopeAll
	if strings.EqualFold(scopeStr, string(resolver.SpoofScopePrivateOnly)) {
		scope = resolver.SpoofScopePrivateOnly
	}
	cidrStr, _ := sset.GetString(ctx, settings.KeyFrontdoorSpoofAllowCIDR)
	var cidrs []*net.IPNet
	if cidrStr != "" {
		cidrs = resolver.ParseCIDRs(strings.Split(cidrStr, ","))
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
	res.SetSpoofPolicy(policy)
	return nil
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
	defer conn.Close()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil || la.IP.IsUnspecified() {
		return ""
	}
	if v4 := la.IP.To4(); v4 != nil {
		return v4.String()
	}
	return la.IP.String()
}
