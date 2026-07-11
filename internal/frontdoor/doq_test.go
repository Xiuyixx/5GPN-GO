package frontdoor

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// startDoQ builds TLSConfigs from cert, starts a DoQ listener on an
// ephemeral loopback port, and registers cleanup to shut it down. Mirrors
// startDoT in dot_test.go.
func startDoQ(t *testing.T, getCert CertificateProvider, res *resolver.Resolver) (*DoQ, string) {
	t.Helper()

	cfgs, err := BuildTLSConfigs(getCert)
	if err != nil {
		t.Fatalf("BuildTLSConfigs: %v", err)
	}

	q := NewDoQ("127.0.0.1:0", cfgs.DoQ, res, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shCancel()
		if err := q.Shutdown(shCtx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	return q, q.Addr()
}

// TestDoQServesQueryOverQUIC covers plan §4 Phase 9 doq_test.go: a
// self-signed cert, an ephemeral quic.EarlyListener, a length-prefixed
// (RFC 9250 §4.2) A query for dns.google sent over a client-opened
// bidirectional stream, and the length-prefixed response read back and
// asserted for correctness. The upstream is faked (newFakeUpstream, the
// same in-memory net.Pipe helper doh_test.go/frontdoor_test.go use) so
// the test stays hermetic — it never depends on a real network path to
// dns.google, only on the DoQ transport + framing + resolver dispatch.
func TestDoQServesQueryOverQUIC(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "8.8.8.8")}
		return m
	})
	res := newLiveResolver(t, up)

	cert := generateSelfSignedCert(t, "localhost")
	getCert := func() (*tls.Certificate, error) { return &cert, nil }

	_, addr := startDoQ(t, getCert, res)

	clientCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test-only, self-signed cert
		NextProtos:         []string{"doq"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, clientCfg, nil)
	if err != nil {
		t.Fatalf("quic.DialAddr: %v", err)
	}
	defer conn.CloseWithError(0, "")

	if got := conn.ConnectionState().TLS.NegotiatedProtocol; got != "doq" {
		t.Fatalf("ALPN negotiated protocol = %q, want %q", got, "doq")
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("OpenStreamSync: %v", err)
	}

	q := makeQuery("dns.google", dns.TypeA)
	if err := writeDoQMessage(stream, q); err != nil {
		t.Fatalf("writeDoQMessage: %v", err)
	}
	// RFC 9250 §5.1: the client signals "no further data on this stream"
	// by closing its write side once the query is sent.
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close: %v", err)
	}

	resp, err := readDoQMessage(stream)
	if err != nil {
		t.Fatalf("readDoQMessage: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || a.A.String() != "8.8.8.8" {
		t.Fatalf("answer = %+v, want A 8.8.8.8", resp.Answer[0])
	}
}
