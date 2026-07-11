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
	if cfg.Panel.SessionTTL == 0 {
		t.Errorf("panel.session_ttl zero after Load")
	}
	if cfg.Proxy.WAShim.Port == 0 {
		t.Errorf("proxy.wa_shim.port zero after Load — validate should have caught this")
	}
}
