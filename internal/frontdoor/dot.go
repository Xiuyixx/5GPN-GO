// Package frontdoor: this file implements :853 DNS-over-TLS (RFC 7858) as
// a miekg/dns "tcp-tls" listener wired to a *resolver.Resolver (plan §4
// Phase 3).
package frontdoor

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// defaultDoTAddr is dual-stack so DoT clients reaching us over IPv6 also
// work (plan AC-F7).
const defaultDoTAddr = "[::]:853"

// DoT is a DNS-over-TLS listener. miekg/dns's "tcp-tls" network natively
// wraps the TCP accept loop in tlsCfg, so this type is a thin wrapper
// that wires the resolver into a dns.Server.
type DoT struct {
	addr     string
	tlsCfg   *tls.Config
	resolver *resolver.Resolver
	server   *dns.Server
	logger   *slog.Logger
}

// NewDoT constructs a DoT listener. addr defaults to defaultDoTAddr
// ("[::]:853") when empty; logger defaults to slog.Default() when nil.
func NewDoT(addr string, tlsCfg *tls.Config, r *resolver.Resolver, logger *slog.Logger) *DoT {
	if addr == "" {
		addr = defaultDoTAddr
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DoT{
		addr:     addr,
		tlsCfg:   tlsCfg,
		resolver: r,
		logger:   logger,
	}
}

// Start binds the tcp-tls listener and begins serving in a background
// goroutine. It blocks until the listener is confirmed bound (or binding
// fails), so a caller that gets a nil error can immediately dial addr.
func (d *DoT) Start(ctx context.Context) error {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", d.handle)

	srv := &dns.Server{
		Addr:      d.addr,
		Net:       "tcp-tls",
		TLSConfig: d.tlsCfg,
		Handler:   mux,
	}
	d.server = srv

	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		// ListenAndServe only returns before signalling ready on a bind /
		// startup failure — a clean shutdown instead closes ready first.
		return fmt.Errorf("frontdoor: dot listen %s: %w", d.addr, err)
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown gracefully stops the listener, honoring ctx's deadline.
func (d *DoT) Shutdown(ctx context.Context) error {
	if d.server == nil {
		return nil
	}
	return d.server.ShutdownContext(ctx)
}

// Addr returns the address the listener is actually bound to. Only
// meaningful after a successful Start; primarily useful for tests that
// bind an ephemeral port ("host:0") and need to learn which port the OS
// assigned.
func (d *DoT) Addr() string {
	if d.server != nil && d.server.Listener != nil {
		return d.server.Listener.Addr().String()
	}
	return d.addr
}

// handle answers a single DoT query. It never calls dns.ResponseWriter's
// Close — miekg/dns's TCP loop reads (and answers) multiple queries per
// connection and closes the socket itself once the connection ends; a
// per-query Close here would sever the connection after the first query,
// breaking the RFC 7858 "many queries per TLS session" model.
func (d *DoT) handle(w dns.ResponseWriter, req *dns.Msg) {
	resp, err := d.resolver.Resolve(context.Background(), req)
	if err != nil {
		d.logger.Warn("dot: resolve error", "error", err, "remote", w.RemoteAddr())
	}
	if resp == nil {
		return
	}
	if err := w.WriteMsg(resp); err != nil {
		d.logger.Warn("dot: write error", "error", err, "remote", w.RemoteAddr())
	}
}
