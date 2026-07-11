// Package frontdoor: this file implements :853/UDP DNS-over-QUIC (RFC 9250)
// as a quic-go early listener wired to a *resolver.Resolver (plan §4
// Phase 9). Unlike DoT's "tcp-tls" listener (which miekg/dns drives
// end-to-end), quic-go only gives us connections and streams — the RFC
// 9250 §4.2 length-prefixed DNS framing on each stream is hand-rolled
// here.
package frontdoor

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// defaultDoQAddr is dual-stack, mirroring defaultDoTAddr — DoQ clients
// reaching us over IPv6 also work (plan AC-F7).
const defaultDoQAddr = "[::]:853"

// maxDoQMessageBytes bounds a single length-prefixed DNS message read off
// a stream. The RFC 9250 §4.2 length prefix is a 2-byte value, so 65535 is
// the wire-format ceiling; anything the prefix claims beyond that would be
// a malformed/hostile stream, refused before allocating a buffer for it.
const maxDoQMessageBytes = 65535

// DoQ is a DNS-over-QUIC (RFC 9250) listener. It owns a single UDP socket
// (via quic-go's EarlyListener) and answers one DNS query per QUIC stream,
// per RFC 9250 §4.2 framing: a 2-byte big-endian length prefix followed by
// the raw DNS wire message, on both the request and the response.
type DoQ struct {
	addr     string
	tlsCfg   *tls.Config
	resolver *resolver.Resolver
	logger   *slog.Logger
	listener *quic.EarlyListener

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewDoQ constructs a DoQ listener. addr defaults to defaultDoQAddr
// ("[::]:853") when empty; logger defaults to slog.Default() when nil.
func NewDoQ(addr string, tlsCfg *tls.Config, r *resolver.Resolver, logger *slog.Logger) *DoQ {
	if addr == "" {
		addr = defaultDoQAddr
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DoQ{
		addr:     addr,
		tlsCfg:   tlsCfg,
		resolver: r,
		logger:   logger,
	}
}

// Start binds the QUIC listener and begins accepting connections/streams in
// background goroutines. It returns once the UDP socket is bound (the same
// eager-bind-before-serve shape as udpServer.listen / tcpServer.listen), so
// a caller that gets a nil error can immediately dial Addr().
//
// Allow0RTT is explicitly false — plan §4 Phase 9 non-goal: 0-RTT
// resumption stays off in v0.3.0 so a replayed 0-RTT query can never be
// answered twice.
func (q *DoQ) Start(ctx context.Context) error {
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp", q.addr)
	if err != nil {
		return fmt.Errorf("frontdoor: doq listen %s: %w", q.addr, err)
	}

	ln, err := quic.ListenEarly(conn, q.tlsCfg, &quic.Config{
		Allow0RTT:      false,
		MaxIdleTimeout: 30 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("frontdoor: doq quic listen %s: %w", q.addr, err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	q.mu.Lock()
	q.listener = ln
	q.cancel = cancel
	q.mu.Unlock()

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.acceptLoop(runCtx)
	}()

	return nil
}

// Shutdown closes the listener (so no new connections are accepted),
// cancels every in-flight accept/stream-read wait, and then waits for
// outstanding stream handlers to finish — up to ctx's deadline.
func (q *DoQ) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	ln := q.listener
	cancel := q.cancel
	q.mu.Unlock()

	if ln == nil {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	_ = ln.Close()

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Addr returns the address the listener is actually bound to. Only
// meaningful after a successful Start; primarily useful for tests that
// bind an ephemeral port ("host:0") and need to learn which port the OS
// assigned.
func (q *DoQ) Addr() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.listener != nil {
		return q.listener.Addr().String()
	}
	return q.addr
}

// acceptLoop accepts QUIC connections until ctx is cancelled or the
// listener is closed, dispatching each to its own stream-accept loop.
func (q *DoQ) acceptLoop(ctx context.Context) {
	for {
		conn, err := q.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, quic.ErrServerClosed) {
				return
			}
			q.logger.Warn("doq: accept error", "error", err)
			return
		}
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.handleConn(ctx, conn)
		}()
	}
}

// handleConn accepts every stream the peer opens on conn — a compliant
// RFC 9250 client opens one bidirectional stream per query — dispatching
// each to handleStream.
func (q *DoQ) handleConn(ctx context.Context, conn *quic.Conn) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			// Connection closed by the peer, idle-timed-out, or our own
			// Shutdown cancelled ctx — any of these end this connection's
			// stream-accept loop.
			return
		}
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.handleStream(stream)
		}()
	}
}

// handleStream answers a single RFC 9250 §4.2-framed query: read a 2-byte
// big-endian length prefix, read that many bytes as the DNS wire message,
// resolve it, and write the response back with the same framing before
// closing the stream's write side (signalling "no further data" per RFC
// 9250 §5.1).
//
// A recover() guards this goroutine the same way handler.go's ServeDNS and
// doh.go's serveHTTP do — a malformed or hostile stream must never take
// the shared panel process down with it (plan §7.5 shared-fate mitigation).
func (q *DoQ) handleStream(stream *quic.Stream) {
	defer func() {
		if rec := recover(); rec != nil {
			q.logger.Error("doq: panic recovered", "panic", rec)
		}
		_ = stream.Close()
	}()

	req, err := readDoQMessage(stream)
	if err != nil {
		q.logger.Warn("doq: read query", "error", err)
		return
	}

	resp, resolveErr := q.resolver.Resolve(context.Background(), req, doqRemoteIP(stream))
	if resolveErr != nil {
		q.logger.Warn("doq: resolve error", "error", resolveErr, "qname", qnameOf(req))
	}
	if resp == nil {
		resp = servFailReply(req)
	}

	if err := writeDoQMessage(stream, resp); err != nil {
		q.logger.Warn("doq: write response", "error", err)
	}
}

// readDoQMessage reads one RFC 9250 §4.2-framed DNS message: a 2-byte
// big-endian length prefix followed by that many bytes of wire-format DNS.
func readDoQMessage(r io.Reader) (*dns.Msg, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("read length prefix: %w", err)
	}
	n := binary.BigEndian.Uint16(hdr[:])
	if n == 0 {
		return nil, errors.New("zero-length dns message")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read message body (%d bytes): %w", n, err)
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(buf); err != nil {
		return nil, fmt.Errorf("unpack dns message: %w", err)
	}
	return msg, nil
}

// writeDoQMessage packs msg and writes it framed with the same 2-byte
// big-endian length prefix readDoQMessage expects.
func writeDoQMessage(w io.Writer, msg *dns.Msg) error {
	packed, err := msg.Pack()
	if err != nil {
		return fmt.Errorf("pack dns message: %w", err)
	}
	if len(packed) > maxDoQMessageBytes {
		return fmt.Errorf("packed message too large: %d bytes", len(packed))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(packed)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("write length prefix: %w", err)
	}
	if _, err := w.Write(packed); err != nil {
		return fmt.Errorf("write message body: %w", err)
	}
	return nil
}
