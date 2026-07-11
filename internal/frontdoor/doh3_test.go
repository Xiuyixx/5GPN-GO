package frontdoor

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
)

// startDoH3 builds TLSConfigs from cert, starts a DoH3 listener on an
// ephemeral loopback port, and registers cleanup to shut it down. Mirrors
// startDoT/startDoQ.
func startDoH3(t *testing.T, getCert CertificateProvider, doh *DoH) (*DoH3, string) {
	t.Helper()

	cfgs, err := BuildTLSConfigs(getCert)
	if err != nil {
		t.Fatalf("BuildTLSConfigs: %v", err)
	}

	d3 := NewDoH3("127.0.0.1:0", cfgs.DoH3, doh, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d3.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shCancel()
		if err := d3.Shutdown(shCtx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	return d3, d3.Addr()
}

// TestDoH3_POST_ValidWire_Returns200 covers plan §4 Phase 9 doh3_test.go:
// a self-signed cert, an ephemeral HTTP/3 (QUIC) listener wrapping the
// existing DoH handler, a POST /dns-query with a dns-message body sent
// over an http3.Transport-backed *http.Client, and the 200 + valid DNS
// response asserted.
//
// Deviation from the task's literal wording: quic-go v0.60.0 names this
// type Transport, not RoundTripper — there is no http3.RoundTripper
// symbol in this version. http3.Transport is the type that implements
// http.RoundTripper, so it is used here as the client's transport.
func TestDoH3_POST_ValidWire_Returns200(t *testing.T) {
	up := newFakeUpstream(t, func(q *dns.Msg) *dns.Msg {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = []dns.RR{makeA(q.Question[0].Name, "203.0.113.53")}
		return m
	})
	doh := newTestDoH(t, up)

	cert := generateSelfSignedCert(t, "localhost")
	getCert := func() (*tls.Certificate, error) { return &cert, nil }

	_, addr := startDoH3(t, getCert, doh)

	rt := &http3.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // test-only, self-signed cert
			NextProtos:         []string{"h3"},
		},
	}
	defer rt.Close()
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	q := makeQuery("doh3.example.com", dns.TypeA)
	packed, err := q.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://"+addr+"/dns-query", bytes.NewReader(packed))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", dnsMessageContentType)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != dnsMessageContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, dnsMessageContentType)
	}

	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}

	respMsg := new(dns.Msg)
	if err := respMsg.Unpack(body.Bytes()); err != nil {
		t.Fatalf("response did not unpack as a dns.Msg: %v", err)
	}
	if respMsg.Rcode != dns.RcodeSuccess || len(respMsg.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", respMsg)
	}
	a, ok := respMsg.Answer[0].(*dns.A)
	if !ok || a.A.String() != "203.0.113.53" {
		t.Fatalf("answer = %+v, want A 203.0.113.53", respMsg.Answer[0])
	}
}
