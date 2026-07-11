package frontdoor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// generateSelfSignedCert returns a fresh self-signed ECDSA certificate for
// commonName, valid for localhost/127.0.0.1/::1 — enough for a
// tls.Dial(..., InsecureSkipVerify) test client. Shared by this file and
// cert_rotation_test.go.
func generateSelfSignedCert(t *testing.T, commonName string) tls.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{commonName},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}
}

// newBlockingResolver returns a *resolver.Resolver whose single rule
// blocks qname outright. A blocked query answers NXDOMAIN without ever
// touching Upstream, so it proves a query round-trips through the full
// DoT stack (TLS handshake + framing + resolver dispatch) without
// depending on a real (or faked) network upstream.
func newBlockingResolver(t *testing.T, qname string) *resolver.Resolver {
	t.Helper()
	store := &resolver.Store{}
	tbl, err := resolver.BuildTable([]rules.Rule{
		{ID: "1", Kind: rules.KindDomain, Pattern: qname, Action: "block", Priority: 1, Enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	store.Publish(tbl)
	return resolver.NewResolver(store, resolver.NewUpstream(), resolver.NewMetrics())
}

// testLogger returns a slog.Logger that discards output, keeping test -v
// output focused on assertions.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startDoT builds TLSConfigs from cert, starts a DoT listener on an
// ephemeral loopback port, and registers cleanup to shut it down. Returns
// the listener's actual bound address.
func startDoT(t *testing.T, getCert CertificateProvider, res *resolver.Resolver) (*DoT, string) {
	t.Helper()

	cfgs, err := BuildTLSConfigs(getCert)
	if err != nil {
		t.Fatalf("BuildTLSConfigs: %v", err)
	}

	d := NewDoT("127.0.0.1:0", cfgs.DoT, res, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shCancel()
		if err := d.Shutdown(shCtx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	return d, d.Addr()
}

// TestDoTServesQueryOverTLS covers plan §4 Phase 3 dot_test.go: a
// self-signed cert, an ephemeral tcp-tls listener, a framed DNS query
// over a tls.Dial'd connection, and ALPN negotiation asserted as "dot".
func TestDoTServesQueryOverTLS(t *testing.T) {
	const qname = "blocked.example.com"

	cert := generateSelfSignedCert(t, "localhost")
	getCert := func() (*tls.Certificate, error) { return &cert, nil }
	res := newBlockingResolver(t, qname)

	_, addr := startDoT(t, getCert, res)

	clientCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test-only, self-signed cert
		NextProtos:         []string{"dot"},
	}
	tlsConn, err := tls.Dial("tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer tlsConn.Close()

	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != "dot" {
		t.Fatalf("ALPN negotiated protocol = %q, want %q", got, "dot")
	}

	co := &dns.Conn{Conn: tlsConn}
	if err := co.WriteMsg(makeQuery(qname, dns.TypeA)); err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}
	resp, err := co.ReadMsg()
	if err != nil {
		t.Fatalf("ReadMsg: %v", err)
	}
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("Rcode = %s, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
	}
}
