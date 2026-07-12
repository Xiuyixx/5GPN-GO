package frontdoor

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

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// Config controls which addresses the DNS plane's plain :53 UDP/TCP
// listeners bind to. The zero value is usable directly with New/Start —
// Frontdoor falls back to DefaultConfig()'s binds whenever both bind
// lists are left empty.
type Config struct {
	// BindUDP53 lists the addresses the UDP :53 listener binds to.
	// Default (see DefaultConfig): ["[::1]:53"] plus the WireGuard
	// interface IP when autodetection finds one; or ["[::]:53"] once
	// PublicPlainDNSEnabled is true.
	BindUDP53 []string

	// BindTCP53 mirrors BindUDP53 for the TCP :53 fallback listener.
	BindTCP53 []string

	// PublicPlainDNSEnabled, when true, allows a wildcard bind
	// (0.0.0.0:53 / [::]:53 / a bare ":53") to survive the safety
	// filter applied at Start(). When false (the default), any
	// wildcard address in BindUDP53/BindTCP53 is dropped rather than
	// bound — plain :53 is loopback + WireGuard only unless an
	// operator explicitly opts in to public exposure (plan §4 Phase 2,
	// R13 open-resolver mitigation).
	PublicPlainDNSEnabled bool

	// TLSConfigs supplies the four protocol-specific *tls.Config values
	// (see tlsconfigs.go) that DoQEnabled/DoH3Enabled need to bind their
	// encrypted listeners. Left nil, DoQ/DoH3 never start regardless of
	// the *Enabled flags — there is no cert material to hand quic-go /
	// http3.Server. Building it (BuildTLSConfigs against certmagic's
	// GetCertificate) is Phase 10's job; Frontdoor only consumes it.
	TLSConfigs *TLSConfigs

	// DoQEnabled toggles the :853/UDP DNS-over-QUIC listener (RFC 9250,
	// plan §4 Phase 9, AC-Q1/AC-Q2). Default false — DoQ ships gated
	// behind an explicit opt-in.
	DoQEnabled bool

	// DoQBind lists the address(es) the DoQ listener binds to; only the
	// first entry is used today (DoQ owns a single UDP socket, mirroring
	// DoT's single-Addr design). Empty means defaultDoQAddr ("[::]:853").
	DoQBind []string

	// DoH3Enabled toggles the [::]:443/UDP HTTP/3 DoH listener (plan §4
	// Phase 9, AC-Q1/AC-Q3). Default false, same opt-in posture as DoQ.
	DoH3Enabled bool

	// DoH3Bind mirrors DoQBind for the DoH3 listener. Empty means
	// defaultDoH3Addr ("[::]:443").
	DoH3Bind []string
}

// firstBindOrDefault returns binds[0] when non-empty, else def. DoQ and
// DoH3 each own a single UDP socket (unlike BindUDP53/BindTCP53's
// multi-address support), so only the first configured address is used.
func firstBindOrDefault(binds []string, def string) string {
	if len(binds) == 0 {
		return def
	}
	return binds[0]
}

// wireGuardIfacePrefixes lists the interface-name prefixes considered
// "the" WireGuard interface for DefaultConfig's autodetection.
var wireGuardIfacePrefixes = []string{"pgw-", "wg"}

// discoverWireGuardIP scans net.Interfaces() for the first interface
// whose name matches a WireGuard prefix and returns its first
// non-loopback, non-unspecified unicast IP. ok is false when none is
// found, or the interface list can't be read (e.g. a sandboxed test
// environment) — callers treat that the same as "no WireGuard bind
// available", never as a hard error.
func discoverWireGuardIP() (ip string, ok bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, iface := range ifaces {
		name := strings.ToLower(iface.Name)
		matched := false
		for _, prefix := range wireGuardIfacePrefixes {
			if strings.HasPrefix(name, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsUnspecified() {
				continue
			}
			return ipNet.IP.String(), true
		}
	}
	return "", false
}

// DefaultConfig returns the safe, non-public default bind set: IPv6
// loopback plus the WireGuard interface IP when autodetection finds
// one. It never returns a wildcard (0.0.0.0 / [::]) address — public
// exposure is opt-in via Config.PublicPlainDNSEnabled and is applied by
// the safety filter at Start(), not baked into this default.
func DefaultConfig() Config {
	binds := []string{"[::1]:53"}
	if ip, ok := discoverWireGuardIP(); ok {
		binds = append(binds, net.JoinHostPort(ip, "53"))
	}
	return Config{
		BindUDP53: binds,
		BindTCP53: append([]string(nil), binds...),
	}
}

// isWildcardBind reports whether addr's host is a wildcard/unspecified
// address (0.0.0.0, ::, or an empty host as in ":53") — the pattern
// that exposes a listener on every interface, including the public
// one.
func isWildcardBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// sanitizeBinds drops wildcard addresses from binds unless
// publicEnabled is true. This is defense in depth (plan R13): even if
// a misconfigured Config (or a future settings-layer bug) hands
// Frontdoor a public wildcard bind, plain :53 stays closed to the
// internet unless the operator has explicitly opted in.
func sanitizeBinds(binds []string, publicEnabled bool, logger *slog.Logger) []string {
	if publicEnabled {
		return binds
	}
	out := make([]string, 0, len(binds))
	for _, addr := range binds {
		if isWildcardBind(addr) {
			logger.Warn("frontdoor: dropping public wildcard bind; set public_plain_dns_enabled to allow it", "addr", addr)
			continue
		}
		out = append(out, addr)
	}
	return out
}

// Frontdoor owns the DNS plane's plain :53 UDP/TCP, DoT, DoQ, and DoH3
// listeners. The supervisor restarts only the plain listeners; encrypted
// listeners have independent accept loops. DoH rides the panel's HTTP/chi
// listener and is mounted separately.
type Frontdoor struct {
	resolver *resolver.Resolver
	logger   *slog.Logger

	// supervisor belongs to one successful Start lifecycle. A later Start
	// replaces it so a prior give-up cannot carry its terminal state or
	// consumed restart budget across Shutdown -> Start.
	supervisor *Supervisor

	mu      sync.Mutex
	cfg     Config
	udp     []*udpServer
	tcp     []*tcpServer
	dot     *DoT
	doq     *DoQ
	doh3    *DoH3
	started bool
	cancel  context.CancelFunc
	done    chan struct{}

	// degraded is flipped by enterDegraded (the supervisor's onGiveUp
	// callback, wired in Start) once the restart budget is exhausted.
	// ServeDNS checks it before ever touching the resolver — see
	// handler.go.
	degraded atomic.Bool
}

// ListenerState distinguishes an intentionally-disabled transport from a
// configured transport whose runtime listener is absent.
type ListenerState struct {
	Configured bool
	Running    bool
}

// Status is a lock-consistent snapshot used by the panel metrics endpoint.
type Status struct {
	UDP53    ListenerState
	TCP53    ListenerState
	DoT      ListenerState
	DoQ      ListenerState
	DoH3     ListenerState
	Degraded bool
}

func (fd *Frontdoor) Status() Status {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	udpBinds, tcpBinds := fd.effectiveBinds()
	tlsConfigured := fd.cfg.TLSConfigs != nil
	return Status{
		UDP53:    ListenerState{Configured: len(udpBinds) > 0, Running: len(fd.udp) > 0},
		TCP53:    ListenerState{Configured: len(tcpBinds) > 0, Running: len(fd.tcp) > 0},
		DoT:      ListenerState{Configured: tlsConfigured, Running: fd.dot != nil},
		DoQ:      ListenerState{Configured: tlsConfigured && fd.cfg.DoQEnabled, Running: fd.doq != nil},
		DoH3:     ListenerState{Configured: tlsConfigured && fd.cfg.DoH3Enabled, Running: fd.doh3 != nil},
		Degraded: fd.degraded.Load(),
	}
}

// New wires a Frontdoor around an existing resolver. logger may be nil
// (slog.Default() is used). cfg is not validated or bound to sockets
// until Start.
func New(cfg Config, resolver *resolver.Resolver, logger *slog.Logger) *Frontdoor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Frontdoor{
		cfg:      cfg,
		resolver: resolver,
		logger:   logger,
	}
}

// effectiveBinds resolves fd.cfg into the addresses Start actually
// binds to: DefaultConfig()'s binds when the caller left both lists
// empty, then the wildcard safety filter (sanitizeBinds). Caller must
// hold fd.mu.
func (fd *Frontdoor) effectiveBinds() (udpAddrs, tcpAddrs []string) {
	udpAddrs, tcpAddrs = fd.cfg.BindUDP53, fd.cfg.BindTCP53
	if len(udpAddrs) == 0 && len(tcpAddrs) == 0 {
		def := DefaultConfig()
		udpAddrs, tcpAddrs = def.BindUDP53, def.BindTCP53
	}
	return sanitizeBinds(udpAddrs, fd.cfg.PublicPlainDNSEnabled, fd.logger),
		sanitizeBinds(tcpAddrs, fd.cfg.PublicPlainDNSEnabled, fd.logger)
}

// bindLocked binds every listener in fd.effectiveBinds(), rolling back
// (closing) any partial binds on the first failure, and commits the
// result to fd.udp/fd.tcp only on full success. Caller must hold fd.mu.
func (fd *Frontdoor) bindLocked(ctx context.Context) error {
	udpAddrs, tcpAddrs := fd.effectiveBinds()

	udp := make([]*udpServer, 0, len(udpAddrs))
	for _, addr := range udpAddrs {
		s := newUDPServer(fd, addr)
		if err := s.listen(ctx); err != nil {
			shutdownUDP(udp)
			return err
		}
		udp = append(udp, s)
	}

	tcp := make([]*tcpServer, 0, len(tcpAddrs))
	for _, addr := range tcpAddrs {
		s := newTCPServer(fd, addr)
		if err := s.listen(ctx); err != nil {
			shutdownUDP(udp)
			shutdownTCP(tcp)
			return err
		}
		tcp = append(tcp, s)
	}

	fd.udp, fd.tcp = udp, tcp
	return nil
}

// closePlainListenersLocked shuts down and clears the supervised UDP/TCP
// listeners. Encrypted DNS listeners have independent accept loops and must
// not be torn down when an unrelated plain listener crashes.
// Caller must hold fd.mu.
func (fd *Frontdoor) closePlainListenersLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range fd.udp {
		_ = s.Shutdown(ctx)
	}
	for _, s := range fd.tcp {
		_ = s.Shutdown(ctx)
	}
	fd.udp, fd.tcp = nil, nil
}

// closeListenersLocked shuts down and clears every currently-bound listener.
// It is reserved for full Frontdoor teardown and initial-start rollback.
// Caller must hold fd.mu.
func (fd *Frontdoor) closeListenersLocked() {
	fd.closePlainListenersLocked()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fd.stopDoTLocked(ctx)
	fd.stopDoQLocked(ctx)
	fd.stopDoH3Locked(ctx)
}

func shutdownUDP(servers []*udpServer) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(ctx)
	}
}

func shutdownTCP(servers []*tcpServer) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(ctx)
	}
}

// startDoQLocked builds and starts fd.doq from cfg.TLSConfigs.DoQ when not
// already running. Caller must hold fd.mu. A nil cfg.TLSConfigs is not an
// error — DoQ simply doesn't start, since there is no cert material to
// hand quic-go (Phase 10 is responsible for building TLSConfigs).
func (fd *Frontdoor) startDoQLocked(ctx context.Context, cfg Config) error {
	if fd.doq != nil || cfg.TLSConfigs == nil {
		return nil
	}
	addr := firstBindOrDefault(cfg.DoQBind, defaultDoQAddr)
	d := NewDoQ(addr, cfg.TLSConfigs.DoQ, fd.resolver, fd.logger)
	if err := d.Start(ctx); err != nil {
		return fmt.Errorf("doq: %w", err)
	}
	fd.doq = d
	return nil
}

// startDoH3Locked mirrors startDoQLocked for the HTTP/3 DoH listener. It
// builds a fresh *DoH handler around fd.resolver — DoH is a stateless
// wrapper (see doh.go), so a new instance here behaves identically to the
// one the panel mounts on its own chi router.
func (fd *Frontdoor) startDoH3Locked(ctx context.Context, cfg Config) error {
	if fd.doh3 != nil || cfg.TLSConfigs == nil {
		return nil
	}
	addr := firstBindOrDefault(cfg.DoH3Bind, defaultDoH3Addr)
	doh := NewDoH(fd.resolver, fd.logger)
	d3 := NewDoH3(addr, cfg.TLSConfigs.DoH3, doh, fd.logger)
	if err := d3.Start(ctx); err != nil {
		return fmt.Errorf("doh3: %w", err)
	}
	fd.doh3 = d3
	return nil
}

// stopDoQLocked shuts down and clears fd.doq, if running. Caller must hold
// fd.mu.
func (fd *Frontdoor) stopDoQLocked(ctx context.Context) {
	if fd.doq == nil {
		return
	}
	if err := fd.doq.Shutdown(ctx); err != nil {
		fd.logger.Warn("frontdoor: doq shutdown", "error", err)
	}
	fd.doq = nil
}

// stopDoH3Locked mirrors stopDoQLocked for fd.doh3.
func (fd *Frontdoor) stopDoH3Locked(ctx context.Context) {
	if fd.doh3 == nil {
		return
	}
	if err := fd.doh3.Shutdown(ctx); err != nil {
		fd.logger.Warn("frontdoor: doh3 shutdown", "error", err)
	}
	fd.doh3 = nil
}

// startEncryptedLocked starts DoT (always, when TLSConfigs is available),
// plus DoQ/DoH3 per cfg's *Enabled flags. DoT is the core encrypted-DNS
// entry point (iOS profile depends on it) — treating it as a feature flag
// like DoQ/DoH3 creates a chicken-and-egg with iOS Preflight, so as long
// as TLSConfigs.DoT is populated (i.e. certmagic is running) DoT binds
// unconditionally. On a bind failure, whichever listener already started
// is torn back down so Start never leaves a half-started encrypted
// listener trio behind. Caller must hold fd.mu.
func (fd *Frontdoor) startEncryptedLocked(ctx context.Context, cfg Config) error {
	if err := fd.startDoTLocked(ctx, cfg); err != nil {
		return err
	}
	if cfg.DoQEnabled {
		if err := fd.startDoQLocked(ctx, cfg); err != nil {
			fd.stopDoTLocked(context.Background())
			return err
		}
	}
	if cfg.DoH3Enabled {
		if err := fd.startDoH3Locked(ctx, cfg); err != nil {
			fd.stopDoQLocked(context.Background())
			fd.stopDoTLocked(context.Background())
			return err
		}
	}
	return nil
}

// startDoTLocked builds and starts fd.dot from cfg.TLSConfigs.DoT when not
// already running. Caller must hold fd.mu. Nil TLSConfigs is a no-op — the
// panel-only test path never wires certmagic and doesn't need DoT.
func (fd *Frontdoor) startDoTLocked(ctx context.Context, cfg Config) error {
	if fd.dot != nil || cfg.TLSConfigs == nil {
		return nil
	}
	d := NewDoT(defaultDoTAddr, cfg.TLSConfigs.DoT, fd.resolver, fd.logger)
	if err := d.Start(ctx); err != nil {
		return fmt.Errorf("frontdoor: dot: %w", err)
	}
	fd.dot = d
	return nil
}

// stopDoTLocked shuts down and clears fd.dot, if running. Caller must hold fd.mu.
func (fd *Frontdoor) stopDoTLocked(ctx context.Context) {
	if fd.dot == nil {
		return
	}
	if err := fd.dot.Shutdown(ctx); err != nil {
		fd.logger.Warn("frontdoor: dot shutdown", "error", err)
	}
	fd.dot = nil
}

// Start binds every configured listener and returns immediately after the
// initial bind succeeds. Plain UDP/TCP listeners are handed to the bounded
// supervisor in supervisor.go; encrypted listeners run independently.
func (fd *Frontdoor) Start(ctx context.Context) error {
	fd.mu.Lock()
	if fd.started {
		fd.mu.Unlock()
		return errors.New("frontdoor: already started")
	}
	if err := fd.bindLocked(ctx); err != nil {
		fd.mu.Unlock()
		return fmt.Errorf("frontdoor: start: %w", err)
	}
	if err := fd.startEncryptedLocked(ctx, fd.cfg); err != nil {
		fd.closeListenersLocked()
		fd.mu.Unlock()
		return fmt.Errorf("frontdoor: start: %w", err)
	}
	// A supervisor's give-up state is intentionally terminal for that
	// supervisor instance. Starting Frontdoor again is a new lifecycle, so it
	// must receive a fresh restart window and leave the prior degraded state.
	supervisor := NewSupervisor(fd.logger)
	fd.supervisor = supervisor
	fd.degraded.Store(false)
	fd.started = true
	runCtx, cancel := context.WithCancel(context.Background())
	fd.cancel = cancel
	fd.done = make(chan struct{})
	done := fd.done
	fd.mu.Unlock()

	go func() {
		defer close(done)
		_ = supervisor.Run(runCtx, fd.serveAll, fd.enterDegraded)
	}()
	return nil
}

// serveAll is the task the supervisor watches. Its first invocation
// (from Start) reuses the listeners bound there; every subsequent
// invocation (a restart after a crash) rebinds from scratch — full
// re-bind semantics per plan §4 Phase 2 Round 3 "Interpretation A", so
// a restart always picks up current config rather than reusing
// possibly-broken sockets. It returns nil on a clean stop (ctx
// cancelled via Shutdown) or a non-nil error on crash, which the
// supervisor treats as "restart me".
func (fd *Frontdoor) serveAll(ctx context.Context) error {
	fd.mu.Lock()
	if len(fd.udp) == 0 && len(fd.tcp) == 0 {
		if err := fd.bindLocked(ctx); err != nil {
			fd.mu.Unlock()
			return err
		}
	}
	udp := append([]*udpServer(nil), fd.udp...)
	tcp := append([]*tcpServer(nil), fd.tcp...)
	fd.mu.Unlock()

	errCh := make(chan error, len(udp)+len(tcp))
	var wg sync.WaitGroup
	for _, s := range udp {
		wg.Add(1)
		go func(s *udpServer) { defer wg.Done(); errCh <- s.serve() }(s)
	}
	for _, s := range tcp {
		wg.Add(1)
		go func(s *tcpServer) { defer wg.Done(); errCh <- s.serve() }(s)
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()

	select {
	case <-ctx.Done():
		fd.mu.Lock()
		fd.closePlainListenersLocked()
		fd.mu.Unlock()
		<-allDone
		return nil
	case err := <-errCh:
		// Treat this as a crash: tear down whatever else is still
		// running so the next supervised attempt starts from a clean
		// slate and rebinds the plain listener set. Encrypted DNS listeners
		// own independent accept loops and remain available.
		fd.mu.Lock()
		fd.closePlainListenersLocked()
		fd.mu.Unlock()
		<-allDone
		return err
	}
}

// enterDegraded is the plain-listener supervisor's give-up callback. The
// crashed plain sockets have already been closed, so this flag records the
// degraded state and protects any later direct ServeDNS call from reaching
// the resolver; it does not keep the failed sockets available.
func (fd *Frontdoor) enterDegraded() {
	fd.degraded.Store(true)
}

// Shutdown stops the supervised plain listener tree and waits for it to
// finish, or for ctx to expire first. DoT/DoQ/DoH3 are also shut down,
// independent of whether the plain supervisor tree was ever started.
func (fd *Frontdoor) Shutdown(ctx context.Context) error {
	fd.mu.Lock()
	started := fd.started
	fd.started = false
	cancel := fd.cancel
	done := fd.done
	fd.stopDoTLocked(ctx)
	fd.stopDoQLocked(ctx)
	fd.stopDoH3Locked(ctx)
	fd.mu.Unlock()

	if !started {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reconcile applies a new Config at runtime. DoQEnabled/DoH3Enabled
// transitions start or stop their respective encrypted listeners
// in-place, without a process restart (plan §4 Phase 9, AC-Q1). Plain
// :53 UDP/TCP bind changes are still deferred to the next Start (Phase 2
// stub behavior, unchanged by this phase).
//
// On a bind failure for either listener, that listener's *Enabled flag
// is reverted to false in fd.cfg before returning the error — Reconcile
// never leaves Frontdoor believing a listener is enabled when it isn't
// actually bound, and never tears down an already-working listener to
// make room for a failed one (plan §4 Phase 9 Reconcile failure-path
// note).
func (fd *Frontdoor) Reconcile(ctx context.Context, cfg Config) error {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	prev := fd.cfg
	fd.cfg = cfg

	if cfg.DoQEnabled && !prev.DoQEnabled {
		if err := fd.startDoQLocked(ctx, cfg); err != nil {
			fd.cfg.DoQEnabled = false
			return fmt.Errorf("frontdoor: reconcile: %w", err)
		}
	} else if !cfg.DoQEnabled && prev.DoQEnabled {
		fd.stopDoQLocked(ctx)
	}

	if cfg.DoH3Enabled && !prev.DoH3Enabled {
		if err := fd.startDoH3Locked(ctx, cfg); err != nil {
			fd.cfg.DoH3Enabled = false
			return fmt.Errorf("frontdoor: reconcile: %w", err)
		}
	} else if !cfg.DoH3Enabled && prev.DoH3Enabled {
		fd.stopDoH3Locked(ctx)
	}

	return nil
}
