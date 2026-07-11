// Package resolver implements the DNS plane's split-horizon resolver
// kernel: a hot-swappable RuleTable, a three-state (block/direct/proxy)
// classifier over it, and a DoT upstream client. Listeners (:53, :853,
// :443, DoQ) are wired in a later phase; this package only answers
// dns.Msg in, dns.Msg out.
package resolver

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const defaultUpstreamTimeout = 30 * time.Second

// knownServerNames pins the TLS ServerName (SNI) for well-known DoT
// upstreams reached by bare IP, where SNI can't be inferred from the
// dial address. 223.5.5.5/223.6.6.6 serve a cert for *.alidns.com (NOT
// "dot.pub" — that nickname belongs to Tencent's dns.pub/doh.pub, a
// different provider; verified against the live cert SAN). 1.1.1.1/1.0.0.1
// serve a cert for cloudflare-dns.com.
var knownServerNames = map[string]string{
	"223.5.5.5:853": "dns.alidns.com",
	"223.6.6.6:853": "dns.alidns.com",
	"1.1.1.1:853":   "cloudflare-dns.com",
	"1.0.0.1:853":   "cloudflare-dns.com",
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

// Upstream forwards queries to one of two DoT upstream pools selected by
// category ("direct" -> CN, anything else -> Proxy). Defaults match the
// plan: CN 223.5.5.5:853 (AliDNS), Proxy 1.1.1.1:853 (Cloudflare).
type Upstream struct {
	CN      []string
	Proxy   []string
	Timeout time.Duration

	// TLSDial opens a TLS connection to addr. Defaults to
	// defaultDialTLS (crypto/tls, ALPN "dot", pinned ServerName). Tests
	// substitute an in-memory net.Pipe-backed fake here.
	TLSDial func(ctx context.Context, addr string) (net.Conn, error)
}

// NewUpstream returns an Upstream configured with the plan's default CN
// and Proxy DoT endpoints.
func NewUpstream() *Upstream {
	return &Upstream{
		CN:      []string{"223.5.5.5:853"},
		Proxy:   []string{"1.1.1.1:853"},
		Timeout: defaultUpstreamTimeout,
	}
}

func (u *Upstream) addrsFor(category string) []string {
	if strings.EqualFold(category, "direct") {
		return u.CN
	}
	return u.Proxy
}

// raceTargets returns up to two addresses to dial concurrently. With a
// single configured address it's dialed twice in parallel (two
// independent TCP+TLS attempts) so a single hung/blackholed connection
// doesn't stall the query; with two or more configured addresses the
// first two are raced instead.
func raceTargets(addrs []string) []string {
	switch len(addrs) {
	case 0:
		return nil
	case 1:
		return []string{addrs[0], addrs[0]}
	default:
		return addrs[:2]
	}
}

// Query races DoT dials against up to two targets in the category's pool
// (direct -> CN, else Proxy), returning the first successful response and
// cancelling the loser. Bounded by u.Timeout (default 30s).
func (u *Upstream) Query(ctx context.Context, msg *dns.Msg, category string) (*dns.Msg, error) {
	targets := raceTargets(u.addrsFor(category))
	if len(targets) == 0 {
		return nil, fmt.Errorf("resolver: no upstream configured for category %q", category)
	}

	timeout := u.Timeout
	if timeout <= 0 {
		timeout = defaultUpstreamTimeout
	}
	qctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		msg *dns.Msg
		err error
	}
	resCh := make(chan result, len(targets))
	var wg sync.WaitGroup
	for _, addr := range targets {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			m, err := u.queryOne(qctx, addr, msg)
			resCh <- result{m, err}
		}(addr)
	}
	go func() {
		wg.Wait()
		close(resCh)
	}()

	var lastErr error
	for r := range resCh {
		if r.err == nil && r.msg != nil {
			return r.msg, nil // defer cancel() above cancels the loser on return
		}
		lastErr = r.err
	}
	if lastErr == nil {
		lastErr = errors.New("resolver: upstream query failed")
	}
	return nil, lastErr
}

func (u *Upstream) queryOne(ctx context.Context, addr string, msg *dns.Msg) (*dns.Msg, error) {
	dial := u.TLSDial
	if dial == nil {
		dial = defaultDialTLS
	}
	conn, err := dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("resolver: dial %s: %w", addr, err)
	}
	defer conn.Close()

	// Close conn the moment ctx is done so a losing race (or an overall
	// timeout) unblocks any pending Read/Write immediately instead of
	// waiting out a deadline.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	q := msg.Copy()
	q.Id = dns.Id()
	if err := writeFramed(conn, q); err != nil {
		return nil, fmt.Errorf("resolver: write %s: %w", addr, err)
	}
	resp, err := readFramed(conn)
	if err != nil {
		return nil, fmt.Errorf("resolver: read %s: %w", addr, err)
	}
	resp.Id = msg.Id
	return resp, nil
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
