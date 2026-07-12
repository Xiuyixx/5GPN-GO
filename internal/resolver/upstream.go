// Package resolver implements the DNS plane's split-horizon resolver
// kernel: a hot-swappable RuleTable, a three-state (block/direct/proxy)
// classifier over it, and a DoT upstream client. Listener packages call
// Resolver with dns.Msg values; socket ownership stays outside this package.
package resolver

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	defaultUpstreamTimeout = 8 * time.Second
	defaultFallbackDelay   = 500 * time.Millisecond
)

// knownServerNames pins the TLS ServerName (SNI) for well-known DoT
// upstreams reached by bare IP, where SNI can't be inferred from the
// dial address. 223.5.5.5/223.6.6.6 serve a cert for *.alidns.com (NOT
// "dot.pub" — that nickname belongs to Tencent's dns.pub/doh.pub, a
// different provider; verified against the live cert SAN). 1.1.1.1/1.0.0.1
// serve a cert for cloudflare-dns.com. 8.8.8.8/8.8.4.4 serve dns.google.
var knownServerNames = map[string]string{
	"223.5.5.5:853": "dns.alidns.com",
	"223.6.6.6:853": "dns.alidns.com",
	"1.1.1.1:853":   "cloudflare-dns.com",
	"1.0.0.1:853":   "cloudflare-dns.com",
	"8.8.8.8:853":   "dns.google",
	"8.8.4.4:853":   "dns.google",
}

func serverNameFor(addr string) string {
	if sn, ok := knownServerNames[addr]; ok {
		return sn
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func defaultDialTLS(ctx context.Context, addr string) (net.Conn, error) {
	d := tls.Dialer{
		Config: &tls.Config{
			ServerName: serverNameFor(addr),
			NextProtos: []string{"dot"},
			MinVersion: tls.VersionTLS12,
		},
	}
	return d.DialContext(ctx, "tcp", addr)
}

// Tier identifies which upstream pool answered a query. Emitted from
// Query so the resolver can attribute HitsUpstreamPrimary vs Fallback
// without owning pool-selection logic.
type Tier int

const (
	TierPrimary Tier = iota
	TierFallback
)

// Upstream forwards queries to a category-selected primary pool (CN for
// direct, Proxy for anything else) with an optional cross-category
// Fallback pool that joins the race after FallbackDelay.
//
// Each configured addr owns a single multiplexed DoT connection: writes
// serialize on a mutex, reads dispatch to per-query response channels
// keyed by DNS message ID (RFC 7858 §3.4 permits out-of-order responses).
// A hot conn amortizes the TLS handshake across all subsequent queries;
// a dropped conn is reconnected lazily on the next query.
type Upstream struct {
	// CN is the primary pool for "direct"-classified queries. Defaults
	// to a pair of AliDNS DoT endpoints; racing two addrs guarantees a
	// single blackholed endpoint doesn't stall a query.
	CN []string

	// Proxy is the primary pool for non-direct queries (proxy or
	// unclassified). Defaults to a pair of Cloudflare DoT endpoints.
	Proxy []string

	// Fallback is the cross-category pool joined after FallbackDelay
	// when the primary pool has not answered. If nil, no fallback
	// stage runs; a wedged primary pool returns an error at Timeout.
	Fallback []string

	// Timeout is the hard cap on Query. Defaults to 8s.
	Timeout time.Duration

	// FallbackDelay is how long to wait before firing Fallback dials.
	// Set to 0 to disable the wait (Fallback starts concurrently with
	// primary). Defaults to 500ms.
	FallbackDelay time.Duration

	// TLSDial opens a TLS connection to addr. Defaults to
	// defaultDialTLS (crypto/tls, ALPN "dot", pinned ServerName). Tests
	// substitute an in-memory net.Pipe-backed fake here.
	TLSDial func(ctx context.Context, addr string) (net.Conn, error)

	// Logger receives per-conn warnings (dial failure, reader
	// terminated). Nil is safe (uses slog.Default).
	Logger *slog.Logger

	// conns holds one multiplexed dotConn per configured addr.
	// Initialized lazily on first Query; guarded by connsMu.
	connsMu sync.Mutex
	conns   map[string]*dotConn
}

// NewUpstream returns an Upstream with the default CN and Proxy DoT
// endpoints, Timeout=8s, and FallbackDelay=500ms. Cross-category fallback
// is disabled unless the caller explicitly populates Fallback.
func NewUpstream() *Upstream {
	return &Upstream{
		CN:            []string{"223.5.5.5:853", "223.6.6.6:853"},
		Proxy:         []string{"1.1.1.1:853", "1.0.0.1:853"},
		Timeout:       defaultUpstreamTimeout,
		FallbackDelay: defaultFallbackDelay,
	}
}

// primaryFor returns the primary pool for category. direct → CN,
// everything else → Proxy.
func (u *Upstream) primaryFor(category string) []string {
	if strings.EqualFold(category, "direct") {
		return u.CN
	}
	return u.Proxy
}

// fallbackFor returns the fallback pool for category. Only returns a
// non-empty pool when u.Fallback is EXPLICITLY configured — automatic
// cross-category fallback (direct ↔ proxy pools) was removed because
// in a GFW-adjacent deployment it silently poisoned: a proxy-classified
// query whose primary Cloudflare stalled past FallbackDelay would race
// AliDNS, and Ali returns censored/polluted answers for GFW-blocked
// domains — the client got a wrong IP and split-tunnel routing broke.
// Operators who genuinely want cross-category fallback (e.g. a homelab
// with no GFW concern) must set u.Fallback explicitly.
func (u *Upstream) fallbackFor(category string) []string {
	return u.Fallback
}

// Query races the primary pool for category concurrently. If no primary
// addr has produced an authoritative reply within FallbackDelay, the
// fallback pool joins the race. First valid response wins; loser dials
// are cancelled via ctx. Returns (msg, tier, err) so callers can
// attribute fallback usage in metrics.
//
// A response is "valid" iff its Rcode is one of NOERROR/NXDOMAIN/YXDOMAIN/
// NXRRSET — SERVFAIL/REFUSED/FORMERR/NOTIMP signal upstream failure and
// must not short-circuit the race. This lets us route through a wedged
// resolver that returns SERVFAIL to the healthy one instead of
// propagating the failure straight to the client.
func (u *Upstream) Query(ctx context.Context, msg *dns.Msg, category string) (*dns.Msg, Tier, error) {
	primary := u.primaryFor(category)
	fallback := u.fallbackFor(category)
	if len(primary) == 0 && len(fallback) == 0 {
		return nil, TierPrimary, fmt.Errorf("resolver: no upstream configured for category %q", category)
	}

	timeout := u.Timeout
	if timeout <= 0 {
		timeout = defaultUpstreamTimeout
	}
	fbDelay := u.FallbackDelay
	if fbDelay < 0 {
		fbDelay = 0
	}
	if fbDelay > timeout {
		fbDelay = timeout / 2
	}

	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		msg  *dns.Msg
		err  error
		tier Tier
	}
	// Buffered to len(primary)+len(fallback) so no goroutine blocks on
	// send after the winner returns.
	resCh := make(chan result, len(primary)+len(fallback))
	var queryWG sync.WaitGroup

	fire := func(addrs []string, tier Tier) {
		for _, addr := range addrs {
			addr := addr
			queryWG.Add(1)
			go func() {
				defer queryWG.Done()
				m, err := u.queryOne(qctx, addr, msg)
				resCh <- result{msg: m, err: err, tier: tier}
			}()
		}
	}

	fire(primary, TierPrimary)

	fbTimer := time.NewTimer(fbDelay)
	defer fbTimer.Stop()
	fbStarted := len(fallback) == 0
	if fbDelay == 0 && !fbStarted {
		fire(fallback, TierFallback)
		fbStarted = true
	}

	expected := len(primary)
	if fbStarted {
		expected += len(fallback)
	}

	var lastErr error
	seen := 0
	for {
		select {
		case r := <-resCh:
			seen++
			if r.err == nil && r.msg != nil && isAuthoritative(r.msg) {
				return r.msg, r.tier, nil
			}
			if r.err != nil {
				lastErr = r.err
			} else if r.msg != nil {
				// Non-authoritative reply (SERVFAIL/REFUSED). Track
				// for the failure-cause message but keep racing.
				lastErr = fmt.Errorf("upstream rcode %s", dns.RcodeToString[r.msg.Rcode])
			}
			// If all outstanding sources have reported (both pools
			// exhausted), give up before the timeout so we don't
			// block for nothing. Only applies once fallback has
			// been fired — until then more dials are still coming.
			if fbStarted && seen >= expected {
				if lastErr == nil {
					lastErr = errors.New("resolver: all upstreams failed")
				}
				return nil, TierPrimary, lastErr
			}
		case <-fbTimer.C:
			if !fbStarted {
				fire(fallback, TierFallback)
				fbStarted = true
				expected = len(primary) + len(fallback)
			}
		case <-qctx.Done():
			if errors.Is(qctx.Err(), context.DeadlineExceeded) {
				// Make timeout retirement synchronous so an immediate retry cannot
				// reuse a blackholed connection. Each query retires only the exact
				// connection generation it pinned; retiring an address's current
				// connection here could close a replacement opened concurrently.
				cancel()
				queryWG.Wait()
			}
			if lastErr == nil {
				lastErr = qctx.Err()
			}
			return nil, TierPrimary, lastErr
		}
	}
}

// isAuthoritative reports whether an rcode value is one the race should
// accept as a definitive answer. NOERROR/NXDOMAIN/YXDOMAIN/NXRRSET all
// carry authoritative meaning; SERVFAIL/REFUSED/FORMERR/NOTIMP signal
// that this particular upstream failed and we should keep waiting for a
// healthier peer or the fallback pool.
func isAuthoritative(m *dns.Msg) bool {
	switch m.Rcode {
	case dns.RcodeSuccess, dns.RcodeNameError, dns.RcodeYXDomain, dns.RcodeNXRrset:
		return true
	default:
		return false
	}
}

// queryOne dispatches msg through the multiplexed dotConn for addr and
// awaits the matching response. Each conn owns a single TLS session; on
// error it is discarded and the next call redials.
func (u *Upstream) queryOne(ctx context.Context, addr string, msg *dns.Msg) (*dns.Msg, error) {
	c := u.connFor(addr)
	// Copy so the caller's msg (and its shared question section) is
	// never mutated by the ID re-assignment inside dotConn.query.
	q := msg.Copy()
	origID := msg.Id
	resp, err := c.query(ctx, q)
	if err != nil {
		return nil, err
	}
	resp.Id = origID
	return resp, nil
}

// connFor returns the multiplexed dotConn for addr, constructing it on
// first use. The dial itself happens lazily inside dotConn.query so a
// stopped upstream doesn't cost us anything at startup.
func (u *Upstream) connFor(addr string) *dotConn {
	u.connsMu.Lock()
	defer u.connsMu.Unlock()
	if u.conns == nil {
		u.conns = make(map[string]*dotConn)
	}
	c, ok := u.conns[addr]
	if !ok {
		dial := u.TLSDial
		if dial == nil {
			dial = defaultDialTLS
		}
		logger := u.Logger
		if logger == nil {
			logger = slog.Default()
		}
		c = newDotConn(addr, dial, logger)
		u.conns[addr] = c
	}
	return c
}

// Close terminates all pooled DoT connections and waits for their reader
// goroutines to exit. Safe to call multiple times.
func (u *Upstream) Close() {
	u.connsMu.Lock()
	conns := u.conns
	u.conns = nil
	u.connsMu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// writeFramed / readFramed implement the DoT wire framing shared with
// plain TCP DNS: a 2-byte big-endian length prefix followed by the
// packed message (RFC 7858 §3.3, RFC 1035 §4.2.2).
func writeFramed(conn net.Conn, msg *dns.Msg) error {
	packed, err := msg.Pack()
	if err != nil {
		return err
	}
	if len(packed) > 65535 {
		return errors.New("resolver: message too large to frame")
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(packed)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err = conn.Write(packed)
	return err
}

func readFramed(conn net.Conn) (*dns.Msg, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	m := new(dns.Msg)
	if err := m.Unpack(buf); err != nil {
		return nil, err
	}
	return m, nil
}
