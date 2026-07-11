// Package quicforward implements a UDP :443 QUIC SNI transparent
// forwarder. It peeks the first datagram of every new client flow,
// decrypts the QUIC v1 Initial packet just enough to read the
// ClientHello SNI (RFC 9000 §17.2.2 + RFC 9001 §5), and pipes UDP
// datagrams between the client and an upstream selected by name.
//
// The QUIC Initial decryption path (parse.go) is a port of
// github.com/Xiuyixx/5GPN-X's quic-proxy.go, MIT-licensed, same
// author. Only the byte-level parser is reused; the session/
// forwarder shell is native.
package quicforward

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

const (
	quicVersion1       = 0x00000001
	longHeaderForm     = 0x80
	initialPacketType  = 0x00
	quicInitialSaltV1  = "\x38\x76\x2c\xf7\xf5\x59\x34\xb3\x4d\x17\x9a\xe6\xa4\xc8\x0c\xad\xcc\xbb\x7f\x0a"
	aeadTagSize        = 16
	tlsExtServerNameID = 0x0000
)

// extractSNI decrypts a QUIC v1 Initial packet from a single UDP
// datagram payload and returns the SNI extracted from the enclosed
// TLS ClientHello. Returns ("", false) for anything that isn't a
// recognisable initial packet or when the SNI extension is absent.
//
// Zero-copy over data: the returned SNI is a fresh Go string; the
// intermediate ciphertext buffers are per-call allocations, but on
// modern kernels this parse is ~200µs per Initial and doesn't need
// pooling for the daemon's expected QPS.
func extractSNI(data []byte) (string, bool) {
	if len(data) < 5 {
		return "", false
	}
	if data[0]&longHeaderForm == 0 {
		return "", false // short header, not an Initial
	}
	version := binary.BigEndian.Uint32(data[1:5])
	if version != quicVersion1 {
		return "", false
	}
	pktType := (data[0] & 0x30) >> 4
	if pktType != initialPacketType {
		return "", false
	}

	off := 5
	if len(data) < off+1 {
		return "", false
	}
	dcidLen := int(data[off])
	off++
	if len(data) < off+dcidLen {
		return "", false
	}
	dcid := data[off : off+dcidLen]
	off += dcidLen

	if len(data) < off+1 {
		return "", false
	}
	scidLen := int(data[off])
	off++
	if len(data) < off+scidLen {
		return "", false
	}
	off += scidLen

	tokenLen, n := readVarint(data[off:])
	if n == 0 || len(data) < off+n+int(tokenLen) {
		return "", false
	}
	off += n + int(tokenLen)

	length, n := readVarint(data[off:])
	if n == 0 || uint64(len(data)-off) < length {
		return "", false
	}
	off += n
	if off+int(length) > len(data) {
		return "", false
	}
	protected := data[off : off+int(length)]

	// Derive Initial keys via HKDF-Extract + HKDF-Expand-Label (RFC 9001).
	initialSecret := hkdfExtract([]byte(quicInitialSaltV1), dcid)
	clientSecret := hkdfExpandLabel(initialSecret, "client in", nil, 32)
	key := hkdfExpandLabel(clientSecret, "quic key", nil, 16)
	iv := hkdfExpandLabel(clientSecret, "quic iv", nil, 12)
	hp := hkdfExpandLabel(clientSecret, "quic hp", nil, 16)

	hpCipher, err := aes.NewCipher(hp)
	if err != nil {
		return "", false
	}
	sampleOff := 4
	if len(protected) < sampleOff+16 {
		sampleOff = 0
	}
	if sampleOff+16 > len(protected) {
		return "", false
	}
	sample := protected[sampleOff : sampleOff+16]
	mask := make([]byte, 16)
	hpCipher.Encrypt(mask, sample)

	// RFC 9001 §5.4.1: long headers use mask[0] & 0x0f (low 4 bits);
	// short headers use mask[0] & 0x1f (low 5 bits). The 5GPN-X port
	// this file is derived from used 0x1f unconditionally — which
	// works for ~half of Initial packets (whenever mask[0]'s 0x10 bit
	// happens to be zero) and fails AEAD verification for the other
	// half. We only ever look at long-header Initials here, so 0x0f
	// is authoritative.
	firstByte := data[0] ^ (mask[0] & 0x0f)
	pnLen := int(firstByte&0x03) + 1
	if len(protected) < pnLen {
		return "", false
	}
	packetNumber := make([]byte, pnLen)
	for i := 0; i < pnLen; i++ {
		packetNumber[i] = protected[i] ^ mask[1+i]
	}

	nonce := make([]byte, 12)
	copy(nonce, iv)
	for i := 0; i < pnLen; i++ {
		nonce[11-i] ^= packetNumber[pnLen-1-i]
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", false
	}

	aad := make([]byte, off+pnLen)
	copy(aad, data[:off+pnLen])
	aad[0] = firstByte
	copy(aad[off:], packetNumber)

	ciphertext := protected[pnLen:]
	if len(ciphertext) < aeadTagSize {
		return "", false
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", false
	}

	return parseCryptoFrames(plaintext)
}

// readVarint decodes a QUIC variable-length integer (RFC 9000 §16).
// Returns (value, bytes_consumed); bytes_consumed==0 on failure.
func readVarint(buf []byte) (uint64, int) {
	if len(buf) == 0 {
		return 0, 0
	}
	first := buf[0]
	prefix := first >> 6
	length := 1 << prefix
	if len(buf) < length {
		return 0, 0
	}
	var val uint64
	switch length {
	case 1:
		val = uint64(first & 0x3f)
	case 2:
		val = uint64(first&0x3f)<<8 | uint64(buf[1])
	case 4:
		val = uint64(first&0x3f)<<24 | uint64(buf[1])<<16 | uint64(buf[2])<<8 | uint64(buf[3])
	case 8:
		val = binary.BigEndian.Uint64(buf)
		val &= 0x3fffffffffffffff
	}
	return val, length
}

func hkdfExtract(salt, ikm []byte) []byte {
	m := hmac.New(sha256.New, salt)
	m.Write(ikm)
	return m.Sum(nil)
}

func hkdfExpand(prk, info []byte, length int) []byte {
	var okm []byte
	var prev []byte
	n := (length + sha256.Size - 1) / sha256.Size
	for i := 1; i <= n; i++ {
		m := hmac.New(sha256.New, prk)
		m.Write(prev)
		m.Write(info)
		m.Write([]byte{byte(i)})
		prev = m.Sum(nil)
		okm = append(okm, prev...)
	}
	return okm[:length]
}

func hkdfExpandLabel(secret []byte, label string, context []byte, length int) []byte {
	qlabel := "tls13 " + label
	lbl := make([]byte, 0, 2+1+len(qlabel)+1+len(context))
	lbl = append(lbl, byte(length>>8), byte(length))
	lbl = append(lbl, byte(len(qlabel)))
	lbl = append(lbl, qlabel...)
	lbl = append(lbl, byte(len(context)))
	lbl = append(lbl, context...)
	return hkdfExpand(secret, lbl, length)
}

// parseCryptoFrames walks the plaintext of a decrypted Initial packet
// looking for a CRYPTO frame (type 0x06) that contains the TLS
// ClientHello. PADDING (0x00), ACK (0x01/0x02) and PING (0x03) are
// skipped; unknown frame types abort parsing.
func parseCryptoFrames(plaintext []byte) (string, bool) {
	off := 0
	for off < len(plaintext) {
		frameType := plaintext[off]
		off++
		switch frameType {
		case 0x00:
			for off < len(plaintext) && plaintext[off] == 0x00 {
				off++
			}
		case 0x06:
			_, n := readVarint(plaintext[off:])
			if n == 0 {
				return "", false
			}
			off += n
			dataLen, n := readVarint(plaintext[off:])
			if n == 0 || uint64(len(plaintext)-off) < dataLen {
				return "", false
			}
			off += n
			data := plaintext[off : off+int(dataLen)]
			if sni, ok := sniFromClientHello(data); ok && sni != "" {
				return sni, true
			}
			off += int(dataLen)
		case 0x01, 0x02, 0x03:
			_, n := readVarint(plaintext[off:])
			if n == 0 {
				return "", false
			}
			off += n
			_, n = readVarint(plaintext[off:])
			if n == 0 {
				return "", false
			}
			off += n
			ackRangeCount, n := readVarint(plaintext[off:])
			if n == 0 {
				return "", false
			}
			off += n
			_, n = readVarint(plaintext[off:])
			if n == 0 {
				return "", false
			}
			off += n
			for i := uint64(0); i < ackRangeCount; i++ {
				_, n = readVarint(plaintext[off:])
				if n == 0 {
					return "", false
				}
				off += n
				_, n = readVarint(plaintext[off:])
				if n == 0 {
					return "", false
				}
				off += n
			}
			if frameType == 0x03 {
				for i := 0; i < 3; i++ {
					_, n = readVarint(plaintext[off:])
					if n == 0 {
						return "", false
					}
					off += n
				}
			}
		default:
			return "", false
		}
	}
	return "", false
}

// sniFromClientHello parses a TLS ClientHello (either wrapped in a TLS
// record header or as raw handshake bytes, which is what QUIC CRYPTO
// frames carry) and returns the SNI extension's first host_name entry.
func sniFromClientHello(data []byte) (string, bool) {
	if len(data) < 5 {
		return "", false
	}
	var hs []byte
	if data[0] == 0x16 { // TLS record
		recLen := binary.BigEndian.Uint16(data[3:5])
		if len(data) < 5+int(recLen) {
			return "", false
		}
		hs = data[5 : 5+int(recLen)]
	} else if data[0] == 0x01 { // raw handshake (QUIC)
		hs = data
	} else {
		return "", false
	}
	if len(hs) < 4 || hs[0] != 0x01 {
		return "", false
	}
	hello := hs[4:]
	if len(hello) < 34 {
		return "", false
	}
	off := 34
	if len(hello) < off+1 {
		return "", false
	}
	sidLen := int(hello[off])
	off++
	if len(hello) < off+sidLen+2 {
		return "", false
	}
	off += sidLen
	csLen := binary.BigEndian.Uint16(hello[off : off+2])
	off += 2
	if len(hello) < off+int(csLen)+1 {
		return "", false
	}
	off += int(csLen)
	cmLen := int(hello[off])
	off++
	if len(hello) < off+cmLen+2 {
		return "", false
	}
	off += cmLen
	extLen := binary.BigEndian.Uint16(hello[off : off+2])
	off += 2
	if len(hello) < off+int(extLen) {
		return "", false
	}
	exts := hello[off : off+int(extLen)]
	eo := 0
	for eo+4 <= len(exts) {
		extType := binary.BigEndian.Uint16(exts[eo : eo+2])
		extDLen := int(binary.BigEndian.Uint16(exts[eo+2 : eo+4]))
		if eo+4+extDLen > len(exts) {
			return "", false
		}
		if extType == tlsExtServerNameID {
			d := exts[eo+4 : eo+4+extDLen]
			if len(d) < 2 {
				return "", false
			}
			listLen := int(binary.BigEndian.Uint16(d[0:2]))
			if listLen > len(d)-2 {
				return "", false
			}
			sni := d[2 : 2+listLen]
			if len(sni) < 3 || sni[0] != 0x00 {
				return "", false
			}
			nameLen := int(binary.BigEndian.Uint16(sni[1:3]))
			if nameLen > len(sni)-3 {
				return "", false
			}
			return string(sni[3 : 3+nameLen]), true
		}
		eo += 4 + extDLen
	}
	return "", false
}
