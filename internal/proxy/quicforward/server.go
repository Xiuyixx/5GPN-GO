package quicforward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
	"github.com/Xiuyixx/5GPN-Go/internal/netguard"
)

const (
	defaultMaxSessions         = 2048
	defaultMaxSessionsPerIP    = 64
	defaultMaxConcurrentSetups = 128
)

// Config controls a Server instance. Zero values are safe: Listen
// defaults to ":443", IdleTimeout to 5 minutes.
type Config struct {
	// Listen is the UDP address the server binds.
	Listen string

	// PanelDomain is the operator's own FQDN. Datagrams whose
	// QUIC Initial SNI matches this domain (or a subdomain of it)
	// are forwarded to PanelBackend instead of out to the internet.
	PanelDomain string

	// PanelBackend is the UDP address of the panel's own HTTP/3
	// listener (typically 127.0.0.1:8445). Empty disables the
	// local split — matched-domain datagrams are dropped.
	PanelBackend string

	// IdleTimeout retires a session whose last activity is older
	// than this. Defaults to 5 minutes.
	IdleTimeout time.Duration

	// MaxSessions caps pending plus established upstream UDP sockets.
	// Defaults to 2048.
	MaxSessions int

	// MaxSessionsPerIP caps pending plus established sessions for one
	// source IP. Defaults to 64.
	MaxSessionsPerIP int

	// MaxConcurrentSetups caps concurrent SNI parsing, DNS lookups, and
	// upstream dials. Defaults to 128.
	MaxConcurrentSetups int

	// Gate, when non-nil, is consulted for the first datagram of a
	// new 4-tuple. Established sessions (already-registered
	// clientAddr) are NOT re-checked on every packet, because a
	// scanner would never reach that codepath and re-checking would
	// noticeably regress the hot-path fanout in readLoop. Existing
	// sessions from allowed IPs continue to flow even if the operator
	// tightens the allowlist mid-flight — new sessions from the newly
	// disallowed range get rejected on their first datagram.
	Gate *access.Gate
}

// Server is a UDP :443 QUIC SNI-split forwarder.
type Server struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	listener *net.UDPConn
	runCtx   context.Context
	cancel   context.CancelFunc
	sessions sync.Map // clientAddrString -> *session
	closed   atomic.Bool
	stopped  chan struct{}
	done     chan struct{}

	resourceMu sync.Mutex
	pending    map[string]string // clientAddrString -> normalized source IP
	reserved   int               // pending plus established sessions
	perIP      map[string]int

	loopWG       sync.WaitGroup
	workerWG     sync.WaitGroup
	shutdownOnce sync.Once
}

// session tracks one client<->upstream UDP flow.
type session struct {
	clientAddr *net.UDPAddr
	backend    *net.UDPConn
	sni        string
	resourceIP string

	mu           sync.Mutex
	lastActivity time.Time
}

// New wires a Server around cfg. Nil logger falls back to slog.Default().
func New(cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Listen == "" {
		cfg.Listen = ":443"
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaultMaxSessions
	}
	if cfg.MaxSessionsPerIP <= 0 {
		cfg.MaxSessionsPerIP = defaultMaxSessionsPerIP
	}
	if cfg.MaxConcurrentSetups <= 0 {
		cfg.MaxConcurrentSetups = defaultMaxConcurrentSetups
	}
	return &Server{
		cfg:     cfg,
		logger:  logger,
		stopped: make(chan struct{}),
		done:    make(chan struct{}),
		pending: make(map[string]string),
		perIP:   make(map[string]int),
	}
}

// Start binds the UDP listener and starts the read + gc loops. The
// listener is bound synchronously; a nil return means the socket is
// live.
func (s *Server) Start(ctx context.Context) error {
	if s.closed.Load() {
		return errors.New("quicforward: server is stopped")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("quicforward: resolve %s: %w", s.cfg.Listen, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("quicforward: listen %s: %w", s.cfg.Listen, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		cancel()
		_ = conn.Close()
		return errors.New("quicforward: server is stopped")
	}
	if s.listener != nil {
		s.mu.Unlock()
		cancel()
		_ = conn.Close()
		return errors.New("quicforward: server already started")
	}
	s.listener = conn
	s.runCtx = runCtx
	s.cancel = cancel
	s.loopWG.Add(2)
	s.mu.Unlock()

	go s.readLoop()
	go s.gcLoop()

	s.logger.Info("quicforward: listening", "addr", conn.LocalAddr().String(),
		"panel_domain", s.cfg.PanelDomain, "panel_backend", s.cfg.PanelBackend)
	return nil
}

// Addr returns the bound address.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return s.cfg.Listen
	}
	return s.listener.LocalAddr().String()
}

// Shutdown stops the read loop, closes the listener, tears down
// every active session, and waits for the goroutines to exit.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.shutdownOnce.Do(func() {
		s.closed.Store(true)
		s.mu.Lock()
		ln := s.listener
		cancel := s.cancel
		stopped := s.stopped
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if ln != nil {
			_ = ln.Close()
		}
		close(stopped)

		// Wait for an in-progress promotion to publish its session before
		// taking the shutdown snapshot. promoteSession refuses all later
		// promotions because closed is already true.
		type sessionRef struct {
			key  string
			sess *session
		}
		var sessions []sessionRef
		s.resourceMu.Lock()
		s.sessions.Range(func(k, v any) bool {
			sess, ok := v.(*session)
			if ok {
				sessions = append(sessions, sessionRef{key: k.(string), sess: sess})
			}
			return true
		})
		s.resourceMu.Unlock()
		for _, ref := range sessions {
			s.dropSession(ref.key, ref.sess)
		}

		go func() {
			// setup workers are added only by readLoop. Waiting for the
			// loops first guarantees workerWG cannot receive another Add.
			s.loopWG.Wait()
			s.workerWG.Wait()
			close(s.done)
		}()
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) readLoop() {
	defer s.loopWG.Done()
	buf := make([]byte, 65535)
	for {
		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			return
		}
		n, clientAddr, err := ln.ReadFromUDP(buf)
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("quicforward: read", "error", err)
			continue
		}
		key := clientAddr.String()

		// Hot path: existing session — forward inline. No goroutine,
		// no per-datagram allocation, datagram order preserved.
		if v, ok := s.sessions.Load(key); ok {
			sess := v.(*session)
			sess.mu.Lock()
			sess.lastActivity = time.Now()
			bc := sess.backend
			sess.mu.Unlock()
			if bc != nil {
				_, _ = bc.Write(buf[:n])
			}
			continue
		}

		// Cold path: reserve capacity before allocating or spawning.
		// Duplicate Initial packets for an in-progress 4-tuple are
		// intentionally dropped; the retained first packet establishes
		// the session and later packets use the hot path.
		resourceIP := normalizedIP(clientAddr.IP)
		if !s.tryReserveSession(key, resourceIP) {
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		s.workerWG.Add(1)
		go func() {
			defer s.workerWG.Done()
			s.setupSession(data, clientAddr, key, resourceIP)
		}()
	}
}

// setupSession peeks the SNI, resolves an upstream, dials it, and
// registers the session. Only called for the first datagram of a
// new 4-tuple.
func (s *Server) setupSession(first []byte, clientAddr *net.UDPAddr, key, resourceIP string) {
	promoted := false
	defer func() {
		if !promoted {
			s.releasePending(key, resourceIP)
		}
	}()
	if s.closed.Load() {
		return
	}
	// Internal-only gate: reject the new-session cold path only.
	// Existing 4-tuples were admitted when their first datagram
	// arrived, so they keep flowing; scanners hitting a fresh UDP
	// 4-tuple from a public IP get dropped here before the SNI peek.
	if s.cfg.Gate != nil && clientAddr != nil && !s.cfg.Gate.Allow(clientAddr) {
		return
	}
	sni, ok := extractSNI(first)
	if !ok || sni == "" {
		return
	}
	upstream, isLocal, err := s.resolveUpstream(sni)
	if err != nil || upstream == nil {
		return
	}

	backend, err := net.DialUDP("udp", nil, upstream)
	if err != nil {
		s.logger.Warn("quicforward: dial upstream", "sni", sni, "upstream", upstream.String(), "error", err)
		return
	}

	if s.closed.Load() {
		_ = backend.Close()
		return
	}
	if _, err := backend.Write(first); err != nil {
		_ = backend.Close()
		return
	}

	sess := &session{
		clientAddr:   clientAddr,
		backend:      backend,
		sni:          sni,
		resourceIP:   resourceIP,
		lastActivity: time.Now(),
	}
	if !s.promoteSession(key, resourceIP, sess) {
		_ = backend.Close()
		return
	}
	promoted = true

	if !isLocal {
		s.logger.Debug("quicforward: new session", "sni", sni, "upstream", upstream.String())
	}
	s.relayBackendToClient(sess, key)
}

// resolveUpstream selects the UDP dial target for an SNI. Panel-
// matching SNIs go to PanelBackend; everything else resolves the SNI
// via DNS and dials port 443 on the first IPv4 result. Bare-IP and
// port-bearing SNI values are refused (scanner-abuse mitigation).
func (s *Server) resolveUpstream(sni string) (*net.UDPAddr, bool, error) {
	sni = strings.ToLower(strings.TrimRight(sni, "."))
	if sni == "" {
		return nil, false, errors.New("empty sni")
	}
	if strings.ContainsRune(sni, ':') {
		return nil, false, errors.New("port-bearing sni")
	}
	if net.ParseIP(sni) != nil {
		return nil, false, errors.New("bare-ip sni")
	}

	panelDomain := strings.ToLower(strings.TrimSpace(s.cfg.PanelDomain))
	if panelDomain != "" && (sni == panelDomain || strings.HasSuffix(sni, "."+panelDomain)) {
		if s.cfg.PanelBackend == "" {
			return nil, true, errors.New("panel_backend not configured")
		}
		addr, err := net.ResolveUDPAddr("udp", s.cfg.PanelBackend)
		if err != nil {
			return nil, true, err
		}
		return addr, true, nil
	}

	ctx := context.Background()
	s.mu.Lock()
	if s.runCtx != nil {
		ctx = s.runCtx
	}
	s.mu.Unlock()
	ips, err := netguard.ResolvePublic(ctx, nil, sni)
	if err != nil {
		return nil, false, err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return &net.UDPAddr{IP: v4, Port: 443}, false, nil
		}
	}
	return nil, false, errors.New("no ipv4 result")
}

func (s *Server) relayBackendToClient(sess *session, key string) {
	buf := make([]byte, 65535)
	for {
		n, err := sess.backend.Read(buf)
		if err != nil {
			s.dropSession(key, sess)
			return
		}
		sess.mu.Lock()
		sess.lastActivity = time.Now()
		sess.mu.Unlock()

		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			s.dropSession(key, sess)
			return
		}
		if _, err := ln.WriteToUDP(buf[:n], sess.clientAddr); err != nil {
			s.dropSession(key, sess)
			return
		}
	}
}

// dropSession removes exactly the session generation the caller observed.
// A relay or GC pass can finish after the same client 4-tuple has already
// established a replacement session; CompareAndDelete prevents that stale
// cleanup from deleting the replacement or releasing its resource account.
func (s *Server) dropSession(key string, expected *session) bool {
	if expected == nil || !s.sessions.CompareAndDelete(key, expected) {
		return false
	}
	if expected.backend != nil {
		_ = expected.backend.Close()
	}
	s.releaseEstablished(expected.resourceIP)
	return true
}

func (s *Server) gcLoop() {
	defer s.loopWG.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	for {
		select {
		case <-ticker.C:
			if s.closed.Load() {
				return
			}
			s.reapIdle()
		case <-stopped:
			return
		}
	}
}

func normalizedIP(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return net.IP(v4).String()
	}
	return ip.String()
}

func (s *Server) tryReserveSession(key, resourceIP string) bool {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if s.closed.Load() || key == "" || resourceIP == "" {
		return false
	}
	if _, exists := s.pending[key]; exists {
		return false
	}
	if _, exists := s.sessions.Load(key); exists {
		return false
	}
	if len(s.pending) >= s.cfg.MaxConcurrentSetups || s.reserved >= s.cfg.MaxSessions {
		return false
	}
	if s.perIP[resourceIP] >= s.cfg.MaxSessionsPerIP {
		return false
	}
	s.pending[key] = resourceIP
	s.reserved++
	s.perIP[resourceIP]++
	return true
}

func (s *Server) promoteSession(key, resourceIP string, sess *session) bool {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if s.pending[key] != resourceIP {
		return false
	}
	delete(s.pending, key)
	if s.closed.Load() {
		s.releaseCountLocked(resourceIP)
		return false
	}
	s.sessions.Store(key, sess)
	return true
}

func (s *Server) releasePending(key, resourceIP string) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if s.pending[key] != resourceIP {
		return
	}
	delete(s.pending, key)
	s.releaseCountLocked(resourceIP)
}

func (s *Server) releaseEstablished(resourceIP string) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	s.releaseCountLocked(resourceIP)
}

func (s *Server) releaseCountLocked(resourceIP string) {
	if s.reserved > 0 {
		s.reserved--
	}
	if n := s.perIP[resourceIP]; n > 1 {
		s.perIP[resourceIP] = n - 1
	} else {
		delete(s.perIP, resourceIP)
	}
}

func (s *Server) reapIdle() {
	now := time.Now()
	type sessionRef struct {
		key  string
		sess *session
	}
	var stale []sessionRef
	s.sessions.Range(func(k, v any) bool {
		if sess, ok := v.(*session); ok {
			sess.mu.Lock()
			idle := now.Sub(sess.lastActivity)
			sess.mu.Unlock()
			if idle > s.cfg.IdleTimeout {
				stale = append(stale, sessionRef{key: k.(string), sess: sess})
			}
		}
		return true
	})
	for _, ref := range stale {
		s.dropSession(ref.key, ref.sess)
	}
}
