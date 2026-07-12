// Tests for the rewired Telegram MTProxy settings surface. The handler
// depends on the MTG interface (see mtproxy_settings.go); this test
// injects a fakeMTG that captures method calls so we can assert the
// shape and the failure paths without shelling out to systemctl.
//
// Coverage:
//   - GET when mtg is not installed → 200 with service_status=not-installed
//   - GET when mtg is active         → 200 with enabled=true + decoded domain
//   - POST enable without secret      → 400 secret_not_configured
//   - POST generate-secret + enable   → both succeed, secret is one-shot
//   - Legacy panel_settings.mtproxy.* rows are ignored (no read from Store)
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/mtgctl"
)

// fakeMTG stubs the MTG interface. Every call is recorded so the test
// can assert exact argv shape. Fields with Err set force the matching
// method to return that error.
type fakeMTG struct {
	mu sync.Mutex

	installed      bool // when false, methods return mtgctl.ErrNotInstalled
	active         bool // reported by IsActive / Status
	unitListen     string
	unitSecret     string
	generateReturn string
	generateErr    error

	statusCalls    int
	isActiveCalls  int
	enableCalls    int
	disableCalls   int
	restartCalls   int
	readUnitCalls  int
	writeUnitCalls int
	generateCalls  int

	lastGenerateDomain string
	lastWriteListen    string
	lastWriteSecret    string
}

func (f *fakeMTG) Status(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if !f.installed {
		return "not-installed", mtgctl.ErrNotInstalled
	}
	if f.active {
		return "active", nil
	}
	return "inactive", nil
}

func (f *fakeMTG) IsActive(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.isActiveCalls++
	if !f.installed {
		return false, mtgctl.ErrNotInstalled
	}
	return f.active, nil
}

func (f *fakeMTG) Enable(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enableCalls++
	if !f.installed {
		return mtgctl.ErrNotInstalled
	}
	f.active = true
	return nil
}

func (f *fakeMTG) Disable(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableCalls++
	if !f.installed {
		return mtgctl.ErrNotInstalled
	}
	f.active = false
	return nil
}

func (f *fakeMTG) Restart(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartCalls++
	if !f.installed {
		return mtgctl.ErrNotInstalled
	}
	return nil
}

func (f *fakeMTG) GenerateSecret(_ context.Context, domain string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generateCalls++
	f.lastGenerateDomain = domain
	if f.generateErr != nil {
		return "", f.generateErr
	}
	if f.generateReturn != "" {
		return f.generateReturn, nil
	}
	// Deterministic default: an ee-prefixed base64 secret whose
	// tail decodes to the requested domain, so DecodeFrontingDomain
	// on the returned value round-trips.
	return synthEESecret(domain), nil
}

func (f *fakeMTG) WriteUnit(_ context.Context, listen, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeUnitCalls++
	f.lastWriteListen = listen
	f.lastWriteSecret = secret
	f.unitListen = listen
	f.unitSecret = secret
	return nil
}

func (f *fakeMTG) ReadUnit(context.Context) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readUnitCalls++
	return f.unitListen, f.unitSecret, nil
}

// synthEESecret builds a synthetic ee-prefix base64-url secret for
// tests. It concatenates 0xee + 16 zero bytes + domain and encodes.
// DecodeFrontingDomain(synthEESecret(d)) == d.
func synthEESecret(domain string) string {
	buf := make([]byte, 17+len(domain))
	buf[0] = 0xee
	copy(buf[17:], []byte(domain))
	// base64.RawURLEncoding matches the mtg output shape.
	// Use the same helper mtgctl uses internally by round-tripping
	// through the package's decoder later — here we just b64.
	return rawURLEncode(buf)
}

// rawURLEncode is a small inline base64-url no-pad encoder so this
// test file doesn't need to import encoding/base64 alongside mtgctl.
func rawURLEncode(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out strings.Builder
	n := len(b)
	i := 0
	for ; i+3 <= n; i += 3 {
		v := uint(b[i])<<16 | uint(b[i+1])<<8 | uint(b[i+2])
		out.WriteByte(alphabet[(v>>18)&0x3f])
		out.WriteByte(alphabet[(v>>12)&0x3f])
		out.WriteByte(alphabet[(v>>6)&0x3f])
		out.WriteByte(alphabet[v&0x3f])
	}
	switch n - i {
	case 1:
		v := uint(b[i]) << 16
		out.WriteByte(alphabet[(v>>18)&0x3f])
		out.WriteByte(alphabet[(v>>12)&0x3f])
	case 2:
		v := uint(b[i])<<16 | uint(b[i+1])<<8
		out.WriteByte(alphabet[(v>>18)&0x3f])
		out.WriteByte(alphabet[(v>>12)&0x3f])
		out.WriteByte(alphabet[(v>>6)&0x3f])
	}
	return out.String()
}

// bootstrapWithMTG wires a fakeMTG onto a fresh test server so the
// mtproxy handlers have a controller to talk to.
func bootstrapWithMTG(t *testing.T, mtg MTG) (*Server, string) {
	t.Helper()
	srv, token := bootstrapAndLogin(t, Config{MTG: mtg})
	return srv, token
}

func TestMTProxySettings_GetNotInstalled(t *testing.T) {
	f := &fakeMTG{installed: false}
	srv, token := bootstrapWithMTG(t, f)

	rr := authGet(t, srv, "/api/v1/settings/mtproxy", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var got mtproxySettingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Enabled {
		t.Fatalf("Enabled = true when not installed, want false")
	}
	if got.ServiceStatus != "not-installed" {
		t.Fatalf("ServiceStatus = %q, want not-installed", got.ServiceStatus)
	}
	if got.Listen != mtgctl.DefaultListen {
		t.Fatalf("Listen = %q, want default %q", got.Listen, mtgctl.DefaultListen)
	}
	if got.SecretConfigured {
		t.Fatalf("SecretConfigured = true when not installed, want false")
	}
}

func TestMTProxySettings_GetWhenMTGUnwiredReturns503(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{}) // no MTG

	rr := authGet(t, srv, "/api/v1/settings/mtproxy", token)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "mtg_not_wired") {
		t.Fatalf("expected mtg_not_wired error: %s", rr.Body.String())
	}
}

func TestMTProxySettings_GetActiveReportsDecodedDomain(t *testing.T) {
	// Preload the fake with a known unit + ee-secret whose tail
	// decodes to "www.cloudflare.com".
	const knownSecret = "7j85IUlh_jST-sGwIJ3FHRt3d3cuY2xvdWRmbGFyZS5jb20"
	f := &fakeMTG{
		installed:  true,
		active:     true,
		unitListen: "0.0.0.0:2443",
		unitSecret: knownSecret,
	}
	srv, token := bootstrapWithMTG(t, f)

	rr := authGet(t, srv, "/api/v1/settings/mtproxy", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rr.Code, rr.Body.String())
	}
	var got mtproxySettingsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)

	if !got.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if got.ServiceStatus != "active" {
		t.Fatalf("ServiceStatus = %q, want active", got.ServiceStatus)
	}
	if got.Listen != "0.0.0.0:2443" {
		t.Fatalf("Listen = %q", got.Listen)
	}
	if !got.SecretConfigured {
		t.Fatalf("SecretConfigured = false, want true")
	}
	if got.FrontingDomain != "www.cloudflare.com" {
		t.Fatalf("FrontingDomain = %q, want www.cloudflare.com", got.FrontingDomain)
	}
	// The GET must never leak the raw secret — the response shape
	// intentionally omits any Secret field, so we can assert on the
	// serialised body.
	if strings.Contains(rr.Body.String(), knownSecret) {
		t.Fatalf("GET leaked the raw secret")
	}
}

func TestMTProxySettings_PostEnableWithoutSecretReturns400(t *testing.T) {
	f := &fakeMTG{installed: true, active: false, unitSecret: ""}
	srv, token := bootstrapWithMTG(t, f)

	rr := authPost(t, srv, "/api/v1/settings/mtproxy", token, map[string]any{
		"enabled": true,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "secret_not_configured") {
		t.Fatalf("expected secret_not_configured, got %s", rr.Body.String())
	}
	if f.enableCalls != 0 {
		t.Fatalf("Enable was called %d times, want 0 when secret missing", f.enableCalls)
	}
}

func TestMTProxySettings_GenerateThenEnableFlow(t *testing.T) {
	f := &fakeMTG{installed: true}
	srv, token := bootstrapWithMTG(t, f)

	// 1) Generate a secret.
	rr := authPost(t, srv, "/api/v1/settings/mtproxy/generate-secret", token, map[string]any{
		"fronting_domain": "www.cloudflare.com",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("generate status = %d: %s", rr.Code, rr.Body.String())
	}
	var gen mtproxyGenerateSecretResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &gen); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gen.OK {
		t.Fatalf("ok = false")
	}
	if gen.Secret == "" {
		t.Fatalf("Secret empty — generate should return the one-shot value")
	}
	if !strings.HasPrefix(gen.ConnectLink, "tg://proxy?") {
		t.Fatalf("ConnectLink = %q, want tg:// scheme", gen.ConnectLink)
	}
	if !strings.Contains(gen.ConnectLink, "secret="+gen.Secret) {
		t.Fatalf("ConnectLink missing secret= param: %q", gen.ConnectLink)
	}
	if f.lastGenerateDomain != "www.cloudflare.com" {
		t.Fatalf("mtg generate called with domain=%q, want www.cloudflare.com", f.lastGenerateDomain)
	}
	if f.writeUnitCalls != 1 {
		t.Fatalf("WriteUnit called %d times, want 1", f.writeUnitCalls)
	}
	if f.lastWriteListen != mtgctl.DefaultListen {
		t.Fatalf("WriteUnit listen = %q, want default", f.lastWriteListen)
	}
	if f.lastWriteSecret != gen.Secret {
		t.Fatalf("WriteUnit secret mismatch: unit=%q, response=%q", f.lastWriteSecret, gen.Secret)
	}
	// Service was inactive, so Restart must NOT have fired.
	if f.restartCalls != 0 {
		t.Fatalf("Restart called %d times during generate-while-inactive, want 0", f.restartCalls)
	}

	// 2) Enable the service.
	rr = authPost(t, srv, "/api/v1/settings/mtproxy", token, map[string]any{
		"enabled": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("enable status = %d: %s", rr.Code, rr.Body.String())
	}
	if f.enableCalls != 1 {
		t.Fatalf("Enable called %d times, want 1", f.enableCalls)
	}

	// 3) Follow-up GET reflects the new state and does NOT leak the
	//    secret. FrontingDomain round-trips through DecodeFrontingDomain.
	rr = authGet(t, srv, "/api/v1/settings/mtproxy", token)
	var view mtproxySettingsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &view)
	if !view.Enabled || view.ServiceStatus != "active" {
		t.Fatalf("post-enable snapshot wrong: %+v", view)
	}
	if !view.SecretConfigured {
		t.Fatalf("SecretConfigured = false after generate+enable")
	}
	if view.FrontingDomain != "www.cloudflare.com" {
		t.Fatalf("FrontingDomain = %q, want www.cloudflare.com", view.FrontingDomain)
	}
	if strings.Contains(rr.Body.String(), gen.Secret) {
		t.Fatalf("GET leaked the raw secret after generate")
	}
}

func TestMTProxySettings_PostDisableCallsDisable(t *testing.T) {
	f := &fakeMTG{installed: true, active: true, unitSecret: synthEESecret("www.example.com")}
	srv, token := bootstrapWithMTG(t, f)

	rr := authPost(t, srv, "/api/v1/settings/mtproxy", token, map[string]any{
		"enabled": false,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if f.disableCalls != 1 {
		t.Fatalf("Disable called %d times, want 1", f.disableCalls)
	}
}

func TestMTProxySettings_PostFrontingDomainRotatesSecret(t *testing.T) {
	// Start with an active service already fronting www.cloudflare.com.
	f := &fakeMTG{
		installed:      true,
		active:         true,
		unitListen:     "0.0.0.0:2443",
		unitSecret:     synthEESecret("www.cloudflare.com"),
		generateReturn: synthEESecret("www.example.com"),
	}
	srv, token := bootstrapWithMTG(t, f)

	rr := authPost(t, srv, "/api/v1/settings/mtproxy", token, map[string]any{
		"fronting_domain": "www.example.com",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if f.generateCalls != 1 {
		t.Fatalf("GenerateSecret called %d times, want 1", f.generateCalls)
	}
	if f.lastGenerateDomain != "www.example.com" {
		t.Fatalf("GenerateSecret domain = %q", f.lastGenerateDomain)
	}
	if f.writeUnitCalls != 1 {
		t.Fatalf("WriteUnit called %d times, want 1", f.writeUnitCalls)
	}
	// Service was active → must restart to pick up the new secret.
	if f.restartCalls != 1 {
		t.Fatalf("Restart called %d times, want 1 (service was active)", f.restartCalls)
	}
}

func TestMTProxySettings_PostFrontingDomainSameNoop(t *testing.T) {
	// Sending the identical fronting domain must NOT regenerate.
	f := &fakeMTG{
		installed:  true,
		active:     true,
		unitListen: "0.0.0.0:2443",
		unitSecret: synthEESecret("www.cloudflare.com"),
	}
	srv, token := bootstrapWithMTG(t, f)

	rr := authPost(t, srv, "/api/v1/settings/mtproxy", token, map[string]any{
		"fronting_domain": "www.cloudflare.com",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if f.generateCalls != 0 {
		t.Fatalf("GenerateSecret called %d times on same-domain POST, want 0", f.generateCalls)
	}
	if f.writeUnitCalls != 0 {
		t.Fatalf("WriteUnit called %d times on same-domain POST, want 0", f.writeUnitCalls)
	}
}

func TestMTProxySettings_LegacyDBKeysAreIgnored(t *testing.T) {
	// Set the deprecated panel_settings.mtproxy.* rows and confirm the
	// GET still reports the mtg service state, not the DB values.
	f := &fakeMTG{
		installed:  true,
		active:     false,
		unitListen: "0.0.0.0:2443",
		unitSecret: synthEESecret("www.cloudflare.com"),
	}
	srv, token := bootstrapWithMTG(t, f)

	// Poke legacy keys directly through the Settings store.
	ctx := context.Background()
	_ = srv.Settings.SetBool(ctx, "mtproxy.enabled", true, "test")
	_ = srv.Settings.SetString(ctx, "mtproxy.listen", "0.0.0.0:9999", "test")
	_ = srv.Settings.SetString(ctx, "mtproxy.secret", "dead"+strings.Repeat("beef", 7), "test")

	rr := authGet(t, srv, "/api/v1/settings/mtproxy", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rr.Code, rr.Body.String())
	}
	var got mtproxySettingsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)

	// Enabled must reflect the systemd state (inactive), not the DB flag.
	if got.Enabled {
		t.Fatalf("Enabled reflected legacy DB flag; want systemd state (inactive)")
	}
	// Listen must reflect the unit file, not the DB row.
	if got.Listen != "0.0.0.0:2443" {
		t.Fatalf("Listen = %q, want unit value 0.0.0.0:2443", got.Listen)
	}
}
