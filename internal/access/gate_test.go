package access

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

func newTestSettings(t *testing.T) *settings.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canary.db")
	handle, err := db.Open(db.Config{Path: path})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(handle); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return settings.New(handle)
}

func tcp(ip string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: 40000}
}

// TestDisabledAllowsAnyIP — with the toggle off the gate should be
// transparent, no matter what CIDRs sit in the store.
func TestDisabledAllowsAnyIP(t *testing.T) {
	s := newTestSettings(t)
	ctx := context.Background()
	// deliberately configure a restrictive CIDR list to prove disabled
	// still wins.
	if err := s.SetString(ctx, settings.KeyFrontdoorInternalCIDRs, "172.22.0.0/16", "test"); err != nil {
		t.Fatal(err)
	}
	g, err := NewGate(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"8.8.8.8", "1.2.3.4", "172.22.0.5", "127.0.0.1"} {
		if !g.Allow(tcp(ip)) {
			t.Errorf("disabled gate rejected %s", ip)
		}
	}
	if g.Enabled() {
		t.Fatalf("Enabled() true with toggle off")
	}
}

// TestEnabledDefaultCIDRs — with the default RFC1918 list, private IPs
// pass and public IPs are rejected. Loopback always passes.
func TestEnabledDefaultCIDRs(t *testing.T) {
	s := newTestSettings(t)
	ctx := context.Background()
	if err := s.SetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled, true, "test"); err != nil {
		t.Fatal(err)
	}
	// leave KeyFrontdoorInternalCIDRs unset → NewGate should fall back
	// to DefaultInternalCIDRs.
	g, err := NewGate(s)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Enabled() {
		t.Fatalf("Enabled() false with toggle on")
	}

	allowed := []string{"172.22.2.33", "127.0.0.1", "10.0.0.1", "192.168.1.1"}
	for _, ip := range allowed {
		if !g.Allow(tcp(ip)) {
			t.Errorf("default CIDR list rejected internal %s", ip)
		}
	}
	rejected := []string{"8.244.10.5", "188.253.127.215", "1.1.1.1"}
	for _, ip := range rejected {
		if g.Allow(tcp(ip)) {
			t.Errorf("default CIDR list allowed public %s", ip)
		}
	}
}

// TestEnabledCustomCIDRs — an explicit narrow list should allow only
// that block and reject the wider RFC1918 space.
func TestEnabledCustomCIDRs(t *testing.T) {
	s := newTestSettings(t)
	ctx := context.Background()
	if err := s.SetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetString(ctx, settings.KeyFrontdoorInternalCIDRs, "172.22.0.0/16", "test"); err != nil {
		t.Fatal(err)
	}
	g, err := NewGate(s)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Allow(tcp("172.22.5.5")) {
		t.Errorf("narrow CIDR rejected 172.22.5.5")
	}
	// 10.0.0.1 is RFC1918 but outside 172.22.0.0/16 — must be rejected.
	if g.Allow(tcp("10.0.0.1")) {
		t.Errorf("narrow CIDR allowed 10.0.0.1")
	}
	if g.Allow(tcp("8.8.8.8")) {
		t.Errorf("narrow CIDR allowed public 8.8.8.8")
	}
	// Loopback is always allowed regardless of CIDR list.
	if !g.Allow(tcp("127.0.0.1")) {
		t.Errorf("narrow CIDR rejected loopback")
	}
}

// TestIPv6Loopback — ::1 should always pass when the gate is enabled,
// even if no v6 prefix is configured, because loopback is unconditional.
func TestIPv6Loopback(t *testing.T) {
	s := newTestSettings(t)
	ctx := context.Background()
	if err := s.SetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled, true, "test"); err != nil {
		t.Fatal(err)
	}
	// Only v4 CIDR in the list — v6 loopback must still be allowed.
	if err := s.SetString(ctx, settings.KeyFrontdoorInternalCIDRs, "10.0.0.0/8", "test"); err != nil {
		t.Fatal(err)
	}
	g, err := NewGate(s)
	if err != nil {
		t.Fatal(err)
	}
	v6lo := &net.TCPAddr{IP: net.ParseIP("::1"), Port: 40000}
	if !g.Allow(v6lo) {
		t.Errorf("v6 loopback rejected under v4-only allowlist")
	}
	v6pub := &net.TCPAddr{IP: net.ParseIP("2001:4860:4860::8888"), Port: 40000}
	if g.Allow(v6pub) {
		t.Errorf("v6 public 2001:4860:4860::8888 allowed under v4-only allowlist")
	}
}

// TestRefreshSwapsState — flipping the toggle in the store and calling
// Refresh must be observed by subsequent Allow() calls without
// reconstruction.
func TestRefreshSwapsState(t *testing.T) {
	s := newTestSettings(t)
	ctx := context.Background()
	g, err := NewGate(s)
	if err != nil {
		t.Fatal(err)
	}
	// Initially: disabled, public IP passes.
	if !g.Allow(tcp("8.8.8.8")) {
		t.Fatalf("expected public allowed pre-enable")
	}
	if err := s.SetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled, true, "test"); err != nil {
		t.Fatal(err)
	}
	// Toggle flipped in the DB but not yet Refresh()ed — snapshot is stale.
	if !g.Allow(tcp("8.8.8.8")) {
		t.Fatalf("stale snapshot should still allow")
	}
	if err := g.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if g.Allow(tcp("8.8.8.8")) {
		t.Fatalf("after Refresh, public 8.8.8.8 should be rejected")
	}
	if !g.Allow(tcp("172.22.5.5")) {
		t.Fatalf("after Refresh, private 172.22.5.5 should still be allowed")
	}
}

// TestValidateCIDRs — a bogus entry surfaces at the settings-POST
// boundary, not silently at Refresh time.
func TestValidateCIDRs(t *testing.T) {
	if err := ValidateCIDRs("10.0.0.0/8,172.16.0.0/12"); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	if err := ValidateCIDRs("not-a-cidr"); err == nil {
		t.Fatalf("bogus CIDR accepted")
	}
	if err := ValidateCIDRs(""); err != nil {
		t.Fatalf("empty list should be a no-op, got %v", err)
	}
}

func TestConfigurePublishesOneValidatedSnapshot(t *testing.T) {
	g, err := NewGate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Configure(true, "172.22.0.0/16"); err != nil {
		t.Fatal(err)
	}
	if !g.Enabled() || !g.Allow(tcp("172.22.1.1")) || g.Allow(tcp("10.0.0.1")) {
		t.Fatal("configured policy was not published atomically")
	}
	if err := g.Configure(true, "bad-cidr"); err == nil {
		t.Fatal("invalid Configure accepted")
	}
	if !g.Allow(tcp("172.22.1.1")) || g.Allow(tcp("10.0.0.1")) {
		t.Fatal("invalid Configure changed the prior snapshot")
	}
}
