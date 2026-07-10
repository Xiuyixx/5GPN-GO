package render

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

func sampleConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Domain: "gateway.example.com", PanelPort: 8443, PanelBind: "0.0.0.0",
			TLS: config.TLSConfig{Cert: "/etc/letsencrypt/live/x/fullchain.pem", Key: "/etc/letsencrypt/live/x/privkey.pem"},
		},
		Panel: config.PanelConfig{SessionTTL: 24 * time.Hour, RateLimit: config.RateLimitConfig{LoginPerMinute: 5, LockoutMinutes: 15}},
		DNS:   config.DNSConfig{DoTPort: 853, Upstreams: []string{"1.1.1.1:53", "9.9.9.9:53"}},
		Proxy: config.ProxyConfig{
			SniProxy: config.SniProxyConfig{ListenHTTP: 80, LoopbackHTTPS: 8443},
			WAShim:   config.WAShimConfig{Listen: "0.0.0.0", Port: 443, Backend: "127.0.0.1:8443", WAHost: "g.whatsapp.net", AllowCIDR: []string{"172.22.0.0/16"}, MaxConn: 8192},
		},
		Exits: []config.ExitConfig{
			{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn.example.com:51820"}},
		},
	}
}

func TestDnsdistRender(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := (DnsdistRenderer{}).Render(sampleConfig(), buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		"addLocal(", "addTLSLocal(", "0.0.0.0:853",
		`"1.1.1.1:53"`, `"9.9.9.9:53"`,
		"MaxQPSIPRule(10000)",
		"172.22.0.0/16",
		"/etc/letsencrypt/live/x/fullchain.pem",
		// chinaList block invariants
		"local chinaList = newSuffixMatchNode()",
		"pcall(function()",
		`io.lines(`,
		defaultChinaListPath,
		"warnlog(",
		"SuffixMatchNodeRule(chinaList)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dnsdist missing %q\n----\n%s", want, body)
		}
	}
}

func TestDnsdistGolden(t *testing.T) {
	cfg := sampleConfig()
	cfg.DNS.ChinaListPath = "/var/lib/5gpn/chinalist.txt"

	buf := &bytes.Buffer{}
	if err := (DnsdistRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()

	golden := "testdata/dnsdist.golden.conf"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", golden)
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("golden file missing — run with UPDATE_GOLDEN=1 to generate: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("dnsdist golden mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

func TestDnsdistCustomChinaListPath(t *testing.T) {
	cfg := sampleConfig()
	cfg.DNS.ChinaListPath = "/custom/path/chinalist.txt"

	buf := &bytes.Buffer{}
	if err := (DnsdistRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	if !strings.Contains(body, `"/custom/path/chinalist.txt"`) {
		t.Errorf("expected custom chinalist path in output, got:\n%s", body)
	}
	if strings.Contains(body, defaultChinaListPath) {
		t.Errorf("default path should not appear when custom path set, got:\n%s", body)
	}
}

func TestSniproxyRender(t *testing.T) {
	buf := &bytes.Buffer{}
	if err := (SniproxyRenderer{}).Render(sampleConfig(), buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		"nameserver 1.1.1.1",
		"nameserver 9.9.9.9",
		"listener 0.0.0.0:80",
		"listener 127.0.0.1:8443",
		"user pxout",
		"table tls_hosts",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sniproxy missing %q\n----\n%s", want, body)
		}
	}
}

// TestMihomoRenderExitsMap verifies proxy map construction and name/type strip.
func TestMihomoRenderExitsMap(t *testing.T) {
	cfg := sampleConfig()
	cfg.Exits = []config.ExitConfig{
		{ID: "direct", Protocol: "direct"},
		{ID: "wg1", Protocol: "wireguard", Config: map[string]any{
			"endpoint": "vpn.example.com:51820", "private_key": "abc", "peer": "def",
			// These should be stripped — xexit.Parse injects them but mihomo.go must not clobber.
			"name": "should-be-stripped", "type": "should-be-stripped",
		}},
	}

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	// Structural invariants.
	for _, want := range []string{
		"proxies:",
		"name: wg1",
		"type: wireguard",
		"private_key: abc",
		"rules:",
		"mixed-port:",
		"store-selected: false",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mihomo missing %q\n----\n%s", want, body)
		}
	}

	// Active exit (first in list = "direct") must appear as MATCH target and group leader.
	if !strings.Contains(body, "MATCH,direct") {
		t.Errorf("expected MATCH,direct (active exit), got:\n%s", body)
	}
	// name/type keys from Config map must NOT clobber the explicit fields.
	if strings.Contains(body, "should-be-stripped") {
		t.Errorf("name/type from Config map leaked into output:\n%s", body)
	}
	// Old hard-coded MATCH,PROXY must not appear.
	if strings.Contains(body, "MATCH,PROXY") {
		t.Errorf("hard-coded MATCH,PROXY must not appear in output:\n%s", body)
	}
}

// TestMihomoRenderNoUserMatch verifies synthetic MATCH fallback when EffectiveRules has no MATCH rule.
func TestMihomoRenderNoUserMatch(t *testing.T) {
	cfg := sampleConfig()
	cfg.Exits = []config.ExitConfig{
		{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn.example.com:51820"}},
	}
	cfg.EffectiveRules = []rules.Rule{
		{ID: "r1", Kind: rules.KindDomainSuffix, Pattern: "google.com", Action: "wg1", Priority: 10, Enabled: true},
		{ID: "r2", Kind: rules.KindDomain, Pattern: "direct.example.com", Action: "DIRECT", Priority: 5, Enabled: true},
	}

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	// r2 has lower priority so it sorts first.
	if !strings.Contains(body, "DOMAIN,direct.example.com,DIRECT") {
		t.Errorf("expected r2 rule line, got:\n%s", body)
	}
	if !strings.Contains(body, "DOMAIN-SUFFIX,google.com,wg1") {
		t.Errorf("expected r1 rule line, got:\n%s", body)
	}
	// Synthetic MATCH to active exit (wg1 = first exit).
	if !strings.Contains(body, "MATCH,wg1") {
		t.Errorf("expected synthetic MATCH,wg1, got:\n%s", body)
	}
}

// TestMihomoRenderUserMatchOverride verifies that a user-supplied MATCH rule is used as-is
// and no duplicate MATCH is appended (Risk R2).
func TestMihomoRenderUserMatchOverride(t *testing.T) {
	cfg := sampleConfig()
	cfg.Exits = []config.ExitConfig{
		{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn.example.com:51820"}},
		{ID: "wg2", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn2.example.com:51820"}},
	}
	cfg.EffectiveRules = []rules.Rule{
		{ID: "r1", Kind: rules.KindDomainSuffix, Pattern: "google.com", Action: "wg1", Priority: 10, Enabled: true},
		// User explicitly sets MATCH to wg2 — must be honored, no synthetic fallback.
		{ID: "m1", Kind: rules.KindMatch, Action: "wg2", Priority: 99, Enabled: true},
	}

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	if !strings.Contains(body, "MATCH,wg2") {
		t.Errorf("expected user MATCH,wg2, got:\n%s", body)
	}
	// Must not have two MATCH lines.
	count := strings.Count(body, "MATCH,")
	if count != 1 {
		t.Errorf("expected exactly 1 MATCH line, got %d:\n%s", count, body)
	}
}

// TestMihomoRenderActiveExitLeadsGroup verifies active exit is first in PROXY group.
func TestMihomoRenderActiveExitLeadsGroup(t *testing.T) {
	cfg := sampleConfig()
	cfg.Exits = []config.ExitConfig{
		{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn1.example.com:51820"}},
		{ID: "wg2", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn2.example.com:51820"}},
	}

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()

	// wg1 is first exit → active; must appear before wg2 in the proxy-groups proxies list.
	wg1Pos := strings.Index(body, "- wg1")
	wg2Pos := strings.Index(body, "- wg2")
	if wg1Pos < 0 || wg2Pos < 0 {
		t.Fatalf("proxy group entries not found:\n%s", body)
	}
	if wg1Pos > wg2Pos {
		t.Errorf("active exit wg1 must precede wg2 in proxy group:\n%s", body)
	}
}

// TestMihomoRenderMixedPort verifies mixed-port is rendered with default and custom values.
func TestMihomoRenderMixedPort(t *testing.T) {
	cfg := sampleConfig()

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "mixed-port: 7890") {
		t.Errorf("expected default mixed-port 7890, got:\n%s", buf.String())
	}

	cfg.Proxy.MihomoMixedPort = 1234
	buf.Reset()
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "mixed-port: 1234") {
		t.Errorf("expected custom mixed-port 1234, got:\n%s", buf.String())
	}
}

// TestMihomoRenderGolden checks full render output against a golden file.
func TestMihomoRenderGolden(t *testing.T) {
	cfg := sampleConfig()
	cfg.Exits = []config.ExitConfig{
		{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn.example.com:51820", "private_key": "testkey"}},
		{ID: "wg2", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn2.example.com:51820"}},
	}
	cfg.EffectiveRules = []rules.Rule{
		{ID: "r1", Kind: rules.KindDomainSuffix, Pattern: "google.com", Action: "wg1", Priority: 10, Enabled: true},
		{ID: "r2", Kind: rules.KindDomain, Pattern: "direct.example.com", Action: "DIRECT", Priority: 5, Enabled: true},
	}
	cfg.Proxy.MihomoMixedPort = 7890

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()

	golden := "testdata/mihomo.golden.yaml"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", golden)
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("golden file missing — run with UPDATE_GOLDEN=1 to generate: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("mihomo golden mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}
