package frontdoor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// newFakeUpstream mirrors the resolver package's own test helper: TLSDial
// hands the resolver an in-memory net.Pipe whose server side replies with
// whatever reply() returns, so these tests never touch a real socket.
func newFakeUpstream(t *testing.T, reply func(*dns.Msg) *dns.Msg) *resolver.Upstream {
	t.Helper()
	up := resolver.NewUpstream()
	up.Timeout = 2 * time.Second
	up.TLSDial = func(ctx context.Context, addr string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
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
			resp := reply(q)
			packed, err := resp.Pack()
			if err != nil {
				return
			}
			var out [2]byte
			binary.BigEndian.PutUint16(out[:], uint16(len(packed)))
			_, _ = server.Write(out[:])
			_, _ = server.Write(packed)
		}()
		return client, nil
	}
	return up
}

// failingUpstream fails every dial immediately, driving the resolver down
// its SERVFAIL path without ever touching the network.
func failingUpstream() *resolver.Upstream {
	up := resolver.NewUpstream()
	up.Timeout = 2 * time.Second
	up.TLSDial = func(ctx context.Context, addr string) (net.Conn, error) {
		return nil, errors.New("frontdoor test: upstream unreachable")
	}
	return up
}

func makeQuery(qname string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(qname), qtype)
	m.Id = 0xBEEF
	return m
}

func makeA(name, ip string) dns.RR {
	rr := new(dns.A)
	rr.Hdr = dns.RR_Header{Name: dns.Fqdn(name), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 42}
	rr.A = net.ParseIP(ip)
	return rr
}

// newTestDoH builds a DoH handler around a resolver with an empty rule
// table (every qname falls through to the default proxy action, AC-R4)
// backed by up.
func newTestDoH(t *testing.T, up *resolver.Upstream) *DoH {
	t.Helper()
	store := &resolver.Store{}
	tbl, err := resolver.BuildTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	store.Publish(tbl)
	r := resolver.NewResolver(store, up, resolver.NewMetrics())
	return NewDoH(r, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDoH_POST_ValidWire_Returns200(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "93.184.216.34")}
		return m
	})
	d := newTestDoH(t, up)

	q := makeQuery("example.com", dns.TypeA)
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(packed))
	req.Header.Set("Content-Type", dnsMessageContentType)
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != dnsMessageContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, dnsMessageContentType)
	}

	resp := new(dns.Msg)
	if err := resp.Unpack(rr.Body.Bytes()); err != nil {
		t.Fatalf("response did not unpack as a dns.Msg: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestDoH_GET_ValidBase64_Returns200(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	})
	d := newTestDoH(t, up)

	q := makeQuery("example.org", dns.TypeAAAA)
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString(packed)

	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+b64, nil)
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(rr.Body.Bytes()); err != nil {
		t.Fatalf("response did not unpack: %v", err)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want NOERROR", resp.Rcode)
	}
}

func TestDoH_POST_WrongContentType_Returns415(t *testing.T) {
	d := newTestDoH(t, newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}))

	req := httptest.NewRequest(http.MethodPost, "/dns-query", strings.NewReader("irrelevant"))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rr.Code)
	}
}

func TestDoH_POST_GarbageBytes_Returns400(t *testing.T) {
	d := newTestDoH(t, newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		return m
	}))

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader([]byte{0xFF, 0x00, 0x13, 0x37, 0xDE, 0xAD}))
	req.Header.Set("Content-Type", dnsMessageContentType)
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req) // must not panic

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestDoH_AXFRQuery_Refused(t *testing.T) {
	// failingUpstream makes the test fail loudly (SERVFAIL instead of
	// REFUSED) if the DoH-layer AXFR short-circuit ever regresses and the
	// query reaches the resolver's forward path.
	d := newTestDoH(t, failingUpstream())

	q := makeQuery("example.com", dns.TypeAXFR)
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(packed))
	req.Header.Set("Content-Type", dnsMessageContentType)
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (DNS-level refusal, not an HTTP error)", rr.Code)
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(rr.Body.Bytes()); err != nil {
		t.Fatalf("response did not unpack: %v", err)
	}
	if resp.Rcode != dns.RcodeRefused {
		t.Fatalf("rcode = %d, want REFUSED", resp.Rcode)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

func TestDoH_CacheControl_PresentOnSuccess(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "203.0.113.7")}
		return m
	})
	d := newTestDoH(t, up)

	q := makeQuery("cache.example.com", dns.TypeA)
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(packed))
	req.Header.Set("Content-Type", dnsMessageContentType)
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "max-age=42" {
		t.Fatalf("Cache-Control = %q, want max-age=42 (matches makeA's TTL)", cc)
	}
}

func TestDoH_CacheControl_NoStoreOnServfail(t *testing.T) {
	d := newTestDoH(t, failingUpstream())

	q := makeQuery("unreachable.example.com", dns.TypeA)
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(packed))
	req.Header.Set("Content-Type", dnsMessageContentType)
	rr := httptest.NewRecorder()

	d.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (DNS-level SERVFAIL, not an HTTP error)", rr.Code)
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(rr.Body.Bytes()); err != nil {
		t.Fatalf("response did not unpack: %v", err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode = %d, want SERVFAIL", resp.Rcode)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}
