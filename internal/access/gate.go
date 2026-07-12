// Package access implements the "internal-only" IP allowlist gate used
// by the panel HTTP surface and by the three transparent proxies
// (mtproxy, sniforward, quicforward).
//
// The business rule this encodes:
//
//   5GPN-Go's paying customers all reach the box via the 联通 5G APN
//   slice, which assigns a private (RFC1918) source address such as
//   172.22.0.0/16. When the operator flips
//   frontdoor.internal_only_enabled to true, every accept-time call
//   site asks the gate whether the peer's IP falls inside a configured
//   list of CIDRs (default: private/RFC1918 + loopback). If not, the
//   connection is silently dropped before any handshake budget is
//   spent, so unauthenticated internet scanners never see the panel
//   login, mtproxy handshake, or QUIC/TLS peek path at all. Loopback
//   is always allowed regardless of the CIDR list so local health
//   probes and cross-process localhost dials never lock themselves out
//   (e.g. sniforward → 127.0.0.1:8444 panel backend).
//
// The gate is deliberately dead simple: two settings (enabled + CIDR
// list) parsed into an atomic.Pointer[state] snapshot, plus an Allow()
// method that runs on every accept. Live-swap via Refresh means the
// panel can toggle the gate without a daemon restart.
package access

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// DefaultInternalCIDRs is the fall-back CIDR list used when the
// operator has enabled the gate but hasn't populated
// KeyFrontdoorInternalCIDRs. Covers the three RFC1918 blocks + IPv4
// loopback + IPv6 loopback + IPv6 ULA (fd00::/8), which is what the
// 联通 5G APN slice actually assigns for the private customer plane.
const DefaultInternalCIDRs = "172.16.0.0/12,10.0.0.0/8,192.168.0.0/16,127.0.0.0/8,::1/128,fd00::/8"

// state is the immutable snapshot swapped atomically on every
// Refresh. Keeping enabled + cidrs together in one pointer means an
// Allow() call always sees a consistent pair; there is no window where
// enabled=true has been read alongside a stale (empty) cidr slice.
type state struct {
	enabled bool
	cidrs   []netip.Prefix
}

// Gate is the read-side of the internal-only IP allowlist.
//
// Construct via NewGate. Zero value is not usable — the atomic
// pointer needs an initial snapshot before the first Allow() call, and
// NewGate wires that up from the settings store.
type Gate struct {
	settings *settings.Store
	snapshot atomic.Pointer[state]
}

// NewGate reads the two settings and returns a Gate whose Allow()
// method is safe to call from any goroutine. A nil settings store is
// tolerated (returns a permanently-disabled gate) so callers wired
// without the DNS/panel front-door in tests don't crash.
func NewGate(store *settings.Store) (*Gate, error) {
	g := &Gate{settings: store}
	if err := g.Refresh(context.Background()); err != nil {
		return nil, err
	}
	return g, nil
}

// Refresh re-reads the settings store and swaps the internal state
// atomically. Called from the internal-only settings POST handler
// after a successful write so operators don't have to restart to
// tighten or loosen the allowlist.
func (g *Gate) Refresh(ctx context.Context) error {
	if g == nil {
		return nil
	}
	next := &state{}
	if g.settings != nil {
		on, err := g.settings.GetBool(ctx, settings.KeyFrontdoorInternalOnlyEnabled)
		if err != nil {
			return fmt.Errorf("access.Gate.Refresh: %w", err)
		}
		next.enabled = on
		raw, err := g.settings.GetString(ctx, settings.KeyFrontdoorInternalCIDRs)
		if err != nil {
			return fmt.Errorf("access.Gate.Refresh: %w", err)
		}
		if strings.TrimSpace(raw) == "" {
			raw = DefaultInternalCIDRs
		}
		next.cidrs = parseCIDRs(raw)
	}
	g.snapshot.Store(next)
	return nil
}

// Enabled reports the current toggle state without a settings round
// trip. Cheap enough to call on every accept — reads the atomic
// pointer once. Used by the api server to short-circuit the middleware
// when the toggle is off (zero per-request cost in the common case).
func (g *Gate) Enabled() bool {
	if g == nil {
		return false
	}
	s := g.snapshot.Load()
	if s == nil {
		return false
	}
	return s.enabled
}

// Allow returns true if the connection from remote is permitted under
// the current policy:
//
//   - Gate disabled → always allow (fast path, one atomic load).
//   - remote is loopback → always allow (health probes, sniforward →
//     127.0.0.1:8444 panel backend, etc.).
//   - remote's IP falls inside any configured CIDR → allow.
//   - Otherwise → reject.
//
// A nil remote (or one whose address can't be parsed as ip:port) is
// rejected when the gate is enabled and allowed when it is not, on the
// principle that a well-behaved net.Conn always exposes RemoteAddr
// and anything else is a scanner-shaped probe.
func (g *Gate) Allow(remote net.Addr) bool {
	if g == nil {
		return true
	}
	s := g.snapshot.Load()
	if s == nil || !s.enabled {
		return true
	}
	ip := extractIP(remote)
	if !ip.IsValid() {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, p := range s.cidrs {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// AllowIP is the same as Allow but takes an already-parsed netip.Addr.
// Used by the QUIC forwarder where clientAddr *net.UDPAddr is
// available inline.
func (g *Gate) AllowIP(ip netip.Addr) bool {
	if g == nil {
		return true
	}
	s := g.snapshot.Load()
	if s == nil || !s.enabled {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, p := range s.cidrs {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// extractIP pulls a netip.Addr out of a net.Addr regardless of the
// concrete type. TCP conns give us *net.TCPAddr, UDP conns give us
// *net.UDPAddr, and the fallback path parses a "host:port" string.
func extractIP(remote net.Addr) netip.Addr {
	if remote == nil {
		return netip.Addr{}
	}
	switch a := remote.(type) {
	case *net.TCPAddr:
		if a == nil {
			return netip.Addr{}
		}
		return netAddrToNetip(a.IP)
	case *net.UDPAddr:
		if a == nil {
			return netip.Addr{}
		}
		return netAddrToNetip(a.IP)
	case *net.IPAddr:
		if a == nil {
			return netip.Addr{}
		}
		return netAddrToNetip(a.IP)
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		host = remote.String()
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr
	}
	return netip.Addr{}
}

// netAddrToNetip converts a net.IP (possibly a v4-in-v6 form) to a
// netip.Addr, normalising IPv4-mapped v6 addresses to their v4 form so
// prefix matches against RFC1918 (v4) work as expected on dual-stack
// listeners.
func netAddrToNetip(ip net.IP) netip.Addr {
	if ip == nil {
		return netip.Addr{}
	}
	if v4 := ip.To4(); v4 != nil {
		addr, _ := netip.AddrFromSlice(v4)
		return addr.Unmap()
	}
	addr, _ := netip.AddrFromSlice(ip)
	return addr.Unmap()
}

// parseCIDRs parses a comma-separated CIDR list. Invalid entries are
// silently dropped — validation lives at the settings POST boundary,
// so a bad value can't reach the gate; if one does, we still prefer a
// live gate on the survivors over a hard failure that would leave the
// panel unreachable.
func parseCIDRs(raw string) []netip.Prefix {
	out := make([]netip.Prefix, 0, 8)
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ValidateCIDRs parses a comma-separated CIDR list the same way the
// runtime does, returning the first error. Exposed for the settings
// POST handler so validation semantics stay in one place.
func ValidateCIDRs(raw string) error {
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := netip.ParsePrefix(s); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", s, err)
		}
	}
	return nil
}
