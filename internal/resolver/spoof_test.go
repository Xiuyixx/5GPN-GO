package resolver

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

func spoofTestQuery(name string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	return m
}

// TestSpoof_AProxyMatchReturnsGatewayIP is the golden path: a proxy-
// classified A query under scope=all returns the gateway IP without
// forwarding to any upstream and increments HitsSpoof.
func TestSpoof_AProxyMatchReturnsGatewayIP(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "openai.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	// Sentinel upstream: if the resolver ever forwards this query, the
	// test fails immediately — spoof must short-circuit the forward path.
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		t.Fatalf("upstream received a query that should have been spoofed: %s", q.Question[0].Name)
		return nil
	}, nil)
	r := NewResolver(store, up, NewMetrics())

	gateway := net.ParseIP("177.0.143.27")
	r.SetSpoofPolicy(&SpoofPolicy{
		ServerIP4: gateway,
		Scope:     SpoofScopeAll,
		TTL:       120,
	})

	resp, err := r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeA), net.ParseIP("100.64.0.1"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %s, want NOERROR", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("Answer count = %d, want 1", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("Answer[0] type = %T, want *dns.A", resp.Answer[0])
	}
	if !a.A.Equal(gateway) {
		t.Fatalf("A = %s, want %s", a.A, gateway)
	}
	if a.Hdr.Ttl != 120 {
		t.Fatalf("TTL = %d, want 120", a.Hdr.Ttl)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 1 || snap.HitsProxy != 0 {
		t.Fatalf("counters: spoof=%d proxy=%d, want spoof=1 proxy=0", snap.HitsSpoof, snap.HitsProxy)
	}
}

// TestSpoof_AAAAWithoutV6FallsThroughToUpstream — a policy that only
// configures ServerIP4 must NOT spoof AAAA queries; they forward
// normally so a v4-only gateway doesn't black-hole v6 clients.
func TestSpoof_AAAAWithoutV6FallsThroughToUpstream(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "openai.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	forwarded := false
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		forwarded = true
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, nil)
	r := NewResolver(store, up, NewMetrics())
	r.SetSpoofPolicy(&SpoofPolicy{
		ServerIP4: net.ParseIP("177.0.143.27"),
		// ServerIP6 deliberately nil.
		Scope: SpoofScopeAll,
	})

	if _, err := r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeAAAA), nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !forwarded {
		t.Fatal("AAAA without ServerIP6 must forward, not spoof")
	}
	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 0 || snap.HitsProxy != 1 {
		t.Fatalf("counters: spoof=%d proxy=%d, want spoof=0 proxy=1", snap.HitsSpoof, snap.HitsProxy)
	}
}

// TestSpoof_AAAAWithV6ReturnsGatewayV6 — when ServerIP6 is set, AAAA
// is spoofed the same way A is.
func TestSpoof_AAAAWithV6ReturnsGatewayV6(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "openai.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	r := NewResolver(store, NewUpstream(), NewMetrics())
	gwv6 := net.ParseIP("2001:db8::1")
	r.SetSpoofPolicy(&SpoofPolicy{
		ServerIP6: gwv6,
		Scope:     SpoofScopeAll,
	})

	resp, err := r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeAAAA), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("Answer count = %d, want 1", len(resp.Answer))
	}
	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	if !ok || !aaaa.AAAA.Equal(gwv6) {
		t.Fatalf("Answer[0] = %+v, want AAAA %s", resp.Answer[0], gwv6)
	}
}

// TestSpoof_DirectAndBlockUnaffected — spoof policy applies only on
// the "proxy" classification path. Block still NXDOMAINs, direct
// still forwards to the CN upstream.
func TestSpoof_DirectAndBlockUnaffected(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "b", Kind: rules.KindDomainSuffix, Pattern: "ads.example.com", Action: "block", Priority: 1, Enabled: true},
		{ID: "d", Kind: rules.KindDomainSuffix, Pattern: "taobao.com", Action: "direct", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	directHit := false
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		directHit = true
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("1.1.1.1"),
		}}
		return m
	}, nil)

	r := NewResolver(store, up, NewMetrics())
	r.SetSpoofPolicy(&SpoofPolicy{ServerIP4: net.ParseIP("177.0.143.27"), Scope: SpoofScopeAll})

	// block -> NXDOMAIN, no spoof
	if resp, err := r.Resolve(context.Background(), spoofTestQuery("ads.example.com", dns.TypeA), nil); err != nil {
		t.Fatal(err)
	} else if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("block: rcode = %s, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
	}

	// direct -> forward, no spoof
	if _, err := r.Resolve(context.Background(), spoofTestQuery("www.taobao.com", dns.TypeA), nil); err != nil {
		t.Fatal(err)
	}
	if !directHit {
		t.Fatal("direct classification did not reach upstream")
	}

	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 0 {
		t.Fatalf("HitsSpoof = %d, want 0 for block+direct paths", snap.HitsSpoof)
	}
}

// TestSpoof_PrivateOnlyRespectsCIDR — scope=private_only spoofs a
// client inside AllowCIDR and forwards for one outside it. Also
// covers the nil-client failsafe: unknown client under private_only
// must NOT be spoofed.
func TestSpoof_PrivateOnlyRespectsCIDR(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "openai.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, nil)
	r := NewResolver(store, up, NewMetrics())

	r.SetSpoofPolicy(&SpoofPolicy{
		ServerIP4: net.ParseIP("177.0.143.27"),
		Scope:     SpoofScopePrivateOnly,
		AllowCIDR: ParseCIDRs([]string{"172.22.0.0/16"}),
	})

	// Inside CIDR -> spoof.
	resp, _ := r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeA), net.ParseIP("172.22.0.5"))
	if a, ok := resp.Answer[0].(*dns.A); !ok || !a.A.Equal(net.ParseIP("177.0.143.27")) {
		t.Fatalf("in-CIDR client was not spoofed: %+v", resp.Answer)
	}

	// Outside CIDR -> forward.
	_, _ = r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeA), net.ParseIP("8.8.8.8"))
	// Unknown client -> also forward (nil fails closed under private_only).
	_, _ = r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeA), nil)

	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 1 || snap.HitsProxy != 2 {
		t.Fatalf("counters: spoof=%d proxy=%d, want spoof=1 proxy=2", snap.HitsSpoof, snap.HitsProxy)
	}
}

// TestSpoof_ZeroTTLDefaults — an operator that leaves TTL zero gets
// the defaultSpoofTTL floor, not a zero-TTL answer.
func TestSpoof_ZeroTTLDefaults(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable(nil)
	store.Publish(tbl)

	r := NewResolver(store, NewUpstream(), NewMetrics())
	r.SetSpoofPolicy(&SpoofPolicy{
		ServerIP4: net.ParseIP("177.0.143.27"),
		Scope:     SpoofScopeAll,
		// TTL: 0 — expect defaultSpoofTTL.
	})

	// No rules — falls through to default proxy action.
	resp, err := r.Resolve(context.Background(), spoofTestQuery("random.example.org", dns.TypeA), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("Answer count = %d", len(resp.Answer))
	}
	if got := resp.Answer[0].Header().Ttl; got != defaultSpoofTTL {
		t.Fatalf("TTL = %d, want %d", got, defaultSpoofTTL)
	}
}

// TestSpoof_LivePolicySwap — a SetSpoofPolicy call between two Resolve
// invocations must take effect on the second call. This pins the
// atomic-pointer swap contract.
func TestSpoof_LivePolicySwap(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable(nil)
	store.Publish(tbl)

	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, nil)
	r := NewResolver(store, up, NewMetrics())

	// No policy: proxy path forwards.
	if _, err := r.Resolve(context.Background(), spoofTestQuery("a.example.org", dns.TypeA), nil); err != nil {
		t.Fatal(err)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 0 || snap.HitsProxy != 1 {
		t.Fatalf("initial: spoof=%d proxy=%d, want 0/1", snap.HitsSpoof, snap.HitsProxy)
	}

	// Publish policy: next call spoofs.
	r.SetSpoofPolicy(&SpoofPolicy{ServerIP4: net.ParseIP("177.0.143.27"), Scope: SpoofScopeAll})
	resp, err := r.Resolve(context.Background(), spoofTestQuery("a.example.org", dns.TypeA), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("spoofed reply had no Answer: %+v", resp)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 1 || snap.HitsProxy != 1 {
		t.Fatalf("post-swap: spoof=%d proxy=%d, want 1/1", snap.HitsSpoof, snap.HitsProxy)
	}

	// Nil policy: reverts to forward.
	r.SetSpoofPolicy(nil)
	if _, err := r.Resolve(context.Background(), spoofTestQuery("a.example.org", dns.TypeA), nil); err != nil {
		t.Fatal(err)
	}
	if snap := r.Metrics.Snapshot(); snap.HitsSpoof != 1 || snap.HitsProxy != 2 {
		t.Fatalf("post-nil: spoof=%d proxy=%d, want 1/2", snap.HitsSpoof, snap.HitsProxy)
	}
}

// TestSpoof_NonAddressTypesFallThrough — HTTPS/SVCB/MX etc. on a
// proxy-classified name must still forward, so browsers keep getting
// authoritative HTTPS records (ECH, alpn) instead of a synthetic
// NOERROR-empty reply.
func TestSpoof_NonAddressTypesFallThrough(t *testing.T) {
	store := &Store{}
	tbl, _ := BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomainSuffix, Pattern: "openai.com", Action: "Proxy", Priority: 1, Enabled: true},
	})
	store.Publish(tbl)

	forwarded := false
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		forwarded = true
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}, nil)
	r := NewResolver(store, up, NewMetrics())
	r.SetSpoofPolicy(&SpoofPolicy{
		ServerIP4: net.ParseIP("177.0.143.27"),
		ServerIP6: net.ParseIP("2001:db8::1"),
		Scope:     SpoofScopeAll,
	})

	if _, err := r.Resolve(context.Background(), spoofTestQuery("chat.openai.com", dns.TypeHTTPS), nil); err != nil {
		t.Fatal(err)
	}
	if !forwarded {
		t.Fatal("HTTPS-type query on proxy path must forward, not spoof")
	}
}
