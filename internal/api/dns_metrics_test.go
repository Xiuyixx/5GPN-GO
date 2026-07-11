package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// ------------------------------------------------------------------
// 1. Zero counters + not_configured listeners when Metrics/Resolver
//    are both nil (the default for most existing test servers).
// ------------------------------------------------------------------

func TestDNSMetrics_ZeroWhenNilDeps(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})

	rr := authGet(t, srv, "/api/v1/metrics/dns", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[DNSMetricsResponse](t, rr)

	if resp.QueriesTotal != 0 || resp.HitsBlock != 0 || resp.HitsDirect != 0 ||
		resp.HitsProxy != 0 || resp.UpstreamErrors != 0 || resp.RefusedAXFR != 0 {
		t.Fatalf("want all-zero counters, got %+v", resp)
	}
	want := notConfiguredListeners()
	if resp.Listeners != want {
		t.Fatalf("want listeners %+v, got %+v", want, resp.Listeners)
	}
	if resp.Cert != nil {
		t.Fatalf("want nil cert block when ACME unconfigured, got %+v", resp.Cert)
	}
}

// ------------------------------------------------------------------
// 2. Counters reflect a wired-in *resolver.Metrics.
// ------------------------------------------------------------------

func TestDNSMetrics_CountersReflectMetrics(t *testing.T) {
	m := resolver.NewMetrics()
	srv, token := bootstrapAndLogin(t, Config{Metrics: m})

	m.IncQueries()
	m.IncQueries()
	m.IncBlock()
	m.IncDirect()
	m.IncDirect()
	m.IncDirect()
	m.IncProxy()
	m.IncUpstreamError()
	m.IncRefusedAXFR()

	rr := authGet(t, srv, "/api/v1/metrics/dns", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[DNSMetricsResponse](t, rr)

	if resp.QueriesTotal != 2 {
		t.Errorf("queries_total: want 2, got %d", resp.QueriesTotal)
	}
	if resp.HitsBlock != 1 {
		t.Errorf("hits_block: want 1, got %d", resp.HitsBlock)
	}
	if resp.HitsDirect != 3 {
		t.Errorf("hits_direct: want 3, got %d", resp.HitsDirect)
	}
	if resp.HitsProxy != 1 {
		t.Errorf("hits_proxy: want 1, got %d", resp.HitsProxy)
	}
	if resp.UpstreamErrors != 1 {
		t.Errorf("upstream_errors: want 1, got %d", resp.UpstreamErrors)
	}
	if resp.RefusedAXFR != 1 {
		t.Errorf("refused_axfr: want 1, got %d", resp.RefusedAXFR)
	}
}

// ------------------------------------------------------------------
// 3. Cert block absent when ACME.Domain is empty (default zero value).
// ------------------------------------------------------------------

func TestDNSMetrics_CertAbsentWhenDomainEmpty(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	srv.ACME = ACMEOptions{StorageDir: t.TempDir()} // Domain left empty

	rr := authGet(t, srv, "/api/v1/metrics/dns", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[DNSMetricsResponse](t, rr)
	if resp.Cert != nil {
		t.Fatalf("want nil cert block when ACME.Domain is empty, got %+v", resp.Cert)
	}
}

// ------------------------------------------------------------------
// 4. Cert block populated when a real cert file sits in the expected
//    certmagic FileStorage layout.
// ------------------------------------------------------------------

// writeFakeCert generates a self-signed leaf certificate expiring
// notAfter and writes it as PEM at the certmagic FileStorage path
// dns_metrics.go reads: <storageDir>/certificates/acme-v02.api.letsencrypt.org-directory/<domain>/<domain>.crt
func writeFakeCert(t *testing.T, storageDir, domain string, notAfter time.Time) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	dir := filepath.Join(storageDir, "certificates", "acme-v02.api.letsencrypt.org-directory", domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain+".crt"), certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
}

func TestDNSMetrics_CertPopulatedFromFile(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	domain := "dns-metrics-test.example.com"
	storageDir := t.TempDir()
	notAfter := time.Now().Add(20 * 24 * time.Hour)
	writeFakeCert(t, storageDir, domain, notAfter)
	srv.ACME = ACMEOptions{Domain: domain, StorageDir: storageDir}

	rr := authGet(t, srv, "/api/v1/metrics/dns", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[DNSMetricsResponse](t, rr)
	if resp.Cert == nil {
		t.Fatal("want populated cert block, got nil")
	}
	if resp.Cert.Domain != domain {
		t.Errorf("cert.domain: want %q, got %q", domain, resp.Cert.Domain)
	}
	if resp.Cert.NotAfterUnix != notAfter.Unix() {
		t.Errorf("cert.not_after_unix: want %d, got %d", notAfter.Unix(), resp.Cert.NotAfterUnix)
	}
	if resp.Cert.DaysUntilExpiry < 19 || resp.Cert.DaysUntilExpiry > 20 {
		t.Errorf("cert.days_until_expiry: want ~20, got %d", resp.Cert.DaysUntilExpiry)
	}
}

// ------------------------------------------------------------------
// 5. Malformed cert file -> cert block absent, no 500.
// ------------------------------------------------------------------

func TestDNSMetrics_MalformedCertFileNoCrash(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	domain := "dns-metrics-malformed.example.com"
	storageDir := t.TempDir()

	dir := filepath.Join(storageDir, "certificates", "acme-v02.api.letsencrypt.org-directory", domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain+".crt"), []byte("not a real certificate"), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	srv.ACME = ACMEOptions{Domain: domain, StorageDir: storageDir}

	rr := authGet(t, srv, "/api/v1/metrics/dns", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 (no 500 on malformed cert), got %d: %s", rr.Code, rr.Body.String())
	}
	resp := decode[DNSMetricsResponse](t, rr)
	if resp.Cert != nil {
		t.Fatalf("want nil cert block for malformed cert file, got %+v", resp.Cert)
	}
}
