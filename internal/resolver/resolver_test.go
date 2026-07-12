package resolver

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// newFakeUpstream returns an Upstream whose TLSDial hands the resolver an
// in-memory net.Pipe. The server side answers with reply (a message the
// test supplies), so we can assert exactly what the resolver receives from
// a real DoT upstream without opening a socket. If received is non-nil,
// each forwarded query is deep-copied onto it (buffered) so tests can
// assert ECS strip / payload-size clamp without racing.
func newFakeUpstream(t *testing.T, reply func(*dns.Msg) *dns.Msg, received chan<- *dns.Msg) *Upstream {
	t.Helper()
	up := NewUpstream()
	up.Timeout = 2 * time.Second

	up.TLSDial = func(ctx context.Context, addr string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			for {
				var hdr [2]byte
				if _, err := io.ReadFull(server, hdr[:]); err != nil {
					return
				}
				n := binary.BigEndian.Uint16(hdr[:])
				buf := make([]byte, n)
				if _, err := io.ReadFull(server, buf); err != nil {
					return
				}
				q := new(dns.Msg)
				if err := q.Unpack(buf); err != nil {
					return
				}
				if received != nil {
					// Channel send establishes happens-before with the test
					// goroutine's channel receive; no shared-pointer races.
					select {
					case received <- q.Copy():
					default:
					}
				}

				resp := reply(q)
				packed, err := resp.Pack()
				if err != nil {
					return
				}
				var out [2]byte
				binary.BigEndian.PutUint16(out[:], uint16(len(packed)))
				if _, err := server.Write(out[:]); err != nil {
					return
				}
				if _, err := server.Write(packed); err != nil {
					return
				}
			}
		}()
		return client, nil
	}
	return up
}

func makeQuery(qname string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(qname), qtype)
	m.Id = 0x1234
	return m
}

// TestBlockReturnsNXDOMAIN — plan AC-R1.
func TestBlockReturnsNXDOMAIN(t *testing.T) {
	store := &Store{}
	tbl, err := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomain, Pattern: "ads.example.com", Action: "block", Priority: 1, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Publish(tbl)

	r := NewResolver(store, NewUpstream(), NewMetrics())
	resp, err := r.Resolve(context.Background(), makeQuery("ads.example.com", dns.TypeA), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN, got rcode=%d", resp.Rcode)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsBlock != 1 {
		t.Fatalf("HitsBlock=%d, want 1", snap.HitsBlock)
	}
}

// TestDirectForwardsToCNUpstream — plan AC-R2.
func TestDirectForwardsToCNUpstream(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "taobao.com", Action: "direct", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "1.2.3.4")}
		return m
	}, nil)
	r := NewResolver(store, up, NewMetrics())

	resp, err := r.Resolve(context.Background(), makeQuery("www.taobao.com", dns.TypeA), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("unexpected reply: %+v", resp)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsDirect != 1 {
		t.Fatalf("HitsDirect=%d, want 1", snap.HitsDirect)
	}
}

// TestProxyForwardsAndMissForwards covers AC-R3 (Proxy hit) + AC-R4 (no
// match falls through to proxy).
func TestProxyForwardsAndMissForwards(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "google.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, nil)
	r := NewResolver(store, up, NewMetrics())

	// Hit
	if _, err := r.Resolve(context.Background(), makeQuery("mail.google.com", dns.TypeA), nil); err != nil {
		t.Fatal(err)
	}
	// Miss (no rule) — should still go proxy per AC-R4.
	if _, err := r.Resolve(context.Background(), makeQuery("random.example.org", dns.TypeA), nil); err != nil {
		t.Fatal(err)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsProxy != 2 {
		t.Fatalf("HitsProxy=%d, want 2 (rule hit + miss default)", snap.HitsProxy)
	}
}

// TestAXFRRefused — plan Missing #4.
func TestAXFRRefused(t *testing.T) {
	store := &Store{}
	store.Publish(mustBuild(t, nil))
	r := NewResolver(store, NewUpstream(), NewMetrics())

	q := makeQuery("example.com", dns.TypeAXFR)
	resp, err := r.Resolve(context.Background(), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeRefused {
		t.Fatalf("AXFR: expected REFUSED, got rcode=%d", resp.Rcode)
	}
	if snap := r.Metrics.Snapshot(); snap.RefusedAXFR != 1 {
		t.Fatalf("RefusedAXFR=%d, want 1", snap.RefusedAXFR)
	}
}

func TestMultipleQuestionsReturnFORMERRWithoutForwarding(t *testing.T) {
	store := &Store{}
	store.Publish(mustBuild(t, []rules.Rule{
		{ID: "blocked", Kind: rules.KindDomain, Pattern: "blocked.example", Action: "block", Priority: 1, Enabled: true},
	}))

	var dials atomic.Int64
	up := NewUpstream()
	up.Proxy = []string{"must-not-dial:853"}
	up.TLSDial = func(context.Context, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected upstream dial")
	}
	r := NewResolver(store, up, NewMetrics())

	req := makeQuery("allowed.example", dns.TypeA)
	req.Question = append(req.Question, dns.Question{
		Name:   dns.Fqdn("blocked.example"),
		Qtype:  dns.TypeA,
		Qclass: dns.ClassINET,
	})
	resp, err := r.Resolve(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.Rcode != dns.RcodeFormatError {
		t.Fatalf("rcode = %s, want FORMERR", dns.RcodeToString[resp.Rcode])
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("upstream dials = %d, want 0", got)
	}
}

// TestEDNS0StripsClientSubnet — plan R14.
func TestEDNS0StripsClientSubnet(t *testing.T) {
	store := &Store{}
	store.Publish(mustBuild(t, nil))

	seen := make(chan *dns.Msg, 4)
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, seen)
	r := NewResolver(store, up, NewMetrics())

	q := makeQuery("example.com", dns.TypeA)
	// Attach EDNS0 with a client-subnet option — resolver must strip.
	o := new(dns.OPT)
	o.Hdr.Name = "."
	o.Hdr.Rrtype = dns.TypeOPT
	o.SetUDPSize(4096) // deliberately > 1232 to also test clamp
	ecs := &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.ParseIP("203.0.113.42")}
	o.Option = append(o.Option, ecs)
	q.Extra = append(q.Extra, o)

	if _, err := r.Resolve(context.Background(), q, nil); err != nil {
		t.Fatal(err)
	}

	var received *dns.Msg
	select {
	case received = <-seen:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("upstream never received the forwarded query")
	}
	opt := received.IsEdns0()
	if opt == nil {
		t.Fatal("upstream lost the OPT record entirely (should keep OPT, just strip ECS)")
	}
	for _, o := range opt.Option {
		if _, isECS := o.(*dns.EDNS0_SUBNET); isECS {
			t.Fatal("client-subnet leaked to upstream")
		}
	}
	if opt.UDPSize() > 1232 {
		t.Fatalf("UDP buffer size not clamped: %d", opt.UDPSize())
	}
}

// TestHotSwapPinsInFlightSnapshot — plan §4 Phase 1 hot-swap invariant.
// The resolver must see the OLD table if Publish races with a Resolve.
// We stall the upstream mid-response, Publish a NEW table (that would
// classify the same qname differently), release the upstream, and assert
// the old classification was already committed to.
func TestHotSwapPinsInFlightSnapshot(t *testing.T) {
	store := &Store{}
	// Old table: example.com -> Proxy
	oldTbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "example.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	// New table: example.com -> block (would give NXDOMAIN if the swap
	// actually reached the classifier mid-query).
	newTbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "example.com", Action: "block", Priority: 1, Enabled: true},
	})
	store.Publish(oldTbl)

	// Upstream that pauses until swapReleased is closed, so we can Publish
	// while the query is still in-flight.
	swapReleased := make(chan struct{})
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		<-swapReleased
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, nil)

	r := NewResolver(store, up, NewMetrics())

	done := make(chan struct {
		resp *dns.Msg
		err  error
	}, 1)
	go func() {
		resp, err := r.Resolve(context.Background(), makeQuery("host.example.com", dns.TypeA), nil)
		done <- struct {
			resp *dns.Msg
			err  error
		}{resp, err}
	}()

	// Give the resolver a beat to pin the old table + start forwarding.
	time.Sleep(30 * time.Millisecond)
	store.Publish(newTbl)
	close(swapReleased)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		// The Publish happened AFTER classify(); the resolver must have
		// used the old table (Proxy path, upstream forward) and NOT swap
		// to block mid-flight. So we expect a normal (NOERROR) reply.
		if r.resp.Rcode != dns.RcodeSuccess {
			t.Fatalf("in-flight query got %s; the Publish leaked into a running Resolve", dns.RcodeToString[r.resp.Rcode])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Resolve hung")
	}
}

// TestClassifyGlobalPriority matches dry-run/mihomo's sequential semantics:
// lower-priority-number rules win even when the competing matcher has a more
// specific kind or suffix.
func TestClassifyGlobalPriority(t *testing.T) {
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "broad", Kind: rules.KindDomainSuffix, Pattern: "example.com", Action: "direct", Priority: 10, Enabled: true},
		{ID: "specific", Kind: rules.KindDomainSuffix, Pattern: "sub.example.com", Action: "block", Priority: 100, Enabled: true},
		{ID: "exact-late", Kind: rules.KindDomain, Pattern: "host.example.com", Action: "block", Priority: 100, Enabled: true},
		{ID: "kw-early", Kind: rules.KindDomainKeyword, Pattern: "tracker", Action: "block", Priority: 5, Enabled: true},
		{ID: "final", Kind: rules.KindMatch, Pattern: "", Action: "direct", Priority: 1000, Enabled: true},
	})
	cases := []struct {
		name string
		want Action
	}{
		{"host.example.com", ActionDirect},    // earlier suffix beats later exact
		{"www.sub.example.com", ActionDirect}, // earlier broad suffix beats specific suffix
		{"tracker.example.com", ActionBlock},  // earlier keyword beats suffix
		{"nowhere.local", ActionDirect},       // default (MATCH)
	}
	for _, c := range cases {
		got := classify(tbl, c.name)
		if got != c.want {
			t.Errorf("classify(%q) = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestRaceOfTwoUpstream — exercises Upstream.Query's two-goroutine race.
// One target hangs forever, the other answers; the query must succeed.
func TestRaceOfTwoUpstream(t *testing.T) {
	up := NewUpstream()
	up.CN = []string{"fast:853", "slow:853"}
	up.Timeout = 2 * time.Second
	t.Cleanup(up.Close)

	var dialCount atomic.Int64
	bothDialed := make(chan struct{})
	up.TLSDial = func(ctx context.Context, addr string) (net.Conn, error) {
		if dialCount.Add(1) == 2 {
			close(bothDialed)
		}

		client, server := net.Pipe()
		if addr == "slow:853" {
			// The context passed to TLSDial is dial-scoped and is cancelled as
			// soon as the dial returns; it must not own the established
			// connection. Consume writes without answering until Upstream.Close.
			go func() {
				defer func() { _ = server.Close() }()
				_, _ = io.Copy(io.Discard, server)
			}()
			return client, nil
		}
		go func() {
			defer func() { _ = server.Close() }()
			// Do not let the fast answer win before the second query goroutine
			// has actually entered TLSDial; otherwise the dial-count assertion
			// measures scheduler order rather than concurrent-race behavior.
			<-bothDialed
			var hdr [2]byte
			if _, err := io.ReadFull(server, hdr[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint16(hdr[:])
			buf := make([]byte, n)
			if _, err := io.ReadFull(server, buf); err != nil {
				return
			}
			q := new(dns.Msg)
			_ = q.Unpack(buf)
			m := new(dns.Msg)
			m.SetReply(q)
			packed, _ := m.Pack()
			var out [2]byte
			binary.BigEndian.PutUint16(out[:], uint16(len(packed)))
			_, _ = server.Write(out[:])
			_, _ = server.Write(packed)
		}()
		return client, nil
	}

	resp, _, err := up.Query(context.Background(), makeQuery("example.com", dns.TypeA), "direct")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("race winner did not return success: %d", resp.Rcode)
	}
	if got := dialCount.Load(); got != 2 {
		t.Fatalf("expected 2 concurrent dials, got %d", got)
	}
}

func mustBuild(t *testing.T, rs []rules.Rule) *RuleTable {
	t.Helper()
	tbl, err := BuildTable(rs)
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func makeA(name, ip string) dns.RR {
	rr := new(dns.A)
	rr.Hdr = dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}
	rr.A = net.ParseIP(ip)
	return rr
}
