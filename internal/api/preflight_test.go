package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// generatePreflightCert returns a fresh self-signed ECDSA certificate for
// commonName — enough for a tls.Dial(..., InsecureSkipVerify) test client.
// Duplicated (rather than imported) from internal/frontdoor/dot_test.go's
// generateSelfSignedCert since that helper lives in a different package.
func generatePreflightCert(t *testing.T, commonName string) tls.Certificate {
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
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

// startFakeDoT starts a loopback TLS listener speaking ALPN "dot" and
// hands each accepted connection to handler on its own goroutine. Returns
// the listener's bound address. The listener (and any goroutines blocked
// on it) are torn down via t.Cleanup.
func startFakeDoT(t *testing.T, handler func(co *dns.Conn)) string {
	t.Helper()
	cert := generatePreflightCert(t, "localhost")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"dot"},
	})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				handler(&dns.Conn{Conn: conn})
			}()
		}
	}()
	return ln.Addr().String()
}

// answeringDoTHandler replies to any query with an A record for
// dns.google, mimicking a healthy local DoT listener.
func answeringDoTHandler(co *dns.Conn) {
	req, err := co.ReadMsg()
	if err != nil {
		return
	}
	resp := new(dns.Msg)
	resp.SetReply(req)
	rr, err := dns.NewRR("dns.google. 300 IN A 8.8.8.8")
	if err != nil {
		return
	}
	resp.Answer = append(resp.Answer, rr)
	_ = co.WriteMsg(resp)
}

// silentDoTHandler completes the TLS handshake (the listener does that
// before handler runs) but never answers the query — simulating a DoT
// listener that accepted the connection yet hung, so the preflight has to
// time out waiting for ReadMsg.
func silentDoTHandler(co *dns.Conn) {
	_, _ = co.ReadMsg()
	select {} // block forever; connection is closed by the deferred conn.Close() when the test's deadline trips runIOSPreflight's own ctx and it gives up
}

// closedPortAddr reserves an ephemeral loopback port, then immediately
// releases it, returning an address nothing is listening on — this makes
// the dial fail with "connection refused" instead of racing an
// unallocated port that might be in use elsewhere.
func closedPortAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// ------------------------------------------------------------------
// runIOSPreflight — unit-level coverage (plan §4 Phase 8 AC-I2/I3)
// ------------------------------------------------------------------

func TestRunIOSPreflight_Success(t *testing.T) {
	addr := startFakeDoT(t, answeringDoTHandler)
	res := runIOSPreflight(t.Context(), addr)
	if !res.OK {
		t.Fatalf("want OK=true, got %+v", res)
	}
	if !res.DotHandshake {
		t.Fatalf("want DotHandshake=true, got %+v", res)
	}
	if !res.SampleQuery {
		t.Fatalf("want SampleQuery=true, got %+v", res)
	}
	if res.Error != "" {
		t.Fatalf("want empty Error, got %q", res.Error)
	}
}

func TestRunIOSPreflight_HandshakeFailure(t *testing.T) {
	addr := closedPortAddr(t)
	res := runIOSPreflight(t.Context(), addr)
	if res.OK {
		t.Fatalf("want OK=false, got %+v", res)
	}
	if res.DotHandshake {
		t.Fatalf("want DotHandshake=false, got %+v", res)
	}
	if res.Error == "" {
		t.Fatal("want a non-empty Error explaining the handshake failure")
	}
}

func TestRunIOSPreflight_Timeout(t *testing.T) {
	// Hook: shrink the 30s ceiling so the test doesn't actually wait 30s.
	orig := iosPreflightTimeout
	iosPreflightTimeout = 300 * time.Millisecond
	t.Cleanup(func() { iosPreflightTimeout = orig })

	addr := startFakeDoT(t, silentDoTHandler)
	start := time.Now()
	res := runIOSPreflight(t.Context(), addr)
	elapsed := time.Since(start)

	if res.OK {
		t.Fatalf("want OK=false, got %+v", res)
	}
	if !res.DotHandshake {
		t.Fatalf("want DotHandshake=true (TLS connects fine, only the query hangs), got %+v", res)
	}
	if res.SampleQuery {
		t.Fatalf("want SampleQuery=false, got %+v", res)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("preflight took %s, want it bounded by the shortened iosPreflightTimeout hook", elapsed)
	}
}

// ------------------------------------------------------------------
// HTTP handlers — POST /api/v1/settings/ios/profile-enabled,
// GET /ios-dot.mobileconfig (plan §4 Phase 8 AC-I1/I4)
// ------------------------------------------------------------------

// withFakeIOSPreflightAddr points defaultIOSPreflightDoTAddr at addr for
// the duration of the test, restoring the original value on cleanup. The
// api package tests never run in parallel (grep confirms no t.Parallel()
// callers), so mutating this package var is safe here.
func withFakeIOSPreflightAddr(t *testing.T, addr string) {
	t.Helper()
	orig := defaultIOSPreflightDoTAddr
	defaultIOSPreflightDoTAddr = addr
	t.Cleanup(func() { defaultIOSPreflightDoTAddr = orig })
}

func TestHandleIOSProfileToggle_EnableFailsPreflight(t *testing.T) {
	withFakeIOSPreflightAddr(t, closedPortAddr(t))
	srv, token := bootstrapAndLogin(t, Config{})

	rr := authPost(t, srv, "/api/v1/settings/ios/profile-enabled", token, map[string]bool{"enabled": true})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
	}
	got, err := srv.Settings.GetBool(t.Context(), settings.KeyFrontdoorIOSProfileEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("toggle must stay off after a failed preflight")
	}
}

func TestHandleIOSProfileToggle_EnableSucceedsPreflight(t *testing.T) {
	withFakeIOSPreflightAddr(t, startFakeDoT(t, answeringDoTHandler))
	srv, token := bootstrapAndLogin(t, Config{})

	rr := authPost(t, srv, "/api/v1/settings/ios/profile-enabled", token, map[string]bool{"enabled": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	got, err := srv.Settings.GetBool(t.Context(), settings.KeyFrontdoorIOSProfileEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("toggle must persist as on after a passing preflight")
	}
	if lastAt, err := srv.Settings.GetInt(t.Context(), settings.KeyFrontdoorPreflightLastAt); err == nil && lastAt == 0 {
		// KeyFrontdoorPreflightLastAt is stored via SetJSON as an int64, not
		// SetInt; GetInt best-effort-decodes it back for this smoke check.
		t.Fatalf("expected KeyFrontdoorPreflightLastAt to be recorded on success")
	}

	// GET /api/v1/settings/panel must surface the toggle + preflight
	// timestamp so the wizard/settings page can render state without a
	// second bespoke endpoint (plan §4 Phase 8, item 7).
	rr = authGet(t, srv, "/api/v1/settings/panel", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("panel settings: want 200, got %d %s", rr.Code, rr.Body.String())
	}
	panel := decode[map[string]any](t, rr)
	iosSection, ok := panel["ios"].(map[string]any)
	if !ok {
		t.Fatalf("panel settings missing ios section: %v", panel)
	}
	if iosSection["profile_enabled"] != true {
		t.Fatalf("panel settings ios.profile_enabled = %v, want true", iosSection["profile_enabled"])
	}
	if _, ok := iosSection["preflight_last_at"]; !ok {
		t.Fatalf("panel settings missing ios.preflight_last_at after a passing preflight: %v", iosSection)
	}
}

func TestHandleIOSProfileToggle_DisableSkipsPreflight(t *testing.T) {
	// Point at an address that would fail preflight if it were ever dialed,
	// to prove the disable path never runs one.
	withFakeIOSPreflightAddr(t, closedPortAddr(t))
	srv, token := bootstrapAndLogin(t, Config{})

	rr := authPost(t, srv, "/api/v1/settings/ios/profile-enabled", token, map[string]bool{"enabled": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	got, err := srv.Settings.GetBool(t.Context(), settings.KeyFrontdoorIOSProfileEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("toggle should be off")
	}
}

func TestHandleIOSMobileconfig_503WhenDisabled(t *testing.T) {
	base := &config.Config{}
	base.Server.Domain = "gw.example.com"
	srv, _ := bootstrapAndLogin(t, Config{BaseConfig: base})

	rr := do(t, srv, jsonReq(t, "GET", "/ios-dot.mobileconfig", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d %s", rr.Code, rr.Body.String())
	}
	body := decode[APIError](t, rr)
	if body.Error != "ios_profile_disabled" {
		t.Fatalf("want error=ios_profile_disabled, got %+v", body)
	}
}

func TestHandleIOSMobileconfig_200WhenEnabled(t *testing.T) {
	withFakeIOSPreflightAddr(t, startFakeDoT(t, answeringDoTHandler))
	base := &config.Config{}
	base.Server.Domain = "gw.example.com"
	srv, token := bootstrapAndLogin(t, Config{BaseConfig: base})

	rr := authPost(t, srv, "/api/v1/settings/ios/profile-enabled", token, map[string]bool{"enabled": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("enable: want 200, got %d %s", rr.Code, rr.Body.String())
	}

	rr = do(t, srv, jsonReq(t, "GET", "/ios-dot.mobileconfig", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	s := rr.Body.String()
	if !strings.Contains(s, "OnDemandRules") {
		t.Errorf("rendered profile missing OnDemandRules:\n%s", s)
	}
	if !strings.Contains(s, "gw.example.com") {
		t.Errorf("rendered profile missing primary domain:\n%s", s)
	}
	if !strings.Contains(s, defaultIOSFallbackDoT) {
		t.Errorf("rendered profile missing default fallback DoT:\n%s", s)
	}
}
