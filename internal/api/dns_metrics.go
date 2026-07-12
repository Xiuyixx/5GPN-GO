package api

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DNSMetricsResponse is the wire shape of GET /api/v1/metrics/dns — the
// Dashboard's DNS Plane card (plan §4 Phase 7) polls this every 5s.
type DNSMetricsResponse struct {
	QueriesTotal     int64             `json:"queries_total"`
	HitsBlock        int64             `json:"hits_block"`
	HitsDirect       int64             `json:"hits_direct"`
	HitsProxy        int64             `json:"hits_proxy"`
	HitsSpoof        int64             `json:"hits_spoof"`
	UpstreamErrors   int64             `json:"upstream_errors"`
	UpstreamFallback int64             `json:"upstream_fallback"`
	RefusedAXFR      int64             `json:"refused_axfr"`
	Listeners        DNSListenerStatus `json:"listeners"`
	Cert             *DNSCertStatus    `json:"cert"`
}

// DNSListenerStatus reports each front-door transport's current state as one
// of "healthy" | "degraded" | "not_configured".
type DNSListenerStatus struct {
	UDP53 string `json:"udp53"`
	TCP53 string `json:"tcp53"`
	DoT   string `json:"dot"`
	DoH   string `json:"doh"`
	DoQ   string `json:"doq"`
	DoH3  string `json:"doh3"`
}

// DNSCertStatus is the ACME leaf certificate's expiry, read straight off
// the certmagic-managed file on disk (rather than cached in memory) so
// the panel always reflects the certificate actually in use.
type DNSCertStatus struct {
	Domain          string `json:"domain"`
	NotAfterUnix    int64  `json:"not_after_unix"`
	DaysUntilExpiry int    `json:"days_until_expiry"`
}

// notConfiguredListeners is the nil-safe default for servers built without
// the daemon's live Frontdoor status callback (primarily unit tests).
func notConfiguredListeners() DNSListenerStatus {
	return DNSListenerStatus{
		UDP53: "not_configured",
		TCP53: "not_configured",
		DoT:   "not_configured",
		DoH:   "not_configured",
		DoQ:   "not_configured",
		DoH3:  "not_configured",
	}
}

// handleDNSMetrics is GET /api/v1/metrics/dns. Nil-safe: a Server built
// without the DNS plane wired in (s.Metrics == nil, e.g. most existing
// tests) returns zero counters rather than panicking.
func (s *Server) handleDNSMetrics(w http.ResponseWriter, r *http.Request) {
	resp := DNSMetricsResponse{
		Listeners: notConfiguredListeners(),
		Cert:      s.dnsCertStatus(),
	}
	if s.DNSListeners != nil {
		resp.Listeners = s.DNSListeners()
	}

	if s.Metrics != nil {
		snap := s.Metrics.Snapshot()
		resp.QueriesTotal = snap.QueriesTotal
		resp.HitsBlock = snap.HitsBlock
		resp.HitsDirect = snap.HitsDirect
		resp.HitsProxy = snap.HitsProxy
		resp.HitsSpoof = snap.HitsSpoof
		resp.UpstreamErrors = snap.UpstreamErrors
		resp.UpstreamFallback = snap.UpstreamFallback
		resp.RefusedAXFR = snap.RefusedAXFR
	}

	writeJSON(w, http.StatusOK, resp)
}

// dnsCertStatus reads the certmagic-managed leaf certificate for
// s.ACME.Domain off disk and extracts its expiry. Returns nil whenever
// ACME isn't configured, the file can't be read, or it can't be parsed
// as a PEM-wrapped X.509 certificate — callers must never 500 because an
// operator hasn't finished ACME issuance yet or a renewal is mid-flight.
func (s *Server) dnsCertStatus() *DNSCertStatus {
	if s.ACME.Domain == "" || s.ACME.StorageDir == "" {
		return nil
	}
	// certmagic's FileStorage layout for a Let's Encrypt production
	// account: <storage>/certificates/<issuer-key>/<domain>/<domain>.crt.
	path := filepath.Join(s.ACME.StorageDir, "certificates",
		"acme-v02.api.letsencrypt.org-directory", s.ACME.Domain, s.ACME.Domain+".crt")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return &DNSCertStatus{
		Domain:          s.ACME.Domain,
		NotAfterUnix:    cert.NotAfter.Unix(),
		DaysUntilExpiry: int(time.Until(cert.NotAfter).Hours() / 24),
	}
}
