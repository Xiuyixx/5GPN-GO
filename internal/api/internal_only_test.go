// Tests for the internal-only access-gate settings surface and the
// middleware that enforces it. Each test bootstraps a fresh server so
// the panel_settings table starts empty.
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

func TestInternalOnly_GetFreshDBReturnsDefaults(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})

	rr := authGet(t, srv, "/api/v1/settings/frontdoor/internal-only", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var got internalOnlyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Enabled {
		t.Fatalf("Enabled = true on fresh DB, want false")
	}
	if got.CIDRs != access.DefaultInternalCIDRs {
		t.Fatalf("CIDRs on fresh DB = %q, want default %q", got.CIDRs, access.DefaultInternalCIDRs)
	}
}

func TestInternalOnly_PostPersistsAndReloads(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})

	body := map[string]any{"enabled": true, "cidrs": "172.22.0.0/16"}
	rr := authPost(t, srv, "/api/v1/settings/frontdoor/internal-only", token, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var got internalOnlyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled {
		t.Fatalf("Enabled = false after POST, want true")
	}
	if got.CIDRs != "172.22.0.0/16" {
		t.Fatalf("CIDRs = %q, want 172.22.0.0/16", got.CIDRs)
	}

	// Round-trip: GET must return the same shape.
	rr = authGet(t, srv, "/api/v1/settings/frontdoor/internal-only", token)
	var got2 internalOnlyResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got2)
	if got2.Enabled != got.Enabled || got2.CIDRs != got.CIDRs {
		t.Fatalf("GET after POST diverges: got %+v, want %+v", got2, got)
	}
}

func TestInternalOnly_PostRejectsBadCIDR(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})

	body := map[string]any{"cidrs": "not-a-cidr"}
	rr := authPost(t, srv, "/api/v1/settings/frontdoor/internal-only", token, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(strings.ToLower(rr.Body.String()), "cidr") {
		t.Fatalf("expected CIDR error, got %s", rr.Body.String())
	}
	// Post-condition: the store must NOT have been mutated.
	rr = authGet(t, srv, "/api/v1/settings/frontdoor/internal-only", token)
	var got internalOnlyResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Enabled {
		t.Fatalf("Enabled = true after rejected POST, want false")
	}
}

func TestInternalOnly_MiddlewareRejectsPublicIPWhenEnabled(t *testing.T) {
	// Fresh server WITH a gate wired in. Enabling the gate via the
	// public POST would race the middleware self-lockout (the POST
	// itself would come from 192.0.2.1 and get 403'd) so we set the
	// settings directly through the store and Refresh() before
	// hitting the router.
	srv, token := bootstrapAndLogin(t, Config{})
	gate, err := access.NewGate(srv.Settings)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	srv.Gate = gate
	ctx := t.Context()
	if err := srv.Settings.SetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := srv.Settings.SetString(ctx, settings.KeyFrontdoorInternalCIDRs, "172.22.0.0/16", "test"); err != nil {
		t.Fatal(err)
	}
	if err := gate.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Public IP hitting /api/v1/me → 403 internal_only_access.
	req := jsonReq(t, "GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "8.8.8.8:44321"
	rr := do(t, srv, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("public IP status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "internal_only_access") {
		t.Fatalf("body missing error code: %s", rr.Body.String())
	}

	// Private IP inside the allowlist → 200.
	req = jsonReq(t, "GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "172.22.5.5:44321"
	rr = do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("internal IP status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	// Health endpoint must stay reachable from any IP — monitoring
	// probes come from anywhere.
	req = jsonReq(t, "GET", "/api/v1/health", nil)
	req.RemoteAddr = "8.8.8.8:44321"
	rr = do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("health from public IP = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalOnly_DisabledMiddlewareIsTransparent(t *testing.T) {
	// Gate wired but toggle off — the middleware must be a pass-through
	// and the panel must reach /api/v1/me from a public IP.
	srv, token := bootstrapAndLogin(t, Config{})
	gate, err := access.NewGate(srv.Settings)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	srv.Gate = gate

	req := jsonReq(t, "GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "8.8.8.8:44321"
	rr := do(t, srv, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disabled gate rejected public IP: %d %s", rr.Code, rr.Body.String())
	}
}
