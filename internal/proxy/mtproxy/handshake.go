// Package mtproxy implements a Telegram MTProto proxy speaking the
// obfuscated2 transport with the "dd" (padded intermediate) secret form.
// This is the DPI-resistant protocol every current Telegram client
// speaks when the secret string starts with "dd" — the format that
// tg://proxy?secret=dd… URLs describe.
//
// Fake-TLS mode (ee secrets, TLS-1.3 ClientHello disguise) is NOT
// implemented: the code path is complex, requires a real certificate
// domain, and adds no value for clients that already accept dd secrets.
package mtproxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// HandshakeSize is the fixed length of the obfuscated2 initial packet.
// 64 bytes: 8 random padding + 32 key material + 16 IV + 8 metadata
// (protocol id, dc id, reserved).
const HandshakeSize = 64

// SecretSize is the length of the inner 16-byte secret that a "dd"
// secret carries after its 1-byte 0xdd prefix.
const SecretSize = 16

// Obfuscated2 protocol ids that can appear in the last 8 bytes of the
// decrypted handshake. All three are legitimate MTProto transports and
// this proxy forwards each of them transparently — the framing layer
// (length-prefixing style, padding) is a client↔DC contract that the
// byte relay never inspects. The proxy's sole responsibility is to
// emit the SAME protocol id on the upstream handshake so the DC
// decodes the client's frames correctly on the other end of the
// AES-CTR decrypt+re-encrypt pipe.
//
//   - protoAbridged           — 1-byte length prefix (4-byte units).
//   - protoIntermediate       — 4-byte LE length prefix (byte units).
//   - protoPaddedIntermediate — intermediate + 0..3 pad bytes per frame
//     (default for "dd" secrets, DPI-obfuscation trick).
const (
	protoAbridged           uint32 = 0xefefefef
	protoIntermediate       uint32 = 0xeeeeeeee
	protoPaddedIntermediate uint32 = 0xdddddddd
)

var (
	// ErrTooShort — client sent fewer than HandshakeSize bytes before
	// close. Usually a scanner probe or half-open connect.
	ErrTooShort = errors.New("mtproxy: handshake short read")

	// ErrForbiddenPrefix — first 4 bytes match an HTTP/other probe
	// pattern the obfuscated2 spec forbids. Signals a non-Telegram
	// client (scanner, HTTP proxy misprobe) — reject cheaply.
	ErrForbiddenPrefix = errors.New("mtproxy: forbidden handshake prefix")

	// ErrProtocolMismatch — decrypted protocol id isn't one of the
	// three obfuscated2 variants we accept (abridged 0xefefefef,
	// intermediate 0xeeeeeeee, padded intermediate 0xdddddddd). Either
	// the client is speaking a legacy/fake-TLS transport or the secret
	// is wrong; both mean this connection can't proceed.
	ErrProtocolMismatch = errors.New("mtproxy: protocol mismatch (want abridged, intermediate, or padded intermediate)")
)

// ClientSession holds the two AES-CTR streams, the DC id, and the
// obfuscated2 protocol variant decoded from a client's handshake.
// streamRecv decrypts inbound bytes; streamSend encrypts outbound
// bytes; DC identifies which Telegram DC the client wants to reach;
// Protocol records which of the three obfuscated2 variants the client
// chose (0xefefefef abridged, 0xeeeeeeee intermediate, 0xdddddddd
// padded intermediate) so the proxy can re-advertise the SAME variant
// on the upstream handshake and preserve end-to-end framing.
type ClientSession struct {
	StreamRecv cipher.Stream
	StreamSend cipher.Stream
	DC         int16
	Protocol   uint32
}

// DecodeClientHandshake reads exactly HandshakeSize bytes from r,
// derives the AES-256-CTR streams keyed by secret, decrypts the
// handshake, validates the protocol id, and returns the session.
//
// Wire layout (per obfuscated2 spec):
//
//	 0..8   : random padding
//	 8..40  : key material for c→s stream (32 bytes)
//	40..56  : IV for c→s stream (16 bytes)
//	56..60  : protocol id (must be one of the three obfuscated2 variants)
//	60..62  : DC id (int16, little-endian; negative = media DC)
//	62..64  : reserved (ignored)
//
// The s→c stream is keyed by the byte-reverse of bytes 8..56, so that
// two ends of a proxy can derive matching streams without an extra
// exchange.
func DecodeClientHandshake(r io.Reader, secret [SecretSize]byte) (*ClientSession, [HandshakeSize]byte, error) {
	var enc [HandshakeSize]byte
	if _, err := io.ReadFull(r, enc[:]); err != nil {
		return nil, enc, ErrTooShort
	}
	if forbiddenPrefix(enc[:4]) {
		return nil, enc, ErrForbiddenPrefix
	}

	// Key derivation happens over the ciphertext bytes: this is
	// what the client used, so the two ends compute the same keys
	// without ever sending the key material in the clear.
	keyRecv := sha256.Sum256(append(append([]byte{}, enc[8:40]...), secret[:]...))
	ivRecv := enc[40:56]
	streamRecv, err := newCTR(keyRecv[:], ivRecv)
	if err != nil {
		return nil, enc, err
	}

	reversed := reverseBytes(enc[8:56])
	keySend := sha256.Sum256(append(reversed[:32], secret[:]...))
	ivSend := reversed[32:48]
	streamSend, err := newCTR(keySend[:], ivSend)
	if err != nil {
		return nil, enc, err
	}

	// Decrypt the whole 64-byte header IN PLACE so we can read the
	// protocol/dc metadata. This also advances streamRecv to
	// position 64, ready for the subsequent MTProto payload.
	var dec [HandshakeSize]byte
	streamRecv.XORKeyStream(dec[:], enc[:])

	proto := binary.LittleEndian.Uint32(dec[56:60])
	switch proto {
	case protoAbridged, protoIntermediate, protoPaddedIntermediate:
		// Accept all three obfuscated2 transport variants; the
		// upstream handshake will re-advertise the same one to the
		// DC so end-to-end framing survives the byte relay.
	default:
		return nil, enc, fmt.Errorf("%w (got 0x%08x)", ErrProtocolMismatch, proto)
	}
	dc := int16(binary.LittleEndian.Uint16(dec[60:62]))

	return &ClientSession{
		StreamRecv: streamRecv,
		StreamSend: streamSend,
		DC:         dc,
		Protocol:   proto,
	}, dec, nil
}

// UpstreamSession is the counterpart to ClientSession for the proxy's
// outbound connection to a real Telegram DC. The wire bytes we send
// to the DC (Header) already include the encrypted 64-byte handshake;
// StreamSend/StreamRecv are positioned at offset 64 so subsequent
// payload continues seamlessly.
type UpstreamSession struct {
	StreamRecv cipher.Stream
	StreamSend cipher.Stream
	Header     [HandshakeSize]byte
}

// EncodeUpstreamHandshake builds a fresh 64-byte handshake for the
// outbound connection to Telegram DC. No secret is used (DC-side
// obfuscated2 doesn't require one — the obfuscation exists purely to
// evade DPI, DCs accept any random keys). `proto` MUST be the same
// obfuscated2 variant the client used (see ClientSession.Protocol);
// the relay is a raw byte pipe, so the DC parses the client's frames
// against whatever protocol id we advertise here — advertising a
// different variant than the client would corrupt every frame.
// Returns the encrypted bytes to write to the DC plus the two CTR
// streams for subsequent payload.
func EncodeUpstreamHandshake(dc int16, proto uint32) (*UpstreamSession, error) {
	var raw [HandshakeSize]byte
	// Try up to 128 times to draw a random header that doesn't
	// collide with a forbidden prefix. In practice one attempt
	// works — the forbidden set is tiny.
	for i := 0; i < 128; i++ {
		if _, err := io.ReadFull(rand.Reader, raw[:56]); err != nil {
			return nil, err
		}
		if !forbiddenPrefix(raw[:4]) {
			break
		}
	}

	// Empty secret: DC doesn't authenticate obfuscation, only
	// requires the on-wire pattern.
	var empty []byte

	keySend := sha256.Sum256(append(append([]byte{}, raw[8:40]...), empty...))
	ivSend := raw[40:56]
	streamSend, err := newCTR(keySend[:], ivSend)
	if err != nil {
		return nil, err
	}

	// Compose the plaintext metadata (protocol id + dc id) into
	// bytes 56..64 of raw, then advance streamSend past the first
	// 56 random bytes so the encryption of the metadata lines up
	// on-wire with what a legitimate obfuscated2 client would emit.
	binary.LittleEndian.PutUint32(raw[56:60], proto)
	binary.LittleEndian.PutUint16(raw[60:62], uint16(dc))
	// raw[62:64] stays random from the initial read.

	pad := make([]byte, 56)
	streamSend.XORKeyStream(pad, pad) // advance stream to offset 56
	streamSend.XORKeyStream(raw[56:64], raw[56:64])

	// Now that raw[8:56] is finalized (bytes 8..56 were not touched
	// by the encryption above), we can derive the receive stream
	// from its byte-reverse — matching how the DC will derive its
	// send stream when replying to us.
	reversed := reverseBytes(raw[8:56])
	keyRecv := sha256.Sum256(append(reversed[:32], empty...))
	ivRecv := reversed[32:48]
	streamRecv, err := newCTR(keyRecv[:], ivRecv)
	if err != nil {
		return nil, err
	}

	return &UpstreamSession{
		StreamRecv: streamRecv,
		StreamSend: streamSend,
		Header:     raw,
	}, nil
}

// GenerateSecret returns 16 fresh random bytes suitable for use as the
// inner secret of a dd-form MTProto secret. Callers hex-encode this
// (with a leading "dd") to hand to Telegram clients.
func GenerateSecret() ([SecretSize]byte, error) {
	var s [SecretSize]byte
	if _, err := io.ReadFull(rand.Reader, s[:]); err != nil {
		return s, err
	}
	return s, nil
}

// forbiddenPrefix reports whether the first 4 bytes of an obfuscated2
// handshake match a pattern the spec forbids. These would collide with
// HTTP/other transport probes and are how DPI equipment blocks naive
// obfuscated MTProto. Legitimate clients pick a new random header
// until they land outside this set; so do we.
func forbiddenPrefix(b []byte) bool {
	if len(b) < 4 {
		return true
	}
	// 4-byte big-endian view (ASCII HTTP verbs are BE readable).
	be := binary.BigEndian.Uint32(b)
	switch be {
	case 0x48454144, // "HEAD"
		0x504f5354, // "POST"
		0x47455420, // "GET "
		0x4f505449, // "OPTI"
		0xdddddddd, // padded intermediate (protocol id, not allowed as start)
		0xefefefef, // abridged
		0xeeeeeeee, // intermediate
		0x00000000:
		return true
	}
	// 4-byte little-endian TLS ClientHello sentinel: 0x16030101 is
	// TLS record 22, version 3.1 (TLS 1.0 legacy record) — also
	// used by mtproxy fake-TLS mode which we don't want to be
	// confused with.
	if b[0] == 0x16 && b[1] == 0x03 && b[2] == 0x01 && b[3] == 0x02 {
		return true
	}
	return false
}

// reverseBytes returns a new slice with s in reversed byte order.
func reverseBytes(s []byte) []byte {
	out := make([]byte, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

func newCTR(key, iv []byte) (cipher.Stream, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewCTR(block, iv), nil
}
