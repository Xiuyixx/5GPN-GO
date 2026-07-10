package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
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
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dnsdist missing %q\n----\n%s", want, body)
		}
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

func TestMihomoRenderExitsMap(t *testing.T) {
	cfg := sampleConfig()
	cfg.Exits = []config.ExitConfig{
		{ID: "direct", Protocol: "direct"},
		{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "vpn.example.com:51820", "private_key": "abc", "peer": "def"}},
	}

	buf := &bytes.Buffer{}
	if err := (MihomoRenderer{}).Render(cfg, buf); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		"proxies:",
		"name: wg1",
		"type: wireguard",
		"private_key: abc",
		"rules:",
		"MATCH,PROXY",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mihomo missing %q\n----\n%s", want, body)
		}
	}
}
