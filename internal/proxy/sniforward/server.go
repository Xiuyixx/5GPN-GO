package sniforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
	"github.com/Xiuyixx/5GPN-Go/internal/netguard"
)

// Config controls a Server instance. Zero values are usable for the
// three tunables (timeouts and buffers) but PanelDomain +
// PanelBackend must be set for the SNI-split to work — a request
// whose SNI matches PanelDomain is dialed to PanelBackend, everything
// else is dialed to sni:443 directly.
type Config struct {
	// Listen is the address the accept loop binds. Typically
	// ":443" or "0.0.0.0:443" in production.
	Listen string

	// PanelDomain is the operator's own FQDN (server.domain). A
	// ClientHello whose SNI equals or ends in "."+PanelDomain is
	// treated as intended for the local panel — never forwarded
	// out to the internet.
	PanelDomain string

	// PanelBackend is the loopback address where the panel's HTTPS
	// listener actually binds (e.g. "127.0.0.1:8444"). SNI-matched
	// panel traffic is dialed here.
	PanelBackend string

	// DialTimeout bounds the upstream connect. Defaults to 5s.
	DialTimeout time.Duration

	// HandshakePeekTimeout bounds how long we wait for the first
	// TLS record. Slow-loris scanners get their sockets cut.
	// Defaults to 3s.
	HandshakePeekTimeout time.Duration

	// MaxConcurrent caps in-flight connections. A public :443 with a
	// wildcard SNI-forward policy is an attractive scanning target —
	// without a cap, a spike of scanners resolving thousands of SNIs
	// per second walks the process into EMFILE and starves real
	// clients. Default 2048.
	MaxConcurrent int

	// Gate, when non-nil, is consulted at accept time. Clients whose
	// source IP is not on the allowlist get their socket closed before
	// the TLS ClientHello peek — silent close, matching the existing
	// probe policy for ErrNotTLS. A nil Gate means no restriction.
	Gate *access.Gate
}

// Server is a TCP :443 SNI-split forwarder. Zero value is not
// usable; construct via New.
type Server struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	closed   atomic.Bool
	wg       sync.WaitGroup

	// sem caps in-flight handlers to cfg.MaxConcurrent. accept
	// blocks on this before spawning the handler goroutine, so a
	// scanning storm queues at userspace instead of ballooning fd
	// count and triggering EMFILE.
	sem chan struct{}
}

// New wires a Server around cfg. logger may be nil (uses
// slog.Default()).
func New(cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.HandshakePeekTimeout <= 0 {
		cfg.HandshakePeekTimeout = 3 * time.Second
	}
	if cfg.Listen == "" {
		cfg.Listen = ":443"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2048
	}
	return &Server{
		cfg:    cfg,
		logger: logger,
		sem:    make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Start binds the listener and begins accepting connections in a
// background goroutine. Returns once the bind succeeds; callers get
// a nil error precisely when the socket is live and can be dialed.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("sniforward: listen %s: %w", s.cfg.Listen, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop()

	s.logger.Info("sniforward: listening", "addr", ln.Addr().String(),
		"panel_domain", s.cfg.PanelDomain, "panel_backend", s.cfg.PanelBackend)
	return nil
}

// Addr returns the bound address; useful for tests that Listen on ":0".
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return s.cfg.Listen
	}
	return s.listener.Addr().String()
}

// Shutdown closes the listener and waits for in-flight conns to
// drain, bounded by ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closed.Store(true)
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			return
		}
		conn, err := ln.Accept()
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("sniforward: accept", "error", err)
			continue
		}
		// Rate limit: block acquiring a slot before spawning the
		// handler goroutine. When the semaphore is full, accept
		// itself pauses — Linux keeps the TCP SYNs queued in the
		// listen backlog and drops them past that, but we never
		// walk our own fd count into EMFILE.
		select {
		case s.sem <- struct{}{}:
		default:
			// At cap: refuse this connection cheaply. Closing
			// immediately keeps our fd count flat.
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			s.handleConn(conn)
		}()
	}
}

// handleConn peeks the SNI, picks an upstream, and pumps bytes both
// ways. On any error the client conn is closed and the loop exits;
// nothing is logged at info-or-below level in the hot path — noisy
// scanners would drown the daemon otherwise.
func (s *Server) handleConn(clientConn net.Conn) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("sniforward: panic recovered", "panic", rec)
		}
		_ = clientConn.Close()
	}()

	// Internal-only gate: reject non-allowlisted source IPs at accept
	// time. Silent close matches the existing "no log for scanners"
	// policy — the peekSNI path already returns without logging on
	// ErrNotTLS for the same reason.
	if s.cfg.Gate != nil && !s.cfg.Gate.Allow(clientConn.RemoteAddr()) {
		return
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(s.cfg.HandshakePeekTimeout))

	// Peek the first TLS record; keep raw so we can replay it verbatim
	// to the upstream instead of forcing the client to re-transmit.
	sni, raw, err := peekSNI(clientConn)
	if err != nil {
		// ErrNotTLS: could be a scanner or plain HTTP. Silent close;
		// noise otherwise would fill journalctl.
		return
	}
	// Reset the deadline once we've got the peek bytes — the upstream
	// dial has its own budget.
	_ = clientConn.SetReadDeadline(time.Time{})

	upstream, isLocal := s.selectUpstream(sni)
	if upstream == "" {
		s.logger.Debug("sniforward: reject", "sni", sni, "remote", clientConn.RemoteAddr())
		return
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), s.cfg.DialTimeout)
	defer cancel()

	upstreamConn, err := s.dialUpstream(dialCtx, upstream, isLocal)
	if err != nil {
		s.logger.Warn("sniforward: dial upstream", "upstream", upstream, "sni", sni, "error", err)
		return
	}
	defer func() { _ = upstreamConn.Close() }()

	// Replay the peeked record to the upstream. This must happen
	// before the client's post-handshake data is copied, or the TLS
	// state machines on both sides desync.
	if _, err := upstreamConn.Write(raw); err != nil {
		return
	}

	if !isLocal {
		s.logger.Debug("sniforward: forward", "sni", sni, "upstream", upstream)
	}

	// Copy in both directions; close both sides when either half
	// finishes so half-open TIME_WAIT accumulation is bounded.
	pipe(clientConn, upstreamConn)
}

func (s *Server) dialUpstream(ctx context.Context, upstream string, isLocal bool) (net.Conn, error) {
	dialer := &net.Dialer{}
	if isLocal {
		return dialer.DialContext(ctx, "tcp", upstream)
	}
	return netguard.DialPublicContext(ctx, nil, dialer, "tcp", upstream)
}

// selectUpstream picks the dial target from an SNI. Panel-matching
// SNIs return (panel_backend, true); everything else returns
// (sni:443, false). An empty SNI is refused with ("", false) so
// TLS-Sniff scanners can't get free egress through us.
func (s *Server) selectUpstream(sni string) (string, bool) {
	if sni == "" {
		return "", false
	}
	sni = strings.ToLower(strings.TrimRight(sni, "."))
	// Refuse to forward to bare IPs — scanners abuse SNI=IP to test
	// arbitrary destinations. Also refuse SNI values with a port
	// suffix (RFC 6066 §3 says host_name has no port).
	if strings.ContainsRune(sni, ':') {
		return "", false
	}
	if net.ParseIP(sni) != nil {
		return "", false
	}
	panelDomain := strings.ToLower(strings.TrimSpace(s.cfg.PanelDomain))
	if panelDomain != "" && (sni == panelDomain || strings.HasSuffix(sni, "."+panelDomain)) {
		return s.cfg.PanelBackend, true
	}
	return net.JoinHostPort(sni, "443"), false
}

// pipe splices two conns in both directions. Returns after both
// halves have closed.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		if ac, ok := a.(closeWriter); ok {
			_ = ac.CloseWrite()
		} else {
			_ = a.SetReadDeadline(time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		if bc, ok := b.(closeWriter); ok {
			_ = bc.CloseWrite()
		} else {
			_ = b.SetReadDeadline(time.Now())
		}
	}()
	wg.Wait()
}

type closeWriter interface {
	CloseWrite() error
}
