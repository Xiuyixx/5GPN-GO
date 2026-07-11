// Package frontdoor: this file builds the four protocol-specific
// *tls.Config instances that front the DNS plane's encrypted transports
// (plan §4 Phase 3 Round 2 rewrite, AC-F6).
package frontdoor

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
)

// CertificateProvider returns the certificate to present to TLS clients.
// It is a closure over certmagic's cert cache (e.g. wrapping
// magic.GetCertificate) so that a Let's Encrypt renewal is visible to
// every listener on the very next handshake, without any listener
// restart. All four tls.Config values built by BuildTLSConfigs share the
// exact same CertificateProvider.
type CertificateProvider func() (*tls.Certificate, error)

// TLSConfigs holds the four distinct *tls.Config values used by the DNS
// plane's TLS/QUIC listeners. Each has its own NextProtos and
// ClientSessionCache / SessionTicketKey so a session ticket minted for
// one protocol can never be replayed against another — sharing a single
// tls.Config across all four was explicitly rejected in the plan's ADR
// (§7.3) because a shared ClientSessionCache + NextProtos mismatch breaks
// strict RFC 9250 clients and enables cross-protocol ticket presentation.
type TLSConfigs struct {
	DoT  *tls.Config // NextProtos: ["dot"]
	DoH  *tls.Config // NextProtos: ["h2", "http/1.1"]
	DoQ  *tls.Config // NextProtos: ["doq"] (quic-go)
	DoH3 *tls.Config // NextProtos: ["h3"] (http3.Server)
}

// BuildTLSConfigs builds the four tls.Config values described on
// TLSConfigs. getCert is shared verbatim across all four configs so a
// certmagic renewal reaches every listener without extra plumbing.
func BuildTLSConfigs(getCert CertificateProvider) (*TLSConfigs, error) {
	if getCert == nil {
		return nil, fmt.Errorf("frontdoor: BuildTLSConfigs: getCert must not be nil")
	}

	// tls.Config.GetCertificate must accept a *tls.ClientHelloInfo, but
	// every listener in this single-binary deployment serves the same
	// domain regardless of SNI, so the adapter below simply discards it
	// and defers to the shared CertificateProvider closure.
	getCertificate := func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return getCert()
	}

	dot, err := newProtocolTLSConfig(getCertificate, []string{"dot"})
	if err != nil {
		return nil, fmt.Errorf("frontdoor: dot tls config: %w", err)
	}
	doh, err := newProtocolTLSConfig(getCertificate, []string{"h2", "http/1.1"})
	if err != nil {
		return nil, fmt.Errorf("frontdoor: doh tls config: %w", err)
	}
	doq, err := newProtocolTLSConfig(getCertificate, []string{"doq"})
	if err != nil {
		return nil, fmt.Errorf("frontdoor: doq tls config: %w", err)
	}
	doh3, err := newProtocolTLSConfig(getCertificate, []string{"h3"})
	if err != nil {
		return nil, fmt.Errorf("frontdoor: doh3 tls config: %w", err)
	}

	return &TLSConfigs{DoT: dot, DoH: doh, DoQ: doq, DoH3: doh3}, nil
}

// newProtocolTLSConfig builds one protocol's tls.Config: shared
// getCertificate, its own ALPN list, and its own session cache + ticket
// key so per-protocol state never leaks across listeners.
func newProtocolTLSConfig(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), nextProtos []string) (*tls.Config, error) {
	ticketKey, err := newSessionTicketKey()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate:     getCertificate,
		NextProtos:         nextProtos,
		MinVersion:         tls.VersionTLS12,
		ClientSessionCache: tls.NewLRUClientSessionCache(256),
		SessionTicketKey:   ticketKey,
	}, nil
}

// newSessionTicketKey generates a fresh 32-byte session ticket key via
// crypto/rand. Not persisted across restarts — fresh forward secrecy
// preferred over 0-RTT resumption across restart boundary.
func newSessionTicketKey() ([32]byte, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return key, fmt.Errorf("frontdoor: generate session ticket key: %w", err)
	}
	return key, nil
}
