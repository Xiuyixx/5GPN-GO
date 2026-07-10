package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
	}
	if err := s.render(testConfig()); err != nil {
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

	observed := make(chan ApplyResult, 1)
	s := &Systemd{
		DnsdistPath:   dnsPath,
		MihomoPath:    mihoPath,
		SniproxyPath:  sniPath,
		HealthDelay:   0,
		HealthTimeout: time.Second,
		HealthCheck:   func(context.Context) error { return errors.New("simulated health failure") },
		HealthObserver: func(_ context.Context, _ ApplyRequest, res ApplyResult) {
			observed <- res
		},
	}
	// Skip reload — we're testing the render + probe + rollback path
	// without hitting systemctl.
	s.reloadOverride = func(context.Context) error { return nil }

	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, PrevSnapshot: 0, Config: testConfig()})
	if err != nil {
		t.Fatalf("sync Apply must succeed (bg goroutine owns rollback): %v", err)
	}
	if res.Health != "observing" {
		t.Fatalf("expected sync Health=observing, got %+v", res)
	}

	select {
	case final := <-observed:
		if !final.RolledBack {
			t.Errorf("expected bg observer RolledBack=true, got %+v", final)
		}
		if final.Health != "failed" {
			t.Errorf("expected bg Health=failed, got %q", final.Health)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health observer never fired")
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
	observed := make(chan ApplyResult, 1)
	s := &Systemd{
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
		HealthCheck:  func(context.Context) error { return nil },
		HealthObserver: func(_ context.Context, _ ApplyRequest, res ApplyResult) {
			observed <- res
		},
	}
	s.reloadOverride = func(context.Context) error { return nil }
	cfg := testConfig()
	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.RolledBack {
		t.Fatalf("unexpected rollback: %+v", res)
	}
	if res.Health != "observing" {
		t.Errorf("want sync health=observing, got %q", res.Health)
	}
	select {
	case final := <-observed:
		if final.Health != "ok" {
			t.Errorf("want bg health=ok, got %q (reason=%q)", final.Health, final.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health observer never fired")
	}

	// After confirm, s.Config must equal the applied config.
	s.configMu.RLock()
	got := s.Config
	s.configMu.RUnlock()
	if got != cfg {
		t.Errorf("s.Config not updated on health confirm")
	}
}

// Concurrent Apply while another is still in the health-observation
// phase must return ErrApplyInFlight (v3 HIGH-1).
func TestSystemdApplyReturns409OnConcurrentApply(t *testing.T) {
	dir := t.TempDir()
	blockHealth := make(chan struct{})
	s := &Systemd{
		DnsdistPath:   filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:    filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath:  filepath.Join(dir, "sniproxy.conf"),
		HealthDelay:   0,
		HealthTimeout: 2 * time.Second,
		HealthCheck: func(context.Context) error {
			<-blockHealth
			return nil
		},
	}
	s.reloadOverride = func(context.Context) error { return nil }

	// First Apply: kicks off bg goroutine that blocks on blockHealth.
	first, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: testConfig()})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if first.Health != "observing" {
		t.Fatalf("first apply not observing: %+v", first)
	}

	// Second Apply while bg goroutine still holds applyMu must 409.
	_, err = s.Apply(context.Background(), ApplyRequest{SnapshotID: 2, Config: testConfig()})
	if !errors.Is(err, ErrApplyInFlight) {
		t.Fatalf("expected ErrApplyInFlight, got err=%v", err)
	}

	close(blockHealth)
	// Give the bg goroutine time to unlock.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.applyMu.TryLock() {
			s.applyMu.Unlock()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("applyMu never released after bg goroutine completed")
}

// Apply must reject nil Config with a clear error, not panic.
func TestSystemdApplyRejectsNilConfig(t *testing.T) {
	s := &Systemd{}
	_, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: nil})
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

// SetConfig races against Rollback readers — the RWMutex must prevent
// a data race under -race.
func TestSystemdSetConfigConcurrent(t *testing.T) {
	s := &Systemd{Config: testConfig()}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.SetConfig(testConfig())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.configMu.RLock()
			_ = s.Config
			s.configMu.RUnlock()
		}
	}()
	wg.Wait()
}
