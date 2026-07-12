package installer_test

// This test lives in an *_test package to justify importing internal/config
// (which does not otherwise depend on internal/installer). It asserts that
// the DefaultConfigTemplate the installer writes on a fresh install is
// actually loadable and validates — the previous template contained a
// literal ${5GPN_DOMAIN:-panel.local} that never expanded (the loader's
// env-var regex requires uppercase-only names starting with A-Z_), so the
// hostname field failed validation and the daemon refused to boot.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/installer"
)

// TestDefaultConfigTemplate_LoadsAndValidates writes the installer's
// DefaultConfigTemplate to disk and runs it through config.Load — the
// same code path the daemon uses on boot. Any regression that puts a
// literal ${VAR} that the loader cannot expand, or drops a required
// field, will fail this test before it ships.
func TestDefaultConfigTemplate_LoadsAndValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(installer.DefaultConfigTemplate), 0o640); err != nil {
		t.Fatalf("write template: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load rejected default template — validate will fail on a fresh install: %v", err)
	}
	// Sanity: the fields the panel + daemon absolutely rely on must
	// carry non-zero values, not just pass structural validation.
	if cfg.Server.Domain == "" {
		t.Errorf("server.domain empty after Load")
	}
	if cfg.Server.PanelPort == 0 {
		t.Errorf("server.panel_port zero after Load")
	}
	if cfg.Server.PanelBind != "127.0.0.1" {
		t.Errorf("fresh install must be loopback-only, got panel_bind=%q", cfg.Server.PanelBind)
	}
	if cfg.Panel.SessionTTL == 0 {
		t.Errorf("panel.session_ttl zero after Load")
	}
	if cfg.Proxy.WAShim.Port == 0 {
		t.Errorf("proxy.wa_shim.port zero after Load — validate should have caught this")
	}
	if cfg.IOS.HTTPPort != 0 {
		t.Errorf("legacy plaintext iOS listener must default off, got %d", cfg.IOS.HTTPPort)
	}
}

func TestMigratedConfig_LoadsAndValidates(t *testing.T) {
	body, _ := installer.RenderNewConfig(installer.LegacyExtract{
		Domain:         "gateway.example.com",
		RemoteDNS:      "1.1.1.1 8.8.8.8",
		LocalDNS:       "223.5.5.5",
		TGToken:        "111:secret",
		TGAdminIDs:     "42 43",
		IOSProfileUUID: "abcd-uuid",
	})
	path := filepath.Join(t.TempDir(), "migrated.yaml")
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load rejected migrated config: %v\n%s", err, body)
	}
	if len(cfg.DNS.Upstreams) != 3 {
		t.Fatalf("migrated upstreams=%v", cfg.DNS.Upstreams)
	}
	if cfg.Proxy.WAShim.Listen != "127.0.0.1" {
		t.Fatalf("migrated wa_shim must remain loopback-only: %+v", cfg.Proxy.WAShim)
	}
}
