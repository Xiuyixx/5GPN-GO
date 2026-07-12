package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
	"github.com/Xiuyixx/5GPN-Go/internal/tgbot"
)

func TestPanelSettingsValidationDoesNotPartiallyCommit(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	ctx := context.Background()
	if err := srv.Settings.SetString(ctx, settings.KeyServerDomain, "old.example", "test"); err != nil {
		t.Fatal(err)
	}

	rr := authPost(t, srv, "/api/v1/settings/panel", token, map[string]any{
		"server": map[string]any{"domain": "new.example", "panel_port": 70000},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rr.Code, rr.Body.String())
	}
	domain, err := srv.Settings.GetString(ctx, settings.KeyServerDomain)
	if err != nil {
		t.Fatal(err)
	}
	if domain != "old.example" {
		t.Fatalf("earlier field partially committed: %q", domain)
	}
}

func TestInternalOnlyValidationDoesNotPartiallyCommit(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := authPost(t, srv, "/api/v1/settings/frontdoor/internal-only", token, map[string]any{
		"enabled": true,
		"cidrs":   "not-a-cidr",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rr.Code, rr.Body.String())
	}
	enabled, err := srv.Settings.GetBool(context.Background(), settings.KeyFrontdoorInternalOnlyEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("enabled field partially committed before invalid CIDR")
	}
}

func TestFrontdoorProxyReadFailureIsNotReportedAsSuccess(t *testing.T) {
	srv, _ := bootstrapAndLogin(t, Config{})
	if err := srv.DB.Close(); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/frontdoor/proxy", nil)
	srv.handleGetFrontdoorProxy(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 after settings DB failure: %s", rr.Code, rr.Body.String())
	}
}

func TestFrontdoorSpoofScopeCanonicalizedBeforePersistenceAndPublish(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	live := resolver.NewResolver(&resolver.Store{}, resolver.NewUpstream(), nil)
	t.Cleanup(live.Upstream.Close)
	srv.LiveResolver = live

	rr := authPost(t, srv, "/api/v1/settings/frontdoor/proxy", token, map[string]any{
		"spoof_enabled":   true,
		"spoof_scope":     "  PRIVATE_ONLY  ",
		"spoof_server_ip": "203.0.113.10",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[proxySettingsResponse](t, rr)
	if resp.SpoofScope != string(resolver.SpoofScopePrivateOnly) {
		t.Fatalf("response scope=%q", resp.SpoofScope)
	}
	stored, err := srv.Settings.GetString(context.Background(), settings.KeyFrontdoorSpoofScope)
	if err != nil {
		t.Fatal(err)
	}
	if stored != string(resolver.SpoofScopePrivateOnly) {
		t.Fatalf("stored scope=%q", stored)
	}
	policy := live.Spoof.Load()
	if policy == nil || policy.Scope != resolver.SpoofScopePrivateOnly {
		t.Fatalf("live policy=%+v", policy)
	}
}

func TestTGBotDisablePersistsAcrossRestart(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	srv.TGBot = tgbot.NewManager(tgbot.ManagerConfig{})
	if err := srv.Settings.SetMany(t.Context(), map[string]any{
		settings.KeyTGBotToken:      "old-token",
		settings.KeyTGBotAdminChats: []int64{7},
	}, "test"); err != nil {
		t.Fatal(err)
	}
	rr := authPost(t, srv, "/api/v1/settings/tgbot", token, map[string]any{
		"token": "", "admin_chat_ids": []int64{},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rr.Code, rr.Body.String())
	}
	storedToken, ids, err := effectiveTGBot(t.Context(), srv.Settings, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if storedToken != "" || len(ids) != 0 {
		t.Fatalf("disabled bot would return after restart: token=%q ids=%v", storedToken, ids)
	}
}

func TestFrontdoorSpoofScopeCanonicalizesHistoricalStoredValue(t *testing.T) {
	srv, _ := bootstrapAndLogin(t, Config{})
	if err := srv.Settings.SetString(context.Background(), settings.KeyFrontdoorSpoofScope,
		"  PRIVATE_ONLY  ", "test"); err != nil {
		t.Fatal(err)
	}
	got, err := srv.readProxySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.SpoofScope != string(resolver.SpoofScopePrivateOnly) {
		t.Fatalf("scope=%q", got.SpoofScope)
	}
	policy, err := spoofPolicyFromSettings(proxySettingsResponse{
		SpoofEnabled:      true,
		SpoofScope:        "  PRIVATE_ONLY  ",
		ServerIPEffective: "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Scope != resolver.SpoofScopePrivateOnly {
		t.Fatalf("policy scope=%q", policy.Scope)
	}
}
