package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Domain: "gw.example.com", PanelPort: 8443, PanelBind: "0.0.0.0",
			TLS: config.TLSConfig{Cert: "/etc/x/fullchain.pem", Key: "/etc/x/privkey.pem"},
		},
		Panel: config.PanelConfig{SessionTTL: 24 * time.Hour, RateLimit: config.RateLimitConfig{LoginPerMinute: 5, LockoutMinutes: 15}},
		DNS:   config.DNSConfig{Upstreams: []string{"1.1.1.1:53"}},
		Proxy: config.ProxyConfig{
			SniProxy: config.SniProxyConfig{ListenHTTP: 80, LoopbackHTTPS: 8443},
			WAShim:   config.WAShimConfig{Listen: "0.0.0.0", Port: 443, Backend: "127.0.0.1:8443", WAHost: "g.whatsapp.net", AllowCIDR: []string{"172.22.0.0/16"}, MaxConn: 8192},
		},
		Exits: []config.ExitConfig{{ID: "direct", Protocol: "direct"}},
	}
}

func TestSystemdRenderWritesFilesAtomically(t *testing.T) {
	dir := t.TempDir()
	s := &Systemd{
		Config:       testConfig(),
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
	}
	if err := s.render(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dnsdist.conf", "mihomo.yaml", "sniproxy.conf"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if len(body) < 50 {
			t.Errorf("%s looks empty (%d bytes): %s", name, len(body), body)
		}
	}
	dnsdist, _ := os.ReadFile(filepath.Join(dir, "dnsdist.conf"))
	if !strings.Contains(string(dnsdist), "addTLSLocal(") {
		t.Errorf("dnsdist missing DoT block: %s", dnsdist)
	}
}

func TestSystemdApplyRollsBackOnHealthFailure(t *testing.T) {
	dir := t.TempDir()
	dnsPath := filepath.Join(dir, "dnsdist.conf")
	mihoPath := filepath.Join(dir, "mihomo.yaml")
	sniPath := filepath.Join(dir, "sniproxy.conf")
	// seed a "known-good" baseline the rollback can restore.
	if err := os.WriteFile(dnsPath, []byte("-- baseline dnsdist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mihoPath, []byte("baseline: mihomo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sniPath, []byte("# baseline sniproxy"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Systemd{
		Config:        testConfig(),
		DnsdistPath:   dnsPath,
		MihomoPath:    mihoPath,
		SniproxyPath:  sniPath,
		HealthDelay:   0,
		HealthTimeout: time.Second,
		HealthCheck:   func(context.Context) error { return errors.New("simulated health failure") },
	}
	// Skip reload — we're testing the render + probe + rollback path
	// without hitting systemctl.
	s.reloadOverride = func(context.Context) error { return nil }

	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, PrevSnapshot: 0})
	if err == nil {
		t.Fatal("expected health-check error to surface from Apply")
	}
	if !res.RolledBack {
		t.Errorf("expected RolledBack=true, got %+v", res)
	}
	// After rollback the file bodies should match the baselines we
	// wrote up top.
	body, _ := os.ReadFile(dnsPath)
	if string(body) != "-- baseline dnsdist" {
		t.Errorf("dnsdist not restored: %s", body)
	}
	body, _ = os.ReadFile(mihoPath)
	if string(body) != "baseline: mihomo" {
		t.Errorf("mihomo not restored: %s", body)
	}
}

func TestSystemdApplyOKPath(t *testing.T) {
	dir := t.TempDir()
	s := &Systemd{
		Config:       testConfig(),
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
		HealthCheck:  func(context.Context) error { return nil },
	}
	s.reloadOverride = func(context.Context) error { return nil }
	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.RolledBack {
		t.Fatalf("unexpected rollback: %+v", res)
	}
	if res.Health != "ok" {
		t.Errorf("want health=ok, got %q", res.Health)
	}
}
