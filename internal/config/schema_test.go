package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalYAML = `
server:
  domain: gateway.example.com
  panel_port: 8443
  panel_bind: 0.0.0.0
panel:
  session_ttl: 24h
  rate_limit:
    login_per_minute: 5
    lockout_minutes: 15
proxy:
  wa_shim:
    listen: 0.0.0.0
    port: 443
    backend: 127.0.0.1:8443
    wa_host: g.whatsapp.net
    allow_cidr: ["172.22.0.0/16"]
    peek_timeout: 3s
    connect_timeout: 8s
    dns_ttl: 60s
    max_conn: 8192
exits:
  - id: direct
    protocol: direct
tgbot:
  token: ""
  admin_chat_ids: []
ios:
  http_port: 8111
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExampleYAMLValidates(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(repoRoot, "configs", "example.yaml")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("skip: %s missing (%v)", p, err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load(example.yaml): %v", err)
	}
	if cfg.Server.Domain == "" {
		t.Fatal("expected domain to be non-empty")
	}
	if cfg.Proxy.WAShim.PeekTimeout != 3*time.Second {
		t.Fatalf("peek_timeout got %v want 3s", cfg.Proxy.WAShim.PeekTimeout)
	}
}

func TestMissingDomainRejected(t *testing.T) {
	bad := strings.Replace(minimalYAML, "domain: gateway.example.com", "domain: \"\"", 1)
	p := writeTemp(t, bad)
	if _, err := Load(p); err == nil {
		t.Fatal("expected validation error for empty domain")
	}
}

func TestInvalidCIDRRejected(t *testing.T) {
	bad := strings.Replace(minimalYAML,
		`allow_cidr: ["172.22.0.0/16"]`,
		`allow_cidr: ["not-a-cidr"]`, 1)
	p := writeTemp(t, bad)
	if _, err := Load(p); err == nil {
		t.Fatal("expected validation error for bad CIDR")
	}
}

func TestExitIDMustBeAlphanum(t *testing.T) {
	bad := strings.Replace(minimalYAML, "id: direct", `id: "wg 1"`, 1)
	p := writeTemp(t, bad)
	if _, err := Load(p); err == nil {
		t.Fatal("expected validation error for non-alphanum exit id")
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("TG_TOKEN", "expanded-token")
	body := minimalYAML + "\n# canary: ${TG_TOKEN}\n"
	p := writeTemp(t, body)
	_, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Sanity: the ${...} on a comment line should not break parsing.
}
