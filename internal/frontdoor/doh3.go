// Package frontdoor: this file wraps the existing DoH handler (doh.go) in
// an HTTP/3 server so /dns-query is also reachable over QUIC — RFC 8484
// DNS-over-HTTPS, just carried on h3 instead of h2/HTTP1.1 (plan §4 Phase
// 9). No new query-handling logic lives here: the same *DoH.Handler()
// (including its RFC 8484 §5.1 / RFC 2308 §5 Cache-Control policy) answers
// every request, so DoH3 clients get byte-identical behavior to DoH1.
package frontdoor

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/quic-go/quic-go/http3"
)

// defaultDoH3Addr is dual-stack, mirroring defaultDoTAddr/defaultDoQAddr.
// Port collision with the panel's HTTPS TCP :443 is fine because UDP and
// TCP are separate address spaces (plan §4 Phase 9).
const defaultDoH3Addr = "[::]:443"

// DoH3 serves the existing DoH handler over HTTP/3 (QUIC). It owns a UDP
// socket and an *http3.Server; the /dns-query route is the only route
// mounted, matching doh.go's "no separate mux" non-goal (plan §4 Phase 4).
type DoH3 struct {
	addr   string
	tlsCfg *tls.Config
	doh    *DoH
	server *http3.Server
	logger *slog.Logger

	mu   sync.Mutex
	conn net.PacketConn
}

// NewDoH3 wires an HTTP/3 server around an existing DoH handler. addr
// defaults to defaultDoH3Addr ("[::]:443") when empty; logger defaults to
// slog.Default() when nil.
func NewDoH3(addr string, tlsCfg *tls.Config, doh *DoH, logger *slog.Logger) *DoH3 {
	if addr == "" {
		addr = defaultDoH3Addr
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DoH3{
		addr:   addr,
		tlsCfg: tlsCfg,
		doh:    doh,
		logger: logger,
	}
}

// Start binds the UDP socket and begins serving HTTP/3 in a background
// goroutine. It returns once the socket is bound — the same
// eager-bind-before-serve shape as udpServer.listen/DoQ.Start — so a
// caller that gets a nil error can immediately dial Addr().
func (d *DoH3) Start(ctx context.Context) error {
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp", d.addr)
	if err != nil {
		return fmt.Errorf("frontdoor: doh3 listen %s: %w", d.addr, err)
	}

	mux := http.NewServeMux()
	mux.Handle("/dns-query", d.doh.Handler())

	srv := &http3.Server{
		TLSConfig: d.tlsCfg,
		Handler:   mux,
	}

	d.mu.Lock()
	d.conn = conn
	d.server = srv
	d.mu.Unlock()

	go func() {
		if serveErr := srv.Serve(conn); serveErr != nil {
			d.logger.Debug("doh3: serve stopped", "error", serveErr)
		}
	}()

	return nil
}

// Shutdown gracefully stops the HTTP/3 server, honoring ctx's deadline,
// then closes the underlying UDP socket — http3.Server.Serve documents
// that closing the server does not close the connection passed to it, so
// Shutdown must do that itself (mirrors DoQ.Shutdown's listener + socket
// teardown).
func (d *DoH3) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	srv := d.server
	conn := d.conn
	d.mu.Unlock()

	if srv == nil {
		return nil
	}
	err := srv.Shutdown(ctx)
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

// Addr returns the address the listener is actually bound to. Only
// meaningful after a successful Start; primarily useful for tests that
// bind an ephemeral port ("host:0") and need to learn which port the OS
// assigned.
func (d *DoH3) Addr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		return d.conn.LocalAddr().String()
	}
	return d.addr
}
