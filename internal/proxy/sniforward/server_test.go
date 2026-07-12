package sniforward

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// -----------------------------------------------------------------------
// peek.go — SNI extraction golden path + edge cases
// -----------------------------------------------------------------------

// buildClientHello builds a real TLS ClientHello by starting a
// crypto/tls.Client against a capture-only conn just long enough to
// grab the first flight bytes. Avoids hand-rolling wire bytes.
func buildClientHello(t *testing.T, sni string) []byte {
	t.Helper()
	c := &captureConn{}
	tlsC := tls.Client(c, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	done := make(chan struct{})
	go func() { _ = tlsC.Handshake(); close(done) }()

	deadline := time.Now().Add(500 * time.Millisecond)
	for c.Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("client never wrote ClientHello")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.SetEOF()
	<-done
	return c.Bytes()
}

type captureConn struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	eof  bool
	cond *sync.Cond
}

func (c *captureConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}
func (c *captureConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.cond == nil {
		c.cond = sync.NewCond(&c.mu)
	}
	for !c.eof {
		c.cond.Wait()
	}
	c.mu.Unlock()
	return 0, io.EOF
}
func (c *captureConn) SetEOF() {
	c.mu.Lock()
	c.eof = true
	if c.cond != nil {
		c.cond.Broadcast()
	}
	c.mu.Unlock()
}
func (c *captureConn) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Len()
}
func (c *captureConn) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, c.buf.Len())
	copy(out, c.buf.Bytes())
	return out
}
func (c *captureConn) Close() error                       { return nil }
func (c *captureConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (c *captureConn) RemoteAddr() net.Addr               { return dummyAddr{} }
func (c *captureConn) SetDeadline(_ time.Time) error      { return nil }
func (c *captureConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *captureConn) SetWriteDeadline(_ time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "test" }
func (dummyAddr) String() string  { return "test:0" }

func TestPeekSNI_ExtractsRealHelloSNI(t *testing.T) {
	hello := buildClientHello(t, "chat.openai.com")
	sni, raw, err := peekSNI(bytes.NewReader(hello))
	if err != nil {
		t.Fatalf("peekSNI: %v", err)
	}
	if sni != "chat.openai.com" {
		t.Fatalf("sni = %q, want chat.openai.com", sni)
	}
	if !bytes.Equal(raw, hello) {
		t.Fatal("raw record must be byte-identical to the peeked bytes")
	}
}

func TestPeekSNI_NotTLS(t *testing.T) {
	_, _, err := peekSNI(bytes.NewReader([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")))
	if err != ErrNotTLS {
		t.Fatalf("err = %v, want ErrNotTLS", err)
	}
}

func TestPeekSNI_TruncatedRecord(t *testing.T) {
	buf := []byte{0x16, 0x03, 0x01, 0x01, 0xF4, 0x01, 0x02, 0x03, 0x04, 0x05}
	_, _, err := peekSNI(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error on truncated body")
	}
}

// -----------------------------------------------------------------------
// server.go — selectUpstream policy
// -----------------------------------------------------------------------

func TestSelectUpstream_PanelDomainMatchesLocal(t *testing.T) {
	s := New(Config{PanelDomain: "sbxiuyi.ddns-route.net", PanelBackend: "127.0.0.1:8444"}, nil)
	up, isLocal := s.selectUpstream("sbxiuyi.ddns-route.net")
	if !isLocal || up != "127.0.0.1:8444" {
		t.Fatalf("panel SNI: got (%s,%v), want (127.0.0.1:8444,true)", up, isLocal)
	}
}

func TestSelectUpstream_SubdomainOfPanelStillLocal(t *testing.T) {
	s := New(Config{PanelDomain: "sbxiuyi.ddns-route.net", PanelBackend: "127.0.0.1:8444"}, nil)
	up, isLocal := s.selectUpstream("api.sbxiuyi.ddns-route.net")
	if !isLocal || up != "127.0.0.1:8444" {
		t.Fatalf("subdomain: got (%s,%v)", up, isLocal)
	}
}

func TestSelectUpstream_ForeignSNIForwarded(t *testing.T) {
	s := New(Config{PanelDomain: "sbxiuyi.ddns-route.net", PanelBackend: "127.0.0.1:8444"}, nil)
	up, isLocal := s.selectUpstream("chat.openai.com")
	if isLocal || up != "chat.openai.com:443" {
		t.Fatalf("foreign SNI: got (%s,%v)", up, isLocal)
	}
}

func TestSelectUpstream_RejectsIPSNI(t *testing.T) {
	s := New(Config{PanelDomain: "sbxiuyi.ddns-route.net", PanelBackend: "127.0.0.1:8444"}, nil)
	for _, ip := range []string{"1.2.3.4", "2001:db8::1"} {
		up, _ := s.selectUpstream(ip)
		if up != "" {
			t.Fatalf("bare-IP SNI %q: got %s, want reject", ip, up)
		}
	}
}

func TestSelectUpstream_RejectsEmptySNI(t *testing.T) {
	s := New(Config{PanelDomain: "sbxiuyi.ddns-route.net", PanelBackend: "127.0.0.1:8444"}, nil)
	up, _ := s.selectUpstream("")
	if up != "" {
		t.Fatalf("empty SNI: got %s, want reject", up)
	}
}

func TestSelectUpstream_CaseInsensitive(t *testing.T) {
	s := New(Config{PanelDomain: "sbxiuyi.ddns-route.net", PanelBackend: "127.0.0.1:8444"}, nil)
	up, isLocal := s.selectUpstream("SBXIUYI.ddns-Route.NET")
	if !isLocal || up != "127.0.0.1:8444" {
		t.Fatalf("case-insensitive: got (%s,%v)", up, isLocal)
	}
}

func TestDialUpstream_ResolvedPrivateRejected(t *testing.T) {
	s := New(Config{}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	conn, err := s.dialUpstream(ctx, "localhost:443", false)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("localhost received an external forwarding connection")
	}
	if err == nil {
		t.Fatal("localhost was not rejected")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %q, want non-public destination rejection", err)
	}
}

// -----------------------------------------------------------------------
// End-to-end: real TLS client → sniforward → real TLS backend
// -----------------------------------------------------------------------

// TestForward_EndToEnd_PanelDomain wires a real TLS listener (the
// stand-in for the panel HTTPS backend), starts sniforward in front
// of it, then dials sniforward with a real TLS client. Success =
// TLS handshake finishes end-to-end and the backend's banner reaches
// the client, proving:
//  1. SNI peek picked the right upstream.
//  2. The peeked record was replayed byte-identical (otherwise the
//     TLS record MAC would fail).
//  3. The two-way pipe pumps bytes both directions.
func TestForward_EndToEnd_PanelDomain(t *testing.T) {
	panelDomain := "panel.local.test"
	certPEM, keyPEM := selfSignedCert(t, panelDomain)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	backendLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{pair},
	})
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	defer func() { _ = backendLn.Close() }()
	go func() {
		for {
			c, err := backendLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = c.Write([]byte("PANEL_OK"))
			}(c)
		}
	}()

	srv := New(Config{
		Listen:       "127.0.0.1:0",
		PanelDomain:  panelDomain,
		PanelBackend: backendLn.Addr().String(),
	}, nil)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	tlsClient, err := tls.Dial("tcp", srv.Addr(), &tls.Config{
		ServerName:         panelDomain,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer func() { _ = tlsClient.Close() }()

	got := tlsClient.ConnectionState().PeerCertificates[0].Subject.CommonName
	if got != panelDomain {
		t.Fatalf("peer CN = %q, want %q — sniforward routed to the wrong upstream", got, panelDomain)
	}

	buf := make([]byte, 32)
	_ = tlsClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := tlsClient.Read(buf)
	if err != nil {
		t.Fatalf("read banner: %v", err)
	}
	if !strings.HasPrefix(string(buf[:n]), "PANEL_OK") {
		t.Fatalf("banner = %q", string(buf[:n]))
	}
}

// selfSignedCert produces a fresh ECDSA P-256 self-signed cert valid
// for cn. Kept inline (no cross-file helper) so this test file is
// entirely self-contained.
func selfSignedCert(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}
