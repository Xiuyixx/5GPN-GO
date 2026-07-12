package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
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

func invalidRenderConfig() *config.Config {
	cfg := testConfig()
	cfg.EffectiveRules = []rules.Rule{{
		ID: "invalid-action", Kind: rules.KindDomain, Pattern: "example.com",
		Action: "missing-exit", Enabled: true,
	}}
	return cfg
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

func TestSystemdApplyRestoresFilesAfterPartialRenderFailure(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "dnsdist.conf"),
		filepath.Join(dir, "mihomo.yaml"),
		filepath.Join(dir, "sniproxy.conf"),
	}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("baseline-%d", i)), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	reloads := 0
	s := &Systemd{DnsdistPath: paths[0], MihomoPath: paths[1], SniproxyPath: paths[2]}
	s.reloadOverride = func(context.Context) error {
		reloads++
		return nil
	}

	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: invalidRenderConfig()})
	if err == nil || !strings.Contains(err.Error(), `action "missing-exit"`) {
		t.Fatalf("apply error = %v, want invalid-action render error", err)
	}
	if !res.RolledBack || res.Health != "failed" {
		t.Fatalf("result = %+v, want successful rollback", res)
	}
	if reloads != 0 {
		t.Fatalf("reloads = %d, want 0 after render failure", reloads)
	}
	for i, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != fmt.Sprintf("baseline-%d", i) {
			t.Fatalf("%s not restored: body=%q err=%v", path, body, readErr)
		}
	}
}

func TestSystemdApplyRenderRestoreFailureIsCombinedAndNotRolledBack(t *testing.T) {
	dir := t.TempDir()
	s := &Systemd{
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
	}
	restoreErr := errors.New("restore failed")
	s.restoreOverride = func(snapshotBundle) error { return restoreErr }

	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: invalidRenderConfig()})
	if err == nil || !strings.Contains(err.Error(), `action "missing-exit"`) || !errors.Is(err, restoreErr) {
		t.Fatalf("combined error = %v, want render and restore causes", err)
	}
	if res.RolledBack || res.Health != "failed" {
		t.Fatalf("result = %+v, rollback failure must not report RolledBack", res)
	}
}

func TestSystemdApplyRollbackReloadFailureIsCombinedAndNotRolledBack(t *testing.T) {
	dir := t.TempDir()
	s := &Systemd{
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
	}
	applyReloadErr := errors.New("apply reload failed")
	rollbackReloadErr := errors.New("rollback reload failed")
	reloads := 0
	s.reloadOverride = func(context.Context) error {
		reloads++
		if reloads == 1 {
			return applyReloadErr
		}
		return rollbackReloadErr
	}

	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: testConfig()})
	if !errors.Is(err, applyReloadErr) || !errors.Is(err, rollbackReloadErr) {
		t.Fatalf("combined error = %v, want apply and rollback reload causes", err)
	}
	if res.RolledBack || res.Health != "failed" {
		t.Fatalf("result = %+v, rollback reload failure must not report RolledBack", res)
	}
	if reloads != 2 {
		t.Fatalf("reloads = %d, want apply + rollback attempts", reloads)
	}
}

func TestSystemdHealthRollbackFailureReportsNotRolledBack(t *testing.T) {
	dir := t.TempDir()
	healthErr := errors.New("health failed")
	rollbackReloadErr := errors.New("rollback reload failed")
	observed := make(chan ApplyResult, 1)
	reloads := 0
	s := &Systemd{
		DnsdistPath:   filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:    filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath:  filepath.Join(dir, "sniproxy.conf"),
		HealthTimeout: time.Second,
		HealthCheck:   func(context.Context) error { return healthErr },
		HealthObserver: func(_ context.Context, _ ApplyRequest, res ApplyResult) {
			observed <- res
		},
	}
	s.reloadOverride = func(context.Context) error {
		reloads++
		if reloads == 1 {
			return nil
		}
		return rollbackReloadErr
	}

	res, err := s.Apply(context.Background(), ApplyRequest{SnapshotID: 1, Config: testConfig()})
	if err != nil || res.Health != "observing" {
		t.Fatalf("initial result = %+v err=%v", res, err)
	}
	select {
	case final := <-observed:
		if final.RolledBack || final.Health != "failed" {
			t.Fatalf("final result = %+v, want failed without rollback claim", final)
		}
		if !strings.Contains(final.Reason, healthErr.Error()) || !strings.Contains(final.Reason, rollbackReloadErr.Error()) {
			t.Fatalf("final reason = %q, want both failures", final.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("health observer did not report rollback failure")
	}
}

func TestSystemdCommitFailureRestoresAndReloadsPreviousFiles(t *testing.T) {
	dir := t.TempDir()
	dnsPath := filepath.Join(dir, "dnsdist.conf")
	mihoPath := filepath.Join(dir, "mihomo.yaml")
	sniPath := filepath.Join(dir, "sniproxy.conf")
	baselines := map[string]string{
		dnsPath:  "-- old dnsdist",
		mihoPath: "old: mihomo",
		sniPath:  "# old sniproxy",
	}
	for path, body := range baselines {
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	reloads := 0
	oldConfig := testConfig()
	s := &Systemd{
		DnsdistPath: dnsPath, MihomoPath: mihoPath, SniproxyPath: sniPath,
		Config: oldConfig,
	}
	s.reloadOverride = func(context.Context) error { reloads++; return nil }
	res, err := s.Apply(context.Background(), ApplyRequest{
		SnapshotID: 2,
		Config:     testConfig(),
		Commit:     func(context.Context) error { return errors.New("database unavailable") },
	})
	if err == nil || !res.RolledBack {
		t.Fatalf("commit failure result=%+v err=%v", res, err)
	}
	if reloads != 2 {
		t.Fatalf("reload calls=%d, want new + restored", reloads)
	}
	for path, want := range baselines {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Fatalf("%s not restored: body=%q err=%v", path, body, readErr)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("%s mode=%04o, want 0640", path, info.Mode().Perm())
		}
	}
	if s.Config != oldConfig {
		t.Fatal("failed commit replaced last-known-good Config")
	}
}

func TestSystemdCommitFailureRemovesFilesThatDidNotExistBefore(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "dnsdist.conf"),
		filepath.Join(dir, "mihomo.yaml"),
		filepath.Join(dir, "sniproxy.conf"),
	}
	s := &Systemd{DnsdistPath: paths[0], MihomoPath: paths[1], SniproxyPath: paths[2]}
	s.reloadOverride = func(context.Context) error { return nil }
	_, err := s.Apply(context.Background(), ApplyRequest{
		SnapshotID: 1,
		Config:     testConfig(),
		Commit:     func(context.Context) error { return errors.New("commit failed") },
	})
	if err == nil {
		t.Fatal("expected commit error")
	}
	for _, path := range paths {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("new file survived rollback: %s err=%v", path, statErr)
		}
	}
}

func TestSystemdRollbackRestoresAllFilesAfterPartialRenderFailure(t *testing.T) {
	for _, failAt := range []int{2, 3} {
		t.Run(fmt.Sprintf("file-%d", failAt), func(t *testing.T) {
			dir := t.TempDir()
			paths := []string{
				filepath.Join(dir, "dnsdist.conf"),
				filepath.Join(dir, "mihomo.yaml"),
				filepath.Join(dir, "sniproxy.conf"),
			}
			baselines := []string{"old dnsdist", "old mihomo", "old sniproxy"}
			for i, path := range paths {
				if err := os.WriteFile(path, []byte(baselines[i]), 0o640); err != nil {
					t.Fatal(err)
				}
			}

			renderErr := errors.New("injected per-file render failure")
			renderCalls := 0
			reloadCalls := 0
			s := &Systemd{
				DnsdistPath: paths[0], MihomoPath: paths[1], SniproxyPath: paths[2],
				Config: testConfig(),
			}
			s.renderFileOverride = func(path string, r Renderer, cfg *config.Config) error {
				renderCalls++
				if renderCalls == failAt {
					return renderErr
				}
				return writeRendered(path, r, cfg)
			}
			s.reloadOverride = func(context.Context) error {
				reloadCalls++
				return nil
			}

			err := s.Rollback(context.Background(), 42)
			if !errors.Is(err, renderErr) {
				t.Fatalf("Rollback error = %v, want injected render failure", err)
			}
			if renderCalls != failAt {
				t.Fatalf("render calls = %d, want %d", renderCalls, failAt)
			}
			if reloadCalls != 1 {
				t.Fatalf("reload calls = %d, want one compensating reload", reloadCalls)
			}
			for i, path := range paths {
				body, readErr := os.ReadFile(path)
				if readErr != nil || string(body) != baselines[i] {
					t.Fatalf("%s not restored: body=%q err=%v", path, body, readErr)
				}
				info, statErr := os.Stat(path)
				if statErr != nil || info.Mode().Perm() != 0o640 {
					t.Fatalf("%s mode not restored: mode=%v err=%v", path, info.Mode().Perm(), statErr)
				}
			}
		})
	}
}

func TestSystemdRollbackReloadFailureRestoresAndReloadsPreviousFiles(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "dnsdist.conf"),
		filepath.Join(dir, "mihomo.yaml"),
		filepath.Join(dir, "sniproxy.conf"),
	}
	baselines := []string{"old dnsdist", "old mihomo", "old sniproxy"}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte(baselines[i]), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	initialReloadErr := errors.New("initial rollback reload failed")
	reloadCalls := 0
	s := &Systemd{
		DnsdistPath: paths[0], MihomoPath: paths[1], SniproxyPath: paths[2],
		Config: testConfig(),
	}
	s.reloadOverride = func(context.Context) error {
		reloadCalls++
		if reloadCalls == 1 {
			return initialReloadErr
		}
		return nil
	}

	err := s.Rollback(context.Background(), 42)
	if !errors.Is(err, initialReloadErr) {
		t.Fatalf("Rollback error = %v, want initial reload failure", err)
	}
	if reloadCalls != 2 {
		t.Fatalf("reload calls = %d, want initial and compensating reloads", reloadCalls)
	}
	for i, path := range paths {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != baselines[i] {
			t.Fatalf("%s not restored: body=%q err=%v", path, body, readErr)
		}
	}
}

func TestSystemdRollbackCompensationJoinsAllFailures(t *testing.T) {
	dir := t.TempDir()
	initialReloadErr := errors.New("initial rollback reload failed")
	restoreErr := errors.New("restore failed")
	compensationReloadErr := errors.New("compensating reload failed")
	reloadCalls := 0
	s := &Systemd{
		DnsdistPath:  filepath.Join(dir, "dnsdist.conf"),
		MihomoPath:   filepath.Join(dir, "mihomo.yaml"),
		SniproxyPath: filepath.Join(dir, "sniproxy.conf"),
		Config:       testConfig(),
	}
	s.restoreOverride = func(snapshotBundle) error { return restoreErr }
	s.reloadOverride = func(context.Context) error {
		reloadCalls++
		if reloadCalls == 1 {
			return initialReloadErr
		}
		return compensationReloadErr
	}

	err := s.Rollback(context.Background(), 42)
	for _, want := range []error{initialReloadErr, restoreErr, compensationReloadErr} {
		if !errors.Is(err, want) {
			t.Fatalf("Rollback error = %v, want cause %v", err, want)
		}
	}
	if reloadCalls != 2 {
		t.Fatalf("reload calls = %d, compensation reload must run after restore failure", reloadCalls)
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
			s.setConfig(testConfig())
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
