package mtproxy

import (
	"errors"
	"net"
)

// Telegram production DC endpoints, as documented on
// https://core.telegram.org/api/datacenter. Kept as a compile-time
// table rather than a runtime fetch: the addresses change once every
// couple of years, and a network glitch during a fetch would leave
// the proxy dead. When they do change, bump this table.
//
// Positive DC ids are the "main" DCs (auth + updates + media). Media-
// only DCs share the same IP space; there's no negative-id split at
// the transport layer since 2019 — we treat any int16 dc id as
// abs(dc) mod 5 to pick a bucket, matching how tdlib normalizes them.
var dcV4 = [...]string{
	1: "149.154.175.53:443",
	2: "149.154.167.51:443",
	3: "149.154.175.100:443",
	4: "149.154.167.91:443",
	5: "91.108.56.130:443",
}

var dcV6 = [...]string{
	1: "[2001:b28:f23d:f001::a]:443",
	2: "[2001:67c:4e8:f002::a]:443",
	3: "[2001:b28:f23d:f003::a]:443",
	4: "[2001:67c:4e8:f004::a]:443",
	5: "[2001:b28:f23f:f005::a]:443",
}

// ErrInvalidDC — client's decoded handshake pointed to a DC id
// outside 1..5. Real clients never do this; a probe or corrupted
// secret does.
var ErrInvalidDC = errors.New("mtproxy: invalid DC id")

// DCAddr returns the TCP address of the Telegram DC identified by dc.
// preferIPv6 hands back the v6 endpoint when true — some networks
// have better v6 reachability to Telegram than v4.
//
// Negative dc ids (media DC hints from older clients) map to the same
// absolute-value main DC — Telegram folded the media split years ago.
func DCAddr(dc int16, preferIPv6 bool) (string, error) {
	id := dc
	if id < 0 {
		id = -id
	}
	if id < 1 || int(id) >= len(dcV4) {
		return "", ErrInvalidDC
	}
	if preferIPv6 {
		return dcV6[id], nil
	}
	return dcV4[id], nil
}

// DialDC opens a TCP connection to the DC identified by dc, preferring
// v6 when the host has a routable v6. Falls back to v4 on v6 dial
// failure so a broken v6 route doesn't wedge the whole proxy.
func DialDC(dialer *net.Dialer, dc int16, preferIPv6 bool) (net.Conn, error) {
	if preferIPv6 {
		addr, err := DCAddr(dc, true)
		if err != nil {
			return nil, err
		}
		conn, err := dialer.Dial("tcp", addr)
		if err == nil {
			return conn, nil
		}
	}
	addr, err := DCAddr(dc, false)
	if err != nil {
		return nil, err
	}
	return dialer.Dial("tcp", addr)
}
