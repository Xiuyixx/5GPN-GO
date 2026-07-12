package mtproxy

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
)

// Config controls a Server instance. Zero values are usable except
// for Secret, which must be a 16-byte value the operator has committed
// to and shared with clients as "dd"+hex — rotating it invalidates all
// current tg://proxy URLs.
type Config struct {
	// Listen is the TCP address the accept loop binds. Default
	// ":9443" — MTProto proxies conventionally live on 9443 to
	// avoid the :443 collision our SNI forwarder needs.
	Listen string

	// Secret is the 16-byte inner secret. Clients paste "dd" +
	// hex(Secret) into Telegram's proxy dialog.
	Secret [SecretSize]byte

	// DialTimeout bounds the connect to the target Telegram DC.
	// Default 5s.
	DialTimeout time.Duration

	// HandshakeTimeout bounds how long we wait for a client's
	// initial 64-byte handshake. Slow-loris scanners get cut off
	// past this. Default 10s.
	HandshakeTimeout time.Duration

	// MaxConcurrent caps in-flight connections. Same reasoning as
	// sniforward: without a cap a public MTProto port draws
	// scanner traffic that will walk us into EMFILE. Default 4096.
	MaxConcurrent int

	// PreferIPv6 dials the DC's v6 endpoint first, falling back to
	// v4. Only useful when the host has good v6 to Telegram.
	PreferIPv6 bool

	// Gate, when non-nil, is consulted at accept time. Clients whose
	// source IP is not on the allowlist get their socket closed before
	// any handshake budget is spent — silent close (no log), to match
	// the existing probe-error policy at handshake reject. A nil Gate
	// means "no restriction" (the historical behaviour).
	Gate *access.Gate
}

// Server accepts obfuscated2 client connections, decodes each
// handshake, dials the corresponding Telegram DC, and pipes bytes
// through a bidirectional cipher relay. Zero value is not usable;
// construct via New.
type Server struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	closed   atomic.Bool
	wg       sync.WaitGroup

	sem chan struct{}
}

// New wires a Server around cfg. logger may be nil (uses slog.Default).
func New(cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Listen == "" {
		cfg.Listen = ":9443"
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4096
	}
	return &Server{
		cfg:    cfg,
		logger: logger,
		sem:    make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Start binds the listener and begins accepting in a background
// goroutine. Returns nil precisely when the socket is live and can be
// dialed by clients; a bind error is returned synchronously so the
// operator sees it at startup instead of in the logs after silent
// failure.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("mtproxy: listen %s: %w", s.cfg.Listen, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop()

	s.logger.Info("mtproxy: listening", "addr", ln.Addr().String())
	return nil
}

// Addr returns the bound address. Useful for tests that Listen on ":0".
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
			s.logger.Warn("mtproxy: accept", "error", err)
			continue
		}
		select {
		case s.sem <- struct{}{}:
		default:
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

// handleConn drives one client through handshake → DC dial → relay.
// Silent close on obvious probe traffic (ErrForbiddenPrefix,
// ErrProtocolMismatch, EOF before handshake) so scanners don't fill
// journalctl.
func (s *Server) handleConn(clientConn net.Conn) {
	defer func() {
		if rec := recover(); rec != nil {
			s.logger.Error("mtproxy: panic recovered", "panic", rec)
			_ = clientConn.Close()
		}
	}()

	// Internal-only gate: reject non-allowlisted source IPs at accept
	// time BEFORE the HandshakeTimeout budget. This closes the timing
	// side-channel that would otherwise reveal the allowlist boundary
	// (a scanner learns which IPs get 10s to handshake vs. instant
	// close) and keeps journalctl clean — scanner traffic disappears
	// with zero cipher work spent.
	if s.cfg.Gate != nil && !s.cfg.Gate.Allow(clientConn.RemoteAddr()) {
		_ = clientConn.Close()
		return
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	cs, _, err := DecodeClientHandshake(clientConn, s.cfg.Secret)
	if err != nil {
		if !isProbeError(err) {
			// Warn (not Debug) — a real Telegram client whose
			// handshake we reject is an operator-actionable event
			// (wrong secret, wrong protocol mode, secret without
			// "dd" prefix in the tg://proxy URL, etc.); Debug is
			// filtered at the default log level and silently
			// swallowed the clue on every failed connection in
			// the wild.
			s.logger.Warn("mtproxy: handshake reject", "error", err, "remote", clientConn.RemoteAddr())
		}
		_ = clientConn.Close()
		return
	}
	_ = clientConn.SetReadDeadline(time.Time{})

	s.logger.Info("mtproxy: handshake ok",
		"dc", cs.DC,
		"proto", fmt.Sprintf("0x%08x", cs.Protocol),
		"remote", clientConn.RemoteAddr())

	dialer := &net.Dialer{Timeout: s.cfg.DialTimeout}
	upstream, err := DialDC(dialer, cs.DC, s.cfg.PreferIPv6)
	if err != nil {
		s.logger.Warn("mtproxy: dial DC", "dc", cs.DC, "error", err)
		_ = clientConn.Close()
		return
	}

	us, err := EncodeUpstreamHandshake(cs.DC, cs.Protocol)
	if err != nil {
		s.logger.Warn("mtproxy: upstream handshake", "error", err)
		_ = clientConn.Close()
		_ = upstream.Close()
		return
	}
	// Send our fresh obfuscated2 handshake to the DC first — bytes
	// after this are the client's payload, re-obfuscated.
	if _, err := upstream.Write(us.Header[:]); err != nil {
		s.logger.Warn("mtproxy: write upstream handshake", "error", err)
		_ = clientConn.Close()
		_ = upstream.Close()
		return
	}

	s.logger.Info("mtproxy: relay start", "dc", cs.DC, "remote", clientConn.RemoteAddr(), "upstream", upstream.RemoteAddr())

	relay := NewRelay(clientConn, upstream, cs, us)
	relayErr := relay.Run(context.Background())
	s.logger.Info("mtproxy: relay end",
		"dc", cs.DC,
		"remote", clientConn.RemoteAddr(),
		"error", relayErr,
		"benign_close", isBenignCloseError(relayErr))
}

// SecretString returns the "dd"+hex representation of the configured
// secret, i.e. the string a client pastes into Telegram's proxy
// dialog. Length: 34 chars (1 leading dd byte + 16 secret bytes = 17
// bytes hex).
func (s *Server) SecretString() string {
	return "dd" + hex.EncodeToString(s.cfg.Secret[:])
}

func isProbeError(err error) bool {
	// ErrProtocolMismatch was INTENTIONALLY removed from the silence
	// list — an operator debugging "why doesn't my iPhone connect"
	// needs the wrong-protocol reject visible in logs. ErrTooShort +
	// ErrForbiddenPrefix are still silenced because they match random
	// internet scanners spraying garbage at :2443.
	return errors.Is(err, ErrTooShort) ||
		errors.Is(err, ErrForbiddenPrefix)
}
