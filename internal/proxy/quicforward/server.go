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
}

// Server is a UDP :443 QUIC SNI-split forwarder.
type Server struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	listener *net.UDPConn
	sessions sync.Map // clientAddrString -> *session
	closed   atomic.Bool
	stopped  chan struct{}
	wg       sync.WaitGroup
}

// session tracks one client<->upstream UDP flow.
type session struct {
	clientAddr *net.UDPAddr
	backend    *net.UDPConn
	sni        string

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
	return &Server{cfg: cfg, logger: logger}
}

// Start binds the UDP listener and starts the read + gc loops. The
// listener is bound synchronously; a nil return means the socket is
// live.
func (s *Server) Start(ctx context.Context) error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("quicforward: resolve %s: %w", s.cfg.Listen, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("quicforward: listen %s: %w", s.cfg.Listen, err)
	}
	s.mu.Lock()
	s.listener = conn
	s.stopped = make(chan struct{})
	s.mu.Unlock()

	s.wg.Add(2)
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
	s.closed.Store(true)
	s.mu.Lock()
	ln := s.listener
	stopped := s.stopped
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if stopped != nil {
		select {
		case <-stopped:
		default:
			close(stopped)
		}
	}

	s.sessions.Range(func(k, v any) bool {
		if sess, ok := v.(*session); ok && sess.backend != nil {
			_ = sess.backend.Close()
		}
		s.sessions.Delete(k)
		return true
	})

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) readLoop() {
	defer s.wg.Done()
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

		// Cold path: copy the datagram and set up a session in a
		// goroutine so read loop stays responsive during DNS +
		// upstream dial.
		data := make([]byte, n)
		copy(data, buf[:n])
		go s.setupSession(data, clientAddr)
	}
}

// setupSession peeks the SNI, resolves an upstream, dials it, and
// registers the session. Only called for the first datagram of a
// new 4-tuple.
func (s *Server) setupSession(first []byte, clientAddr *net.UDPAddr) {
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

	sess := &session{
		clientAddr:   clientAddr,
		backend:      backend,
		sni:          sni,
		lastActivity: time.Now(),
	}
	key := clientAddr.String()

	// Race guard: two Initial datagrams from the same new client
	// might land here concurrently. Keep the first, drop the second.
	if actual, loaded := s.sessions.LoadOrStore(key, sess); loaded {
		_ = backend.Close()
		existing := actual.(*session)
		existing.mu.Lock()
		existing.lastActivity = time.Now()
		bc := existing.backend
		existing.mu.Unlock()
		if bc != nil {
			_, _ = bc.Write(first)
		}
		return
	}

	if _, err := backend.Write(first); err != nil {
		s.dropSession(key)
		return
	}

	s.wg.Add(1)
	go s.relayBackendToClient(sess, key)

	if !isLocal {
		s.logger.Debug("quicforward: new session", "sni", sni, "upstream", upstream.String())
	}
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

	ips, err := net.LookupIP(sni)
	if err != nil || len(ips) == 0 {
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
	defer s.wg.Done()
	buf := make([]byte, 65535)
	for {
		n, err := sess.backend.Read(buf)
		if err != nil {
			s.dropSession(key)
			return
		}
		sess.mu.Lock()
		sess.lastActivity = time.Now()
		sess.mu.Unlock()

		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			s.dropSession(key)
			return
		}
		if _, err := ln.WriteToUDP(buf[:n], sess.clientAddr); err != nil {
			s.dropSession(key)
			return
		}
	}
}

func (s *Server) dropSession(key string) {
	if v, ok := s.sessions.LoadAndDelete(key); ok {
		if sess, ok := v.(*session); ok && sess.backend != nil {
			_ = sess.backend.Close()
		}
	}
}

func (s *Server) gcLoop() {
	defer s.wg.Done()
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

func (s *Server) reapIdle() {
	now := time.Now()
	var stale []string
	s.sessions.Range(func(k, v any) bool {
		if sess, ok := v.(*session); ok {
			sess.mu.Lock()
			idle := now.Sub(sess.lastActivity)
			sess.mu.Unlock()
			if idle > s.cfg.IdleTimeout {
				stale = append(stale, k.(string))
			}
		}
		return true
	})
	for _, k := range stale {
		s.dropSession(k)
	}
}
