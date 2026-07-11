// Package sniforward implements a TCP :443 SNI-based transparent
// forwarder. It peeks the first TLS record on the wire, extracts the
// ClientHello's Server Name Indication (SNI), and dials the upstream
// selected by name — the panel's internal HTTPS backend for the
// operator's own domain, the real target host for everything else.
//
// It never decrypts TLS or terminates the connection. The record it
// peeked is transparently replayed to the upstream so the handshake
// completes end-to-end between the client and the real origin.
package sniforward

import (
	"encoding/binary"
	"errors"
	"io"
)

// TLS record + handshake layout used by peekSNI. Field sizes are from
// RFC 5246 §6.2.1 (TLS record) and RFC 5246 §7.4 / RFC 6066 §3 (SNI
// extension). This is a minimum parser, not a general TLS decoder —
// we only need enough of the ClientHello to pull the SNI and stop.
const (
	tlsRecordTypeHandshake byte = 0x16
	tlsHandshakeClientHello byte = 0x01
	tlsExtServerName        uint16 = 0x0000

	tlsHeaderLen     = 5      // ContentType(1) + Version(2) + Length(2)
	maxTLSRecordSize = 16640  // 16384 + slack for tests
)

// ErrNoSNI is returned when the peeked ClientHello has no
// server_name extension. Callers should fall back to a default
// upstream or drop.
var ErrNoSNI = errors.New("sniforward: no SNI in ClientHello")

// ErrNotTLS signals the client didn't send a TLS handshake record —
// most commonly a plain HTTP client hitting :443 by mistake, or a
// scanner probing the port.
var ErrNotTLS = errors.New("sniforward: not a TLS handshake")

// peekSNI reads the first TLS record off r, extracts the SNI, and
// returns:
//   - sni: the extracted server_name (may be "" with a nil error only
//     when the ClientHello parses cleanly but omits SNI — this case
//     is normalized to (empty, ErrNoSNI) for callers).
//   - raw: the exact byte-for-byte record read, so the caller can
//     replay it to the upstream before beginning the copy loop.
//   - err: parse error or io.EOF.
//
// r is normally a *bufio.Reader wrapping the client conn; io.ReadFull
// against a bufio.Reader lets peek+replay work without seeking, which
// a raw net.Conn wouldn't support.
func peekSNI(r io.Reader) (sni string, raw []byte, err error) {
	head := make([]byte, tlsHeaderLen)
	if _, err = io.ReadFull(r, head); err != nil {
		return "", nil, err
	}
	if head[0] != tlsRecordTypeHandshake {
		return "", head, ErrNotTLS
	}
	recLen := int(binary.BigEndian.Uint16(head[3:5]))
	if recLen <= 0 || recLen > maxTLSRecordSize {
		return "", head, errors.New("sniforward: implausible TLS record length")
	}
	body := make([]byte, recLen)
	if _, err = io.ReadFull(r, body); err != nil {
		return "", head, err
	}
	raw = append(head, body...)

	sni, err = extractSNIFromHandshake(body)
	if err != nil {
		return "", raw, err
	}
	return sni, raw, nil
}

// extractSNIFromHandshake walks the ClientHello structure and returns
// the server_name from the SNI extension, or ErrNoSNI if the
// extension is absent.
//
// ClientHello layout (RFC 5246 §7.4.1.2):
//   handshake_type (1) = 0x01
//   length         (3)
//   client_version (2)
//   random         (32)
//   session_id     (var, len prefixed by 1 byte)
//   cipher_suites  (var, len prefixed by 2 bytes)
//   compression    (var, len prefixed by 1 byte)
//   extensions     (var, len prefixed by 2 bytes) [optional in older
//                                                  versions; assumed
//                                                  present for SNI]
func extractSNIFromHandshake(b []byte) (string, error) {
	if len(b) < 4 || b[0] != tlsHandshakeClientHello {
		return "", errors.New("sniforward: not a ClientHello")
	}
	hsLen := int(uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
	if hsLen+4 > len(b) {
		return "", errors.New("sniforward: truncated ClientHello")
	}
	p := b[4 : 4+hsLen]

	// client_version (2) + random (32)
	if len(p) < 34 {
		return "", errors.New("sniforward: ClientHello prefix too short")
	}
	p = p[34:]

	// session_id
	if len(p) < 1 {
		return "", errors.New("sniforward: missing session_id length")
	}
	sidLen := int(p[0])
	if 1+sidLen > len(p) {
		return "", errors.New("sniforward: session_id overflow")
	}
	p = p[1+sidLen:]

	// cipher_suites
	if len(p) < 2 {
		return "", errors.New("sniforward: missing cipher_suites length")
	}
	csLen := int(binary.BigEndian.Uint16(p[:2]))
	if 2+csLen > len(p) {
		return "", errors.New("sniforward: cipher_suites overflow")
	}
	p = p[2+csLen:]

	// compression_methods
	if len(p) < 1 {
		return "", errors.New("sniforward: missing compression length")
	}
	compLen := int(p[0])
	if 1+compLen > len(p) {
		return "", errors.New("sniforward: compression overflow")
	}
	p = p[1+compLen:]

	// Extensions may be absent in TLS 1.0/1.1 hellos; treat as no-SNI.
	if len(p) < 2 {
		return "", ErrNoSNI
	}
	extLen := int(binary.BigEndian.Uint16(p[:2]))
	if 2+extLen > len(p) {
		return "", errors.New("sniforward: extensions overflow")
	}
	exts := p[2 : 2+extLen]

	for len(exts) >= 4 {
		extType := binary.BigEndian.Uint16(exts[:2])
		thisLen := int(binary.BigEndian.Uint16(exts[2:4]))
		if 4+thisLen > len(exts) {
			return "", errors.New("sniforward: extension overflow")
		}
		if extType == tlsExtServerName {
			return parseServerNameList(exts[4 : 4+thisLen])
		}
		exts = exts[4+thisLen:]
	}
	return "", ErrNoSNI
}

// parseServerNameList reads the ServerNameList structure (RFC 6066 §3)
// and returns the first host_name entry. The extension supports
// multiple entries but every TLS deployment in the wild sends exactly
// one; we return the first host_name (name_type == 0) and skip
// anything else.
func parseServerNameList(b []byte) (string, error) {
	if len(b) < 2 {
		return "", errors.New("sniforward: server_name_list too short")
	}
	listLen := int(binary.BigEndian.Uint16(b[:2]))
	if 2+listLen > len(b) {
		return "", errors.New("sniforward: server_name_list overflow")
	}
	p := b[2 : 2+listLen]
	for len(p) >= 3 {
		nameType := p[0]
		nameLen := int(binary.BigEndian.Uint16(p[1:3]))
		if 3+nameLen > len(p) {
			return "", errors.New("sniforward: server_name entry overflow")
		}
		if nameType == 0 { // host_name
			return string(p[3 : 3+nameLen]), nil
		}
		p = p[3+nameLen:]
	}
	return "", ErrNoSNI
}
