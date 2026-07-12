package frontdoor

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// testLogger is defined in dot_test.go (Phase 3, already landed in this
// package) and reused here.

// newLiveResolver builds a *resolver.Resolver with an empty rule table
// (every qname falls through to the default proxy action, AC-R4)
// backed by up — mirrors newTestDoH's resolver setup in doh_test.go,
// but returns the Resolver itself since Frontdoor.New needs one, not a
// DoH handler.
func newLiveResolver(t *testing.T, up *resolver.Upstream) *resolver.Resolver {
	t.Helper()
	store := &resolver.Store{}
	tbl, err := resolver.BuildTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	store.Publish(tbl)
	return resolver.NewResolver(store, up, resolver.NewMetrics())
}

// startTestFrontdoor starts a Frontdoor bound only to 127.0.0.1:0
// ephemeral ports for whichever of udp/tcp is requested — never to
// DefaultConfig()'s real :53, which would require root and isn't
// necessary to exercise this package's behavior.
func startTestFrontdoor(t *testing.T, res *resolver.Resolver, udp, tcp bool) *Frontdoor {
	t.Helper()
	var cfg Config
	if udp {
		cfg.BindUDP53 = []string{"127.0.0.1:0"}
	}
	if tcp {
		cfg.BindTCP53 = []string{"127.0.0.1:0"}
	}
	fd := New(cfg, res, testLogger())
	if err := fd.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = fd.Shutdown(ctx)
	})
	return fd
}

// testUDPAddr/testTCPAddr expose the OS-assigned ephemeral port bound
// by startTestFrontdoor — test-only accessors into the unexported
// fd.udp/fd.tcp slices (legal since this file is part of package
// frontdoor, not a black-box _test package).
func (fd *Frontdoor) testUDPAddr(t *testing.T) string {
	t.Helper()
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.udp) == 0 {
		t.Fatal("no udp listener bound")
	}
	return fd.udp[0].LocalAddr().String()
}

func (fd *Frontdoor) testTCPAddr(t *testing.T) string {
	t.Helper()
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.tcp) == 0 {
		t.Fatal("no tcp listener bound")
	}
	return fd.tcp[0].LocalAddr().String()
}

func TestFrontdoor_UDP_QueryAnswers(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "203.0.113.10")}
		return m
	})
	fd := startTestFrontdoor(t, newLiveResolver(t, up), true, false)

	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	q := makeQuery("udp.example.com", dns.TypeA)
	resp, _, err := c.Exchange(q, fd.testUDPAddr(t))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestFrontdoor_TCP_QueryAnswers(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "203.0.113.11")}
		return m
	})
	fd := startTestFrontdoor(t, newLiveResolver(t, up), false, true)

	c := &dns.Client{Net: "tcp", Timeout: 3 * time.Second}
	q := makeQuery("tcp.example.com", dns.TypeA)
	resp, _, err := c.Exchange(q, fd.testTCPAddr(t))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestFrontdoor_PanicRecovery_ReturnsSERVFAILAndSurvives(t *testing.T) {
	// resolver.NewResolver with a nil Store panics on the first
	// non-AXFR query: Store.Load() has a pointer receiver and
	// dereferences a field on it, so calling it with a nil *Store
	// (NewResolver does not substitute a default the way it does for a
	// nil Metrics) is a genuine nil-pointer-dereference panic — a
	// realistic "bug in the resolver" stand-in using the exact
	// *resolver.Resolver type Frontdoor requires, no mock seam needed.
	panicking := resolver.NewResolver(nil, nil, nil)
	fd := startTestFrontdoor(t, panicking, true, false)

	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	addr := fd.testUDPAddr(t)

	// Two round trips: the first proves the panic is recovered as
	// SERVFAIL instead of the process dying; the second proves the
	// listener goroutine tree survived the first panic and keeps
	// serving.
	for i := 0; i < 2; i++ {
		q := makeQuery("panic.example.com", dns.TypeA)
		resp, _, err := c.Exchange(q, addr)
		if err != nil {
			t.Fatalf("exchange %d: %v (server should have survived the panic)", i, err)
		}
		if resp.Rcode != dns.RcodeServerFailure {
			t.Fatalf("exchange %d: rcode = %d, want SERVFAIL", i, resp.Rcode)
		}
	}
}

func TestFrontdoor_DegradedMode_ShortCircuitsToServfail(t *testing.T) {
	// A resolver that would otherwise answer successfully — proves
	// degraded mode intercepts before the resolver is ever reached.
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "203.0.113.20")}
		return m
	})
	fd := startTestFrontdoor(t, newLiveResolver(t, up), true, false)

	// Invoke the exact callback Frontdoor.Start wires into
	// Supervisor.Run as onGiveUp — see
	// TestSupervisor_GivesUpAfterFiveRestartsWithin60s for coverage of
	// the bounded-retry bookkeeping that triggers this in production.
	fd.enterDegraded()

	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	q := makeQuery("degraded.example.com", dns.TypeA)
	resp, _, err := c.Exchange(q, fd.testUDPAddr(t))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode = %d, want SERVFAIL (degraded mode)", resp.Rcode)
	}
}

func TestFrontdoor_StartAfterGiveUpResetsSupervisorAndDegraded(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "203.0.113.21")}
		return m
	})
	fd := startTestFrontdoor(t, newLiveResolver(t, up), true, false)
	c := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	q := makeQuery("restarted.example.com", dns.TypeA)
	if _, _, err := c.Exchange(q, fd.testUDPAddr(t)); err != nil {
		t.Fatalf("exchange before give-up: %v", err)
	}

	fd.mu.Lock()
	oldSupervisor := fd.supervisor
	fd.mu.Unlock()
	if oldSupervisor == nil {
		t.Fatal("first Start did not install a supervisor")
	}

	// Model the persistent give-up fields that matter to a later Start. The
	// supervisor's own give-up/run termination is covered by
	// TestSupervisor_GivesUpAfterFiveRestartsWithin60s; this regression pins
	// Frontdoor's lifecycle boundary without waiting through all backoffs.
	oldSupervisor.mu.Lock()
	oldSupervisor.givenUp = true
	oldSupervisor.restarts = make([]time.Time, MaxRestartsPerWindow+1)
	for i := range oldSupervisor.restarts {
		oldSupervisor.restarts[i] = time.Now()
	}
	oldSupervisor.mu.Unlock()
	fd.enterDegraded()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := fd.Shutdown(ctx); err != nil {
		cancel()
		t.Fatalf("Shutdown after give-up: %v", err)
	}
	cancel()

	if err := fd.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	fd.mu.Lock()
	newSupervisor := fd.supervisor
	fd.mu.Unlock()
	if newSupervisor == nil || newSupervisor == oldSupervisor {
		t.Fatal("second Start reused the exhausted supervisor")
	}
	if newSupervisor.GivenUp() {
		t.Fatal("new supervisor inherited given-up state")
	}
	newSupervisor.mu.Lock()
	restarts := len(newSupervisor.restarts)
	newSupervisor.mu.Unlock()
	if restarts != 0 {
		t.Fatalf("new supervisor inherited %d restart timestamps", restarts)
	}
	if fd.Status().Degraded {
		t.Fatal("second Start retained degraded state")
	}

	resp, _, err := c.Exchange(q, fd.testUDPAddr(t))
	if err != nil {
		t.Fatalf("exchange after second Start: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response after second Start: %+v", resp)
	}
}

func TestServeAll_PlainCrashKeepsEncryptedListeners(t *testing.T) {
	fd := New(Config{}, nil, testLogger())
	// A udpServer without listen() deterministically makes serve() fail. The
	// encrypted placeholders need no live sockets: this test pins ownership,
	// namely that a plain listener crash must not shut them down or clear them.
	fd.udp = []*udpServer{{fd: fd, addr: "broken"}}
	dot := &DoT{}
	doq := &DoQ{}
	doh3 := &DoH3{}
	fd.dot, fd.doq, fd.doh3 = dot, doq, doh3

	if err := fd.serveAll(context.Background()); err == nil {
		t.Fatal("serveAll returned nil for an uninitialised UDP server")
	}
	if fd.dot != dot || fd.doq != doq || fd.doh3 != doh3 {
		t.Fatal("plain listener crash tore down an encrypted DNS listener")
	}
	if len(fd.udp) != 0 || len(fd.tcp) != 0 {
		t.Fatal("plain listeners were not cleared after their supervised task crashed")
	}
}

func TestSupervisor_GivesUpAfterFiveRestartsWithin60s(t *testing.T) {
	sv := NewSupervisor(testLogger())

	var calls, giveUps int
	task := func(ctx context.Context) error {
		calls++
		if calls <= 6 {
			panic("simulated listener crash")
		}
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := sv.Run(ctx, task, func() { giveUps++ })
	if !errors.Is(err, ErrSupervisorGaveUp) {
		t.Fatalf("Run error = %v, want ErrSupervisorGaveUp", err)
	}
	if calls != 6 {
		t.Fatalf("task invoked %d times, want 6 (5 restarts + the 6th give-up crash)", calls)
	}
	if giveUps != 1 {
		t.Fatalf("onGiveUp invoked %d times, want exactly 1", giveUps)
	}
	if !sv.GivenUp() {
		t.Fatal("GivenUp() = false after giving up")
	}
}

func TestDefaultConfig_IsLoopbackOnlySafe(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.BindUDP53) == 0 {
		t.Fatal("DefaultConfig: no udp binds")
	}
	if cfg.BindUDP53[0] != "[::1]:53" {
		t.Fatalf("DefaultConfig: first udp bind = %q, want [::1]:53", cfg.BindUDP53[0])
	}
	if cfg.PublicPlainDNSEnabled {
		t.Fatal("DefaultConfig: PublicPlainDNSEnabled should default false")
	}
	for _, addr := range cfg.BindUDP53 {
		if isWildcardBind(addr) {
			t.Fatalf("DefaultConfig: wildcard bind %q leaked into the safe default", addr)
		}
	}
}

func TestSanitizeBinds_DropsWildcardUnlessPublicEnabled(t *testing.T) {
	logger := testLogger()
	binds := []string{"[::1]:53", "[::]:53", "0.0.0.0:53", "10.7.0.1:53"}

	got := sanitizeBinds(binds, false, logger)
	want := []string{"[::1]:53", "10.7.0.1:53"}
	if !slices.Equal(got, want) {
		t.Fatalf("sanitizeBinds(public=false) = %v, want %v", got, want)
	}

	got = sanitizeBinds(binds, true, logger)
	if !slices.Equal(got, binds) {
		t.Fatalf("sanitizeBinds(public=true) = %v, want unchanged %v", got, binds)
	}
}
