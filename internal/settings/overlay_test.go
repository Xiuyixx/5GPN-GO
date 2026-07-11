package settings

import (
	"context"
	"reflect"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// baseline returns a valid config.Config populated the way config.Load
// would leave it after reading a fully-defaulted config.yaml. Overlay
// tests start here and assert what changed.
func baseline() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Domain:    "panel.local",
			PanelBind: "0.0.0.0",
			PanelPort: 8443,
		},
		Proxy: config.ProxyConfig{
			WAShim: config.WAShimConfig{
				Listen:    "127.0.0.1",
				Port:      8447,
				Backend:   "127.0.0.1:1080",
				WAHost:    "www.apple.com",
				AllowCIDR: []string{"127.0.0.1/32"},
				MaxConn:   100,
			},
		},
	}
}

func TestOverlayNoStoreIsNoop(t *testing.T) {
	cfg := baseline()
	want := baseline()
	if err := OverlayConfig(context.Background(), nil, cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("nil store mutated cfg: got %+v want %+v", cfg, want)
	}
}

func TestOverlayEmptyStoreIsNoop(t *testing.T) {
	s := newTestStore(t)
	cfg := baseline()
	want := baseline()
	if err := OverlayConfig(context.Background(), s, cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("empty store mutated cfg: got %+v want %+v", cfg, want)
	}
}

func TestOverlayDomainWins(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyServerDomain, "panel.example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	cfg := baseline()
	if err := OverlayConfig(ctx, s, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Domain != "panel.example.com" {
		t.Errorf("Domain got %q, want panel.example.com", cfg.Server.Domain)
	}
	// Other fields untouched.
	if cfg.Server.PanelPort != 8443 {
		t.Errorf("PanelPort got %d, want 8443 (should not have changed)", cfg.Server.PanelPort)
	}
}

func TestOverlayTGBotFullSurface(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyTGBotToken, "987654321:XYZ", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJSON(ctx, KeyTGBotAdminChats, []int64{111, 222}, "admin"); err != nil {
		t.Fatal(err)
	}
	cfg := baseline()
	if err := OverlayConfig(ctx, s, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.TGBot.Token != "987654321:XYZ" {
		t.Errorf("Token got %q", cfg.TGBot.Token)
	}
	if !reflect.DeepEqual(cfg.TGBot.AdminChatIDs, []int64{111, 222}) {
		t.Errorf("AdminChatIDs got %v", cfg.TGBot.AdminChatIDs)
	}
}

func TestOverlayEmptyStringDoesNotClobber(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Explicitly write an empty string. Contract says this should NOT
	// clobber a good YAML value.
	if err := s.SetString(ctx, KeyServerDomain, "", "admin"); err != nil {
		t.Fatal(err)
	}
	cfg := baseline()
	if err := OverlayConfig(ctx, s, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Domain != "panel.local" {
		t.Errorf("empty DB string clobbered YAML: got %q, want panel.local", cfg.Server.Domain)
	}
}

func TestOverlayEmptyAdminIDsDoesNotClobber(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Seed YAML-side value.
	cfg := baseline()
	cfg.TGBot.Token = "yaml-token"
	cfg.TGBot.AdminChatIDs = []int64{99}
	// Write empty slice.
	if err := s.SetJSON(ctx, KeyTGBotAdminChats, []int64{}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := OverlayConfig(ctx, s, cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.TGBot.AdminChatIDs, []int64{99}) {
		t.Errorf("empty slice clobbered YAML: got %v, want [99]", cfg.TGBot.AdminChatIDs)
	}
}

func TestOverlayWAShim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetString(ctx, KeyWAShimBackend, "10.0.0.5:1080", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInt(ctx, KeyWAShimPort, 9999, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJSON(ctx, KeyWAShimAllowCIDR, []string{"10.0.0.0/8"}, "admin"); err != nil {
		t.Fatal(err)
	}
	cfg := baseline()
	if err := OverlayConfig(ctx, s, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy.WAShim.Backend != "10.0.0.5:1080" {
		t.Errorf("Backend got %q", cfg.Proxy.WAShim.Backend)
	}
	if cfg.Proxy.WAShim.Port != 9999 {
		t.Errorf("Port got %d", cfg.Proxy.WAShim.Port)
	}
	if !reflect.DeepEqual(cfg.Proxy.WAShim.AllowCIDR, []string{"10.0.0.0/8"}) {
		t.Errorf("AllowCIDR got %v", cfg.Proxy.WAShim.AllowCIDR)
	}
	// Untouched fields preserved.
	if cfg.Proxy.WAShim.Listen != "127.0.0.1" {
		t.Errorf("Listen wrongly changed to %q", cfg.Proxy.WAShim.Listen)
	}
	if cfg.Proxy.WAShim.MaxConn != 100 {
		t.Errorf("MaxConn wrongly changed to %d", cfg.Proxy.WAShim.MaxConn)
	}
}
