package frontdoor

import (
	"crypto/sha256"
	"crypto/tls"
	"sync"
	"testing"

	"github.com/miekg/dns"
)

// TestCertRotationKeepsExistingDoTConnectionAlive covers plan AC-N4 /
// cert_rotation_test.go: a certmagic-style renewal (the shared getCert
// closure starts returning a new certificate) must not disturb an
// already-open DoT connection — TLS negotiates the certificate once, at
// handshake time, so an established connection keeps answering on the
// old cert with no CRYPTO_ERROR — while any NEW connection dialed after
// the swap observes the fresh certificate.
func TestCertRotationKeepsExistingDoTConnectionAlive(t *testing.T) {
	const qname = "blocked.example.com"

	certA := generateSelfSignedCert(t, "localhost")
	certB := generateSelfSignedCert(t, "localhost")

	var mu sync.Mutex
	active := &certA
	getCert := func() (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		return active, nil
	}

	res := newBlockingResolver(t, qname)
	_, addr := startDoT(t, getCert, res)

	dial := func(t *testing.T) *tls.Conn {
		t.Helper()
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // test-only, self-signed cert
			NextProtos:         []string{"dot"},
		})
		if err != nil {
			t.Fatalf("tls.Dial: %v", err)
		}
		return conn
	}

	assertBlocked := func(t *testing.T, co *dns.Conn) {
		t.Helper()
		if err := co.WriteMsg(makeQuery(qname, dns.TypeA)); err != nil {
			t.Fatalf("WriteMsg: %v", err)
		}
		resp, err := co.ReadMsg()
		if err != nil {
			t.Fatalf("ReadMsg (possible CRYPTO_ERROR on existing connection): %v", err)
		}
		if resp.Rcode != dns.RcodeNameError {
			t.Fatalf("Rcode = %s, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
		}
	}

	fingerprint := func(state tls.ConnectionState) [32]byte {
		return sha256.Sum256(state.PeerCertificates[0].Raw)
	}

	// 1. Open a long-lived connection BEFORE rotation and confirm it works.
	longLived := dial(t)
	defer longLived.Close()
	longLivedCo := &dns.Conn{Conn: longLived}
	assertBlocked(t, longLivedCo)
	preRotationFingerprint := fingerprint(longLived.ConnectionState())

	// 2. Rotate the certificate — simulates certmagic.CacheManaged.ForceRenew
	// swapping in a freshly issued cert behind the shared closure.
	mu.Lock()
	active = &certB
	mu.Unlock()

	// 3. The SAME already-established connection must keep working: TLS
	// negotiated its certificate once, at the original handshake, so
	// there is no re-handshake and no CRYPTO_ERROR — and its peer
	// certificate must be unchanged.
	assertBlocked(t, longLivedCo)
	if fingerprint(longLived.ConnectionState()) != preRotationFingerprint {
		t.Fatal("existing connection's peer certificate changed after rotation; should be stable")
	}

	// 4. A NEW connection dialed after rotation must see the fresh cert.
	fresh := dial(t)
	defer fresh.Close()
	freshCo := &dns.Conn{Conn: fresh}
	assertBlocked(t, freshCo)

	if fingerprint(fresh.ConnectionState()) == preRotationFingerprint {
		t.Fatal("new connection presented the OLD certificate; rotation did not take effect")
	}
}
