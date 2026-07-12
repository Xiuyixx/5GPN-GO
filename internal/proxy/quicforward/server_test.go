package quicforward

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// -----------------------------------------------------------------------
// parse.go — QUIC Initial SNI extraction against a real quic-go client
// -----------------------------------------------------------------------

// TestExtractSNI_FromFixtureInitial builds a valid QUIC v1 Initial
// packet with a known SNI using the same crypto helpers parse.go
// uses (in-package access), then feeds it back to extractSNI. This
// is a full round-trip: build/encrypt/protect on one side, decrypt/
// unprotect/parse on the other.
func TestExtractSNI_FromFixtureInitial(t *testing.T) {
	pkt := buildFixtureInitial(t, "example.test")
	sni, ok := extractSNI(pkt)
	if !ok {
		t.Fatalf("extractSNI: ok=false on fixture initial (%d bytes)", len(pkt))
	}
	if sni != "example.test" {
		t.Fatalf("sni = %q, want example.test", sni)
	}
}

// TestExtractSNI_ShortHeaderReturnsFalse — anything that isn't a
// long-header packet is not an Initial.
func TestExtractSNI_ShortHeaderReturnsFalse(t *testing.T) {
	// First byte high bit clear = short header.
	pkt := []byte{0x40, 0, 0, 0, 0, 0}
	if _, ok := extractSNI(pkt); ok {
		t.Fatal("short-header packet must not extract SNI")
	}
}

// TestExtractSNI_WrongVersionReturnsFalse — long header but version
// != 0x00000001 (e.g. draft or negotiation) must return false.
func TestExtractSNI_WrongVersionReturnsFalse(t *testing.T) {
	pkt := []byte{0xc0, 0xff, 0x00, 0x00, 0x1d, 0} // draft-29
	if _, ok := extractSNI(pkt); ok {
		t.Fatal("wrong-version packet must not extract SNI")
	}
}

// -----------------------------------------------------------------------
// server.go — resolveUpstream policy
// -----------------------------------------------------------------------

func TestResolveUpstream_PanelDomain(t *testing.T) {
	s := New(Config{
		PanelDomain:  "sbxiuyi.ddns-route.net",
		PanelBackend: "127.0.0.1:8445",
	}, nil)
	up, isLocal, err := s.resolveUpstream("sbxiuyi.ddns-route.net")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !isLocal {
		t.Fatal("panel SNI must resolve to local")
	}
	if up.Port != 8445 {
		t.Fatalf("port = %d, want 8445", up.Port)
	}
}

func TestResolveUpstream_PanelSubdomainStillLocal(t *testing.T) {
	s := New(Config{
		PanelDomain:  "sbxiuyi.ddns-route.net",
		PanelBackend: "127.0.0.1:8445",
	}, nil)
	up, isLocal, err := s.resolveUpstream("api.sbxiuyi.ddns-route.net")
	if err != nil || !isLocal || up.Port != 8445 {
		t.Fatalf("subdomain: up=%v isLocal=%v err=%v", up, isLocal, err)
	}
}

func TestResolveUpstream_BareIPRejected(t *testing.T) {
	s := New(Config{PanelDomain: "x.test", PanelBackend: "127.0.0.1:8445"}, nil)
	for _, sni := range []string{"1.2.3.4", "2001:db8::1"} {
		if _, _, err := s.resolveUpstream(sni); err == nil {
			t.Fatalf("bare-IP SNI %q was not rejected", sni)
		}
	}
}

func TestResolveUpstream_PortInSNIRejected(t *testing.T) {
	s := New(Config{PanelDomain: "x.test", PanelBackend: "127.0.0.1:8445"}, nil)
	if _, _, err := s.resolveUpstream("evil.com:443"); err == nil {
		t.Fatal("port-bearing SNI was not rejected")
	}
}

func TestResolveUpstream_ResolvedPrivateRejected(t *testing.T) {
	s := New(Config{}, nil)
	upstream, isLocal, err := s.resolveUpstream("localhost")
	if err == nil {
		t.Fatalf("localhost resolved to external upstream %v", upstream)
	}
	if isLocal {
		t.Fatal("localhost must not be treated as the configured panel backend")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %q, want non-public destination rejection", err)
	}
}

func TestResourceLimitsAndRelease(t *testing.T) {
	s := New(Config{
		MaxConcurrentSetups: 1,
		MaxSessions:         2,
		MaxSessionsPerIP:    1,
	}, nil)
	const (
		ipA = "198.51.100.10"
		ipB = "203.0.113.20"
	)

	if !s.tryReserveSession("a:1000", ipA) {
		t.Fatal("first setup reservation rejected")
	}
	if s.tryReserveSession("a:1000", ipA) {
		t.Fatal("duplicate 4-tuple received a second reservation")
	}
	if s.tryReserveSession("b:1000", ipB) {
		t.Fatal("concurrent setup cap was not enforced")
	}
	sessA := &session{resourceIP: ipA}
	if !s.promoteSession("a:1000", ipA, sessA) {
		t.Fatal("reserved setup was not promoted")
	}
	if s.tryReserveSession("a:1001", ipA) {
		t.Fatal("per-IP session cap was not enforced")
	}
	if !s.tryReserveSession("b:1000", ipB) {
		t.Fatal("second source could not reserve remaining global capacity")
	}
	sessB := &session{resourceIP: ipB}
	if !s.promoteSession("b:1000", ipB, sessB) {
		t.Fatal("second setup was not promoted")
	}
	if s.tryReserveSession("c:1000", "192.0.2.30") {
		t.Fatal("global session cap was not enforced")
	}

	s.dropSession("a:1000", sessA)
	if !s.tryReserveSession("c:1000", "192.0.2.30") {
		t.Fatal("released capacity was not reusable")
	}
	s.releasePending("c:1000", "192.0.2.30")
	s.dropSession("b:1000", sessB)

	s.resourceMu.Lock()
	reserved := s.reserved
	pending := len(s.pending)
	perIP := len(s.perIP)
	s.resourceMu.Unlock()
	if reserved != 0 || pending != 0 || perIP != 0 {
		t.Fatalf("resource accounting leaked: reserved=%d pending=%d per_ip=%d", reserved, pending, perIP)
	}
}

func TestDropSessionDoesNotDeleteReplacementGeneration(t *testing.T) {
	s := New(Config{}, nil)
	const (
		key = "198.51.100.10:443"
		ip  = "198.51.100.10"
	)
	old := &session{resourceIP: ip}
	replacement := &session{resourceIP: ip}
	if !s.tryReserveSession(key, ip) || !s.promoteSession(key, ip, replacement) {
		t.Fatal("failed to install replacement session")
	}

	if s.dropSession(key, old) {
		t.Fatal("stale generation deleted the replacement session")
	}
	got, ok := s.sessions.Load(key)
	if !ok || got != replacement {
		t.Fatalf("replacement session changed after stale cleanup: got=%p want=%p", got, replacement)
	}
	s.resourceMu.Lock()
	reserved, perIP := s.reserved, s.perIP[ip]
	s.resourceMu.Unlock()
	if reserved != 1 || perIP != 1 {
		t.Fatalf("stale cleanup released replacement accounting: reserved=%d per_ip=%d", reserved, perIP)
	}

	if !s.dropSession(key, replacement) {
		t.Fatal("current generation was not deleted")
	}
	s.resourceMu.Lock()
	reserved, perIP = s.reserved, s.perIP[ip]
	s.resourceMu.Unlock()
	if reserved != 0 || perIP != 0 {
		t.Fatalf("current cleanup leaked accounting: reserved=%d per_ip=%d", reserved, perIP)
	}
}

// -----------------------------------------------------------------------
// End-to-end: UDP echo through the forwarder
// -----------------------------------------------------------------------

// TestForward_UDPEchoThroughForwarder wires an "upstream" that
// echoes datagrams, teaches the server that panel SNI maps to the
// echo backend, then pushes a valid Initial datagram (SNI=panel
// domain) through the forwarder and reads the echo back.
//
// This exercises: SNI extract → panel-domain match → dial upstream
// → write first datagram → relay loop pushes echo back to client.
func TestForward_UDPEchoThroughForwarder(t *testing.T) {
	// Echo backend on 127.0.0.1:random.
	backendConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backendConn.Close() }()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := backendConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = backendConn.WriteToUDP(buf[:n], addr)
		}
	}()

	panelDomain := "panel.local.test"
	srv := New(Config{
		Listen:       "127.0.0.1:0",
		PanelDomain:  panelDomain,
		PanelBackend: backendConn.LocalAddr().String(),
	}, nil)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(t.Context()) }()

	pkt := buildFixtureInitial(t, panelDomain)

	client, err := net.DialUDP("udp", nil, srv.listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write(pkt); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if n != len(pkt) || string(buf[:n]) != string(pkt) {
		t.Fatalf("echo mismatch (n=%d want=%d)", n, len(pkt))
	}
}

func TestShutdownDuringColdPathFlood(t *testing.T) {
	backendConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backendConn.Close() }()
	go func() {
		buf := make([]byte, 65535)
		for {
			if _, _, err := backendConn.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()

	const panelDomain = "panel.local.test"
	srv := New(Config{
		Listen:              "127.0.0.1:0",
		PanelDomain:         panelDomain,
		PanelBackend:        backendConn.LocalAddr().String(),
		MaxConcurrentSetups: 4,
		MaxSessions:         8,
		MaxSessionsPerIP:    8,
	}, nil)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(t.Context()) }()

	pkt := buildFixtureInitial(t, panelDomain)
	clients := make([]*net.UDPConn, 64)
	var sends sync.WaitGroup
	for i := range clients {
		client, dialErr := net.DialUDP("udp", nil, srv.listener.LocalAddr().(*net.UDPAddr))
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		clients[i] = client
		t.Cleanup(func() { _ = client.Close() })
		sends.Add(1)
		go func() {
			defer sends.Done()
			_, _ = client.Write(pkt)
		}()
	}
	sends.Wait()

	deadline := time.Now().Add(time.Second)
	for {
		srv.resourceMu.Lock()
		reserved := srv.reserved
		srv.resourceMu.Unlock()
		if reserved > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forwarder did not admit any cold-path work")
		}
		time.Sleep(time.Millisecond)
	}

	shutdownCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	srv.resourceMu.Lock()
	reserved := srv.reserved
	pending := len(srv.pending)
	perIP := len(srv.perIP)
	srv.resourceMu.Unlock()
	if reserved != 0 || pending != 0 || perIP != 0 {
		t.Fatalf("shutdown leaked resources: reserved=%d pending=%d per_ip=%d", reserved, pending, perIP)
	}
	sessions := 0
	srv.sessions.Range(func(_, _ any) bool {
		sessions++
		return true
	})
	if sessions != 0 {
		t.Fatalf("shutdown left %d sessions", sessions)
	}
}

// -----------------------------------------------------------------------
// Fixture builder — a minimal but valid QUIC v1 Initial with the SNI
// baked into a CRYPTO frame. Uses the same crypto helpers as parse.go
// (in-package access), which makes this the inverse of the parser we
// want to exercise.
// -----------------------------------------------------------------------

func buildFixtureInitial(t *testing.T, sni string) []byte {
	t.Helper()

	// 1. Build a minimal TLS ClientHello with SNI extension.
	ch := buildClientHello(sni)

	// 2. Wrap it in a QUIC CRYPTO frame (type 0x06, offset 0, len ch).
	crypto := []byte{0x06}
	crypto = appendVarint(crypto, 0)
	crypto = appendVarint(crypto, uint64(len(ch)))
	crypto = append(crypto, ch...)

	// 3. Pad to a plausible Initial size (>= 1200 minus overhead is not
	// required for parse; we keep it compact).
	plaintext := crypto

	// 4. Build the long header + protected payload.
	// DCID: 8 bytes we choose; SCID: empty.
	dcid := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	// packet number = 0, length = 1 byte.
	pn := []byte{0x00}
	pnLen := len(pn)

	// First byte: long-header (0x80) | fixed (0x40) | initial-type (0x00) |
	// reserved (0) | pnLen-1 (0). = 0xc0.
	firstByte := byte(0xc0)

	// Assemble header up to the "length" field.
	// We'll compute payload length after AEAD-encrypting.
	initialSecret := hkdfExtract([]byte(quicInitialSaltV1), dcid)
	clientSecret := hkdfExpandLabel(initialSecret, "client in", nil, 32)
	key := hkdfExpandLabel(clientSecret, "quic key", nil, 16)
	iv := hkdfExpandLabel(clientSecret, "quic iv", nil, 12)
	hp := hkdfExpandLabel(clientSecret, "quic hp", nil, 16)

	// Ciphertext = AEAD(plaintext, nonce=iv^pn, aad=header|pn)
	// header up to length is: firstByte | version(4) | dcidLen(1) | dcid |
	// scidLen(1) | tokenLen varint(1) | length varint.
	staticHeader := []byte{firstByte}
	staticHeader = binary.BigEndian.AppendUint32(staticHeader, quicVersion1)
	staticHeader = append(staticHeader, byte(len(dcid)))
	staticHeader = append(staticHeader, dcid...)
	staticHeader = append(staticHeader, 0x00) // scid_len = 0
	staticHeader = append(staticHeader, 0x00) // token_len = 0

	// length = pnLen + len(ciphertext); ciphertext = plaintext + 16 byte tag.
	payloadLen := pnLen + len(plaintext) + aeadTagSize
	lengthVarint := encodeVarint(uint64(payloadLen))
	header := append([]byte{}, staticHeader...)
	header = append(header, lengthVarint...)

	// Nonce.
	nonce := make([]byte, 12)
	copy(nonce, iv)
	for i := 0; i < pnLen; i++ {
		nonce[11-i] ^= pn[pnLen-1-i]
	}

	// AAD = header|pn.
	aad := append([]byte{}, header...)
	aad = append(aad, pn...)

	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)

	// Assemble unprotected packet: header | pn | ciphertext.
	unprotected := append([]byte{}, header...)
	unprotected = append(unprotected, pn...)
	unprotected = append(unprotected, ciphertext...)

	// Apply header protection.
	// Sample offset from start of pn: 4 bytes past the packet-number
	// start (RFC 9001 §5.4.2). Because pnLen=1 and our ciphertext is
	// long, we can safely offset by 4.
	pnStart := len(header)
	sample := unprotected[pnStart+4 : pnStart+4+16]
	blkHP, err := aes.NewCipher(hp)
	if err != nil {
		t.Fatalf("aes hp: %v", err)
	}
	mask := make([]byte, 16)
	blkHP.Encrypt(mask, sample)

	// Apply mask.
	unprotected[0] ^= mask[0] & 0x0f // long-header uses low 4 bits mask
	for i := 0; i < pnLen; i++ {
		unprotected[pnStart+i] ^= mask[1+i]
	}
	return unprotected
}

// buildClientHello — minimal TLS 1.2/1.3 ClientHello with just the
// SNI extension. Not a valid handshake to hand to crypto/tls, but
// enough to feed sniFromClientHello.
func buildClientHello(sni string) []byte {
	// Random(32) + session_id(0) + cipher_suites(2 = 1 cipher of 2 bytes)
	// + compression(1 method 0x00) + extensions.
	//
	// Extension = server_name (0x0000): len(2), list_len(2), name_type(1=0),
	// name_len(2), name bytes.
	sniBytes := []byte(sni)
	extBody := []byte{0x00}                                                // name_type=host_name
	extBody = append(extBody, byte(len(sniBytes)>>8), byte(len(sniBytes))) // name_len
	extBody = append(extBody, sniBytes...)
	listLen := len(extBody)
	extData := []byte{byte(listLen >> 8), byte(listLen)}
	extData = append(extData, extBody...)

	ext := []byte{0x00, 0x00}                                    // ext_type = server_name
	ext = append(ext, byte(len(extData)>>8), byte(len(extData))) // ext_len
	ext = append(ext, extData...)

	// hello body:
	// client_version(2) + random(32) + session_id_len(1)=0
	// + cipher_suites_len(2) + cipher_suites(2)
	// + compression_len(1)=1 + compression(0x00)
	// + extensions_len(2) + extensions
	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)                   // sid_len = 0
	body = append(body, 0x00, 0x02, 0x00, 0x2f) // 1 cipher: TLS_RSA_WITH_AES_128_CBC_SHA
	body = append(body, 0x01, 0x00)             // compression: 1 method, null
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)

	// Handshake header: type(1)=0x01 | length(3)
	hs := []byte{0x01}
	l := len(body)
	hs = append(hs, byte(l>>16), byte(l>>8), byte(l))
	hs = append(hs, body...)
	return hs
}

func appendVarint(b []byte, v uint64) []byte {
	return append(b, encodeVarint(v)...)
}

func encodeVarint(v uint64) []byte {
	switch {
	case v < 1<<6:
		return []byte{byte(v)}
	case v < 1<<14:
		return []byte{byte(v>>8) | 0x40, byte(v)}
	case v < 1<<30:
		return []byte{byte(v>>24) | 0x80, byte(v >> 16), byte(v >> 8), byte(v)}
	default:
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		b[0] |= 0xc0
		return b
	}
}

// Trailing test that sniFromClientHello agrees with our fixture:
// gives us a fast local signal before the full E2E fixture test.
func TestSNIFromClientHello_Fixture(t *testing.T) {
	hs := buildClientHello("example.test")
	sni, ok := sniFromClientHello(hs)
	if !ok || sni != "example.test" {
		t.Fatalf("sniFromClientHello: got (%q,%v)", sni, ok)
	}
}

// TestForward_UDPEchoRejectsShortHeader — first datagram not a QUIC
// Initial: setupSession returns without registering; nothing echoes
// back. Ensures scanners can't provoke session state on scrappy input.
func TestForward_UDPEchoRejectsShortHeader(t *testing.T) {
	// Backend that echoes if reached — we assert it is NOT.
	backendConn, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	defer func() { _ = backendConn.Close() }()
	reached := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 65535)
		for {
			_, _, err := backendConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			select {
			case reached <- struct{}{}:
			default:
			}
		}
	}()

	srv := New(Config{
		Listen:       "127.0.0.1:0",
		PanelDomain:  "panel.local.test",
		PanelBackend: backendConn.LocalAddr().String(),
	}, nil)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(t.Context()) }()

	client, _ := net.DialUDP("udp", nil, srv.listener.LocalAddr().(*net.UDPAddr))
	defer func() { _ = client.Close() }()
	_, _ = client.Write([]byte{0x00, 0x11, 0x22, 0x33}) // short header garbage

	select {
	case <-reached:
		t.Fatal("garbage datagram reached backend — SNI-gate leaked")
	case <-time.After(200 * time.Millisecond):
		// good: no session was set up.
	}
	if !strings.Contains(srv.Addr(), "127.0.0.1") {
		t.Fatalf("Addr = %s", srv.Addr())
	}
}
