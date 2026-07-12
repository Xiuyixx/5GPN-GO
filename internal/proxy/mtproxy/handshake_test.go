package mtproxy

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHandshake_ClientToServerRoundTrip pins the obfuscated2 spec:
// build a client-side handshake exactly as a real Telegram client
// would, hand it to DecodeClientHandshake, and assert the recovered
// DC id and stream positions match.
func TestHandshake_ClientToServerRoundTrip(t *testing.T) {
	var secret [SecretSize]byte
	for i := range secret {
		secret[i] = byte(i * 3) // deterministic for reproducibility
	}
	wantDC := int16(2)

	enc := buildClientHandshake(t, secret, wantDC, protoPaddedIntermediate)

	cs, decHeader, err := DecodeClientHandshake(bytes.NewReader(enc[:]), secret)
	if err != nil {
		t.Fatalf("DecodeClientHandshake: %v", err)
	}
	if cs.DC != wantDC {
		t.Fatalf("DC = %d, want %d", cs.DC, wantDC)
	}
	if got := binary.LittleEndian.Uint32(decHeader[56:60]); got != protoPaddedIntermediate {
		t.Fatalf("protocol = %08x, want %08x", got, protoPaddedIntermediate)
	}

	// The recv stream must be positioned at offset 64 — decrypting
	// the next 8 client bytes should produce whatever the client
	// XOR'd them with, which we verify by round-trip.
	payload := []byte("HELLO_TG")
	streamAtClient := clientSendStream(t, secret, enc)
	encPayload := make([]byte, len(payload))
	streamAtClient.XORKeyStream(encPayload, payload)

	got := make([]byte, len(encPayload))
	cs.StreamRecv.XORKeyStream(got, encPayload)
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload round-trip: got %q, want %q", got, payload)
	}
}

// TestHandshake_ServerToClientRoundTrip mirrors the previous test in
// the reverse direction: the server encrypts, the client decrypts.
// This pins the reversed-header key derivation.
func TestHandshake_ServerToClientRoundTrip(t *testing.T) {
	var secret [SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	enc := buildClientHandshake(t, secret, 5, protoPaddedIntermediate)
	cs, _, err := DecodeClientHandshake(bytes.NewReader(enc[:]), secret)
	if err != nil {
		t.Fatal(err)
	}

	// A real Telegram client, upon completing the same handshake,
	// derives a receive stream from reverse(enc[8:56]) + secret.
	clientRecv := clientRecvStream(t, secret, enc)

	msg := []byte("SERVER-INITIATED-BYTES")
	ciphertext := make([]byte, len(msg))
	cs.StreamSend.XORKeyStream(ciphertext, msg)
	plaintext := make([]byte, len(msg))
	clientRecv.XORKeyStream(plaintext, ciphertext)
	if !bytes.Equal(plaintext, msg) {
		t.Fatalf("s→c round-trip failed: got %q", plaintext)
	}
}

// TestHandshake_RejectsForbiddenPrefix — packets starting with "GET "
// or an HTTP verb must be refused cheaply before we do crypto work.
func TestHandshake_RejectsForbiddenPrefix(t *testing.T) {
	var secret [SecretSize]byte
	pkt := make([]byte, HandshakeSize)
	copy(pkt, "GET / HTTP/1.1\r\n") // "GET " ... starts with a banned verb
	_, _, err := DecodeClientHandshake(bytes.NewReader(pkt), secret)
	if err != ErrForbiddenPrefix {
		t.Fatalf("err = %v, want ErrForbiddenPrefix", err)
	}
}

// TestHandshake_AcceptsAllThreeProtocols — the proxy must accept
// abridged, intermediate, and padded intermediate obfuscated2 variants
// and preserve which one the client picked on the ClientSession so the
// upstream handshake can re-advertise the same variant to the DC.
func TestHandshake_AcceptsAllThreeProtocols(t *testing.T) {
	var secret [SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		proto uint32
	}{
		{"abridged", protoAbridged},
		{"intermediate", protoIntermediate},
		{"padded_intermediate", protoPaddedIntermediate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc := buildClientHandshake(t, secret, 2, tc.proto)
			cs, _, err := DecodeClientHandshake(bytes.NewReader(enc[:]), secret)
			if err != nil {
				t.Fatalf("DecodeClientHandshake: %v", err)
			}
			if cs.Protocol != tc.proto {
				t.Fatalf("Protocol = 0x%08x, want 0x%08x", cs.Protocol, tc.proto)
			}
			if cs.DC != 2 {
				t.Fatalf("DC = %d, want 2", cs.DC)
			}
		})
	}
}

// TestHandshake_RejectsWrongProtocol — decrypts a handshake whose last
// 8 bytes encode a bogus protocol id (not one of the three obfuscated2
// variants); the proxy must refuse it.
func TestHandshake_RejectsWrongProtocol(t *testing.T) {
	var secret [SecretSize]byte
	rand.Read(secret[:])
	enc := buildClientHandshake(t, secret, 2, 0xcafebabe) // not a valid MTProto proto id
	_, _, err := DecodeClientHandshake(bytes.NewReader(enc[:]), secret)
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("err = %v, want ErrProtocolMismatch", err)
	}
}

// TestHandshake_RejectsShortRead — half a packet must not crash.
func TestHandshake_RejectsShortRead(t *testing.T) {
	var secret [SecretSize]byte
	_, _, err := DecodeClientHandshake(bytes.NewReader([]byte{1, 2, 3}), secret)
	if err != ErrTooShort {
		t.Fatalf("err = %v, want ErrTooShort", err)
	}
}

// TestUpstreamHandshake_DecodedAtDC — an outbound handshake that a
// real DC would parse must yield the DC id and protocol id we told it
// to encode, for every supported obfuscated2 variant. We simulate the
// DC by decrypting with no-secret key derivation.
func TestUpstreamHandshake_DecodedAtDC(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto uint32
	}{
		{"abridged", protoAbridged},
		{"intermediate", protoIntermediate},
		{"padded_intermediate", protoPaddedIntermediate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			us, err := EncodeUpstreamHandshake(3, tc.proto)
			if err != nil {
				t.Fatalf("EncodeUpstreamHandshake: %v", err)
			}
			proto, dc := decodeMetadataNoSecret(us.Header)
			if proto != tc.proto {
				t.Fatalf("proto = 0x%08x, want 0x%08x", proto, tc.proto)
			}
			if dc != 3 {
				t.Fatalf("dc = %d, want 3", dc)
			}
			if us.StreamSend == nil || us.StreamRecv == nil {
				t.Fatal("streams unset")
			}
		})
	}
}

// TestRelay_PipesBothDirections — the smoke test that the whole
// handshake+relay stack matches the spec. A fake DC accepts the
// outbound handshake, echoes any payload back, and we verify that
// bytes sent from a "client" arrive at the fake DC and vice versa.
func TestRelay_PipesBothDirections(t *testing.T) {
	var secret [SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}

	cliConnClient, cliConnServer := net.Pipe()
	dcConnServer, dcConnClient := net.Pipe()
	t.Cleanup(func() {
		cliConnClient.Close()
		cliConnServer.Close()
		dcConnServer.Close()
		dcConnClient.Close()
	})

	// Build the client-side view. It sends its handshake first,
	// then encrypted payload.
	clientHeader := buildClientHandshake(t, secret, 2, protoPaddedIntermediate)
	clientSend := clientSendStream(t, secret, clientHeader)
	clientRecv := clientRecvStream(t, secret, clientHeader)

	// Server side reads the handshake it will forward. We already
	// have the encrypted header (clientHeader), so put it into the
	// pipe from the client side and let DecodeClientHandshake read
	// it on the server.
	go func() {
		_, _ = cliConnClient.Write(clientHeader[:])
		// Then the client sends "PING" encrypted.
		encPing := make([]byte, 4)
		clientSend.XORKeyStream(encPing, []byte("PING"))
		_, _ = cliConnClient.Write(encPing)
	}()

	cs, _, err := DecodeClientHandshake(cliConnServer, secret)
	if err != nil {
		t.Fatalf("DecodeClientHandshake: %v", err)
	}

	us, err := EncodeUpstreamHandshake(cs.DC, cs.Protocol)
	if err != nil {
		t.Fatal(err)
	}

	// Fake DC: consume our handshake, decrypt with no-secret keys,
	// echo received payload back.
	dcDone := make(chan struct{})
	go func() {
		defer close(dcDone)
		var incoming [HandshakeSize]byte
		if _, err := io.ReadFull(dcConnClient, incoming[:]); err != nil {
			return
		}
		var noSecret [SecretSize]byte
		_ = noSecret
		dcRecv, dcSend := deriveDCStreams(incoming)
		// Decrypt the first 4 bytes of payload (the "PING").
		buf := make([]byte, 4)
		if _, err := io.ReadFull(dcConnClient, buf); err != nil {
			return
		}
		dcRecv.XORKeyStream(buf, buf)
		// Echo "PONG" back, encrypted.
		out := []byte("PONG")
		dcSend.XORKeyStream(out, out)
		_, _ = dcConnClient.Write(out)
		if !bytes.Equal(buf, []byte("PING")) {
			t.Errorf("DC got %q, want PING", buf)
		}
	}()

	// Write our upstream handshake first.
	if _, err := dcConnServer.Write(us.Header[:]); err != nil {
		t.Fatal(err)
	}

	// Now run the relay for a short window and stop it.
	relay := NewRelay(cliConnServer, dcConnServer, cs, us)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Run(ctx) }()

	// Read PONG from the client-side view.
	expected := make([]byte, 4)
	if _, err := io.ReadFull(cliConnClient, expected); err != nil {
		t.Fatalf("read PONG: %v", err)
	}
	clientRecv.XORKeyStream(expected, expected)
	if !bytes.Equal(expected, []byte("PONG")) {
		t.Fatalf("client got %q, want PONG", expected)
	}

	cancel()
	select {
	case <-relayDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("relay did not stop after cancel")
	}
	select {
	case <-dcDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("fake DC did not finish")
	}
}

// TestServer_RejectsHTTPProbeSilently — a scanner sending "GET /"
// must cause the server to close the connection without spending any
// budget beyond the handshake read.
func TestServer_RejectsHTTPProbeSilently(t *testing.T) {
	var secret [SecretSize]byte
	rand.Read(secret[:])
	srv := New(Config{Listen: "127.0.0.1:0", Secret: secret, HandshakeTimeout: 200 * time.Millisecond}, nil)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pkt := []byte("GET / HTTP/1.1\r\nHost: probe\r\n\r\n")
	// Pad to HandshakeSize so ReadFull completes; the server then
	// runs the forbidden-prefix check and closes.
	pad := make([]byte, HandshakeSize-len(pkt))
	if _, err := conn.Write(append(pkt, pad...)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 32)
	// Any read must return EOF / connection reset — nothing useful.
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("server responded to HTTP probe, want silent close")
	} else if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatal("server did not close the probe connection")
	}
}

// TestSecretString_Format — 34 lowercase hex chars starting with "dd".
func TestSecretString_Format(t *testing.T) {
	var secret [SecretSize]byte
	for i := range secret {
		secret[i] = byte(i)
	}
	srv := New(Config{Listen: "127.0.0.1:0", Secret: secret}, nil)
	got := srv.SecretString()
	if len(got) != 34 {
		t.Fatalf("length = %d, want 34", len(got))
	}
	if got[:2] != "dd" {
		t.Fatalf("prefix = %q, want dd", got[:2])
	}
	for _, c := range got[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char %q in %s", c, got)
		}
	}
}

// TestDCAddr_Bounds pins the DC address bounds behavior.
func TestDCAddr_Bounds(t *testing.T) {
	if _, err := DCAddr(0, false); err == nil {
		t.Fatal("dc=0 should error")
	}
	if _, err := DCAddr(6, false); err == nil {
		t.Fatal("dc=6 should error")
	}
	if _, err := DCAddr(-3, false); err != nil {
		t.Fatalf("dc=-3 should map to dc=3, got %v", err)
	}
	addr, err := DCAddr(2, false)
	if err != nil || !strings.Contains(addr, ":443") {
		t.Fatalf("DCAddr(2) = %q, %v", addr, err)
	}
}

// --- helpers ---------------------------------------------------------

// buildClientHandshake mirrors what a real Telegram client (or a
// third-party MTProto client) generates: 56 random bytes plus a
// protocol id and DC id, encrypted with keys derived from the same
// bytes plus the shared secret.
func buildClientHandshake(t *testing.T, secret [SecretSize]byte, dc int16, proto uint32) [HandshakeSize]byte {
	t.Helper()
	var h [HandshakeSize]byte
	for {
		if _, err := rand.Read(h[:56]); err != nil {
			t.Fatal(err)
		}
		if !forbiddenPrefix(h[:4]) {
			break
		}
	}
	binary.LittleEndian.PutUint32(h[56:60], proto)
	binary.LittleEndian.PutUint16(h[60:62], uint16(dc))

	keyC := sha256.Sum256(append(append([]byte{}, h[8:40]...), secret[:]...))
	block, _ := aes.NewCipher(keyC[:])
	stream := cipher.NewCTR(block, h[40:56])
	// Advance past the 56-byte prefix and encrypt bytes 56..64 in place.
	pad := make([]byte, 56)
	stream.XORKeyStream(pad, pad)
	stream.XORKeyStream(h[56:64], h[56:64])
	return h
}

func clientSendStream(t *testing.T, secret [SecretSize]byte, header [HandshakeSize]byte) cipher.Stream {
	t.Helper()
	keyC := sha256.Sum256(append(append([]byte{}, header[8:40]...), secret[:]...))
	block, _ := aes.NewCipher(keyC[:])
	stream := cipher.NewCTR(block, header[40:56])
	pad := make([]byte, HandshakeSize)
	stream.XORKeyStream(pad, pad) // advance past the whole encrypted header
	return stream
}

func clientRecvStream(t *testing.T, secret [SecretSize]byte, header [HandshakeSize]byte) cipher.Stream {
	t.Helper()
	rev := reverseBytes(header[8:56])
	keyS := sha256.Sum256(append(rev[:32], secret[:]...))
	block, _ := aes.NewCipher(keyS[:])
	return cipher.NewCTR(block, rev[32:48])
}

// decodeMetadataNoSecret reproduces the DC-side decryption path (no
// secret) so a test can verify our outbound header without opening a
// real DC connection.
func decodeMetadataNoSecret(h [HandshakeSize]byte) (uint32, int16) {
	keyC := sha256.Sum256(h[8:40])
	block, _ := aes.NewCipher(keyC[:])
	stream := cipher.NewCTR(block, h[40:56])
	var dec [HandshakeSize]byte
	stream.XORKeyStream(dec[:], h[:])
	return binary.LittleEndian.Uint32(dec[56:60]), int16(binary.LittleEndian.Uint16(dec[60:62]))
}

// deriveDCStreams computes both AES-CTR streams a DC would derive
// after receiving header. Recv is the DC's receive (proxy→DC data);
// send is DC's send (DC→proxy data).
func deriveDCStreams(h [HandshakeSize]byte) (recv, send cipher.Stream) {
	keyC := sha256.Sum256(h[8:40])
	block, _ := aes.NewCipher(keyC[:])
	recv = cipher.NewCTR(block, h[40:56])
	// Advance past the whole 64-byte header (DC already consumed it).
	pad := make([]byte, HandshakeSize)
	recv.XORKeyStream(pad, pad)

	rev := reverseBytes(h[8:56])
	keyS := sha256.Sum256(rev[:32])
	block2, _ := aes.NewCipher(keyS[:])
	send = cipher.NewCTR(block2, rev[32:48])
	return
}

// Silence "unused import" if wg import somehow used only in one path.
var _ = sync.WaitGroup{}
