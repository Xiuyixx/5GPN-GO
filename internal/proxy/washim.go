// Package proxy hosts the WA-shim TCP/443 fail-open shim.
//
// The Go port intentionally mirrors 5GPN-X/wa-shim.py: same env-var
// contract, same WA_PREFIXES / KNOWN handshake constants, same fail-open
// behavior (unrecognized traffic transparently reaches the sniproxy
// backend). Only ED / WA no-SNI Noise frames from configured client
// CIDRs are re-routed to g.whatsapp.net.
package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WAShimConfig mirrors the env-var defaults from 5GPN-X/wa-shim.py 1:1.
type WAShimConfig struct {
	Listen         string          // WA_SHIM_LISTEN (default 0.0.0.0)
	Port           int             // WA_SHIM_PORT   (default 443)
	Backend        string          // WA_SHIM_BACKEND (default 127.0.0.1:8443)
	WAHost         string          // WA_SHIM_WA_HOST (default g.whatsapp.net)
	WAPort         int             // WA_SHIM_WA_PORT (default 443)
	Resolvers      []string        // WA_SHIM_RESOLVER (default 1.1.1.1, 8.8.8.8)
	SelfIPs        map[string]bool // WA_SHIM_SELF_IPS (loopback + 0.0.0.0 always present)
	AllowCIDR      []*net.IPNet    // WA_SHIM_ALLOW_CIDR (default 172.22.0.0/16, 127.0.0.0/8)
	PeekTimeout    time.Duration   // WA_SHIM_PEEK_TIMEOUT (default 3s)
	ConnectTimeout time.Duration   // WA_SHIM_CONNECT_TIMEOUT (default 8s)
	DNSTTL         time.Duration   // WA_SHIM_DNS_TTL (default 60s)
	MaxConn        int             // WA_SHIM_MAXCONN (default 8192)
	Logger         *slog.Logger
}

// DefaultWAShimConfig returns the same defaults as the Python shim.
func DefaultWAShimConfig() WAShimConfig {
	must := func(pattern string) *net.IPNet {
		_, n, err := net.ParseCIDR(pattern)
		if err != nil {
			panic(err)
		}
		return n
	}
	return WAShimConfig{
		Listen:         "0.0.0.0",
		Port:           443,
		Backend:        "127.0.0.1:8443",
		WAHost:         "g.whatsapp.net",
		WAPort:         443,
		Resolvers:      []string{"1.1.1.1", "8.8.8.8"},
		SelfIPs:        map[string]bool{"127.0.0.1": true, "::1": true, "0.0.0.0": true, "::": true},
		AllowCIDR:      []*net.IPNet{must("172.22.0.0/16"), must("127.0.0.0/8")},
		PeekTimeout:    3 * time.Second,
		ConnectTimeout: 8 * time.Second,
		DNSTTL:         60 * time.Second,
		MaxConn:        8192,
	}
}

// Route classifies a peeked prefix. Matches the Python `classify()` output.
type Route string

const (
	// RouteBackend sends the connection to sniproxy (default fail-open path).
	RouteBackend Route = "backend"
	// RouteWhatsApp indicates a WA Noise handshake; caller should forward to WhatsApp edge.
	RouteWhatsApp Route = "whatsapp"
)

// waPrefixes are the two Noise handshake magic prefixes we detect.
var waPrefixes = []string{"ED", "WA"}

// knownHandshakes are the two full-4-byte prefixes the Python code marks
// as "known"; anything ED/WA that isn't one of these is logged as "new".
var knownHandshakes = [][]byte{
	mustHex("45440001"),
	mustHex("57410603"),
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Classify inspects the first few bytes of a connection and returns the
// route + handshake version tag ("", "known" or "new").
func Classify(data []byte) (Route, string) {
	if len(data) >= 2 {
		prefix := string(data[:2])
		for _, p := range waPrefixes {
			if prefix == p {
				if len(data) >= 4 {
					for _, k := range knownHandshakes {
						if bytes.Equal(data[:4], k) {
							return RouteWhatsApp, "known"
						}
					}
					return RouteWhatsApp, "new"
				}
				return RouteWhatsApp, "new"
			}
		}
	}
	return RouteBackend, ""
}

// SourceAllowed reports whether a peer address matches any allow CIDR.
func SourceAllowed(cfg WAShimConfig, remoteHost string) bool {
	ip := net.ParseIP(remoteHost)
	if ip == nil {
		return false
	}
	for _, n := range cfg.AllowCIDR {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// WAShim is the running server.
type WAShim struct {
	cfg     WAShimConfig
	active  atomic.Int64
	cache   waCache
	log     *slog.Logger
}

type waCache struct {
	mu        sync.Mutex
	addrs     []string
	expiresAt time.Time
}

// NewWAShim builds a shim ready to Serve.
func NewWAShim(cfg WAShimConfig) *WAShim {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &WAShim{cfg: cfg, log: cfg.Logger}
}

// Serve accepts connections on cfg.Listen:Port until ctx is done.
func (s *WAShim) Serve(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Listen, strconv.Itoa(s.cfg.Port))
	lc := &net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("wa-shim: listen %s: %w", addr, err)
	}
	defer ln.Close()
	s.log.Info("wa-shim listening", "addr", addr, "backend", s.cfg.Backend, "allow", s.cfg.AllowCIDR)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.log.Warn("wa-shim accept", "err", err)
			continue
		}
		if s.active.Load() >= int64(s.cfg.MaxConn) {
			_ = conn.Close()
			continue
		}
		s.active.Add(1)
		go func() {
			defer s.active.Add(-1)
			s.handle(ctx, conn)
		}()
	}
}

// ActiveConnections is exposed for observability.
func (s *WAShim) ActiveConnections() int64 { return s.active.Load() }

func (s *WAShim) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	source := peerHost(conn)
	first, _ := peek(conn, 8, s.cfg.PeekTimeout)

	route, version := Classify(first)
	if route == RouteWhatsApp && SourceAllowed(s.cfg, source) {
		addrs, err := s.resolveEdge(ctx)
		if err == nil && len(addrs) > 0 {
			s.log.Info("wa-shim: whatsapp handshake", "version", version, "src", source, "edge", addrs[0])
			if s.relay(ctx, conn, net.JoinHostPort(addrs[0], strconv.Itoa(s.cfg.WAPort)), first) {
				return
			}
			s.log.Warn("wa-shim: edge unavailable; failing open to backend", "src", source)
		}
	}
	// Default fail-open path: send everything to sniproxy backend.
	_ = s.relay(ctx, conn, s.cfg.Backend, first)
}

// peek reads up to n bytes with a total deadline. Mirrors the Python
// peek() early-exit heuristics on 2-4 bytes of WA prefix data.
func peek(conn net.Conn, n int, timeout time.Duration) ([]byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 0, n)
	tmp := make([]byte, n)
	for len(buf) < n {
		got, err := conn.Read(tmp[:n-len(buf)])
		if got > 0 {
			buf = append(buf, tmp[:got]...)
		}
		if err != nil {
			break
		}
		// Early exit: if we already have 2 bytes and they are NOT a WA
		// prefix, OR we already have 4 bytes total, stop peeking.
		if len(buf) >= 2 {
			isWA := false
			prefix := string(buf[:2])
			for _, p := range waPrefixes {
				if prefix == p {
					isWA = true
				}
			}
			if !isWA || len(buf) >= 4 {
				break
			}
		}
	}
	return buf, nil
}

func peerHost(c net.Conn) string {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP.String()
	}
	host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	return host
}

// resolveEdge returns cached WA edge IPs or refreshes via configured DNS
// resolvers. Falls through cleanly when no resolver returns a global address.
func (s *WAShim) resolveEdge(ctx context.Context) ([]string, error) {
	// Literal IP short-circuit.
	if ip := net.ParseIP(s.cfg.WAHost); ip != nil {
		if s.cfg.SelfIPs[ip.String()] {
			return nil, nil
		}
		return []string{ip.String()}, nil
	}

	s.cache.mu.Lock()
	if time.Now().Before(s.cache.expiresAt) && len(s.cache.addrs) > 0 {
		out := append([]string(nil), s.cache.addrs...)
		s.cache.mu.Unlock()
		return out, nil
	}
	s.cache.mu.Unlock()

	var addrs []string
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	for _, r := range s.cfg.Resolvers {
		res := &net.Resolver{
			PreferGo: true,
			Dial: func(dctx context.Context, network, _ string) (net.Conn, error) {
				addr := r
				if !strings.Contains(addr, ":") {
					addr = net.JoinHostPort(addr, "53")
				}
				return dialer.DialContext(dctx, network, addr)
			},
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := res.LookupIP(lookupCtx, "ip4", s.cfg.WAHost)
		cancel()
		if err != nil {
			continue
		}
		for _, ip := range ips {
			str := ip.String()
			if s.cfg.SelfIPs[str] || !isGlobal(ip) {
				continue
			}
			addrs = append(addrs, str)
		}
		if len(addrs) > 0 {
			break
		}
	}
	s.cache.mu.Lock()
	s.cache.addrs = addrs
	s.cache.expiresAt = time.Now().Add(s.cfg.DNSTTL)
	s.cache.mu.Unlock()
	return addrs, nil
}

// relay bidirectionally splices client <-> upstream. `first` is sent to
// upstream immediately so we don't lose the peeked bytes.
func (s *WAShim) relay(ctx context.Context, client net.Conn, upstreamAddr string, first []byte) bool {
	dialer := &net.Dialer{Timeout: s.cfg.ConnectTimeout}
	up, err := dialer.DialContext(ctx, "tcp", upstreamAddr)
	if err != nil {
		return false
	}
	defer up.Close()
	if len(first) > 0 {
		if _, err := up.Write(first); err != nil {
			return false
		}
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, up); done <- struct{}{} }()
	<-done
	return true
}

func isGlobal(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
}
