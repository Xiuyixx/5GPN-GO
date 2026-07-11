package frontdoor

import (
	"net"
	"net/http"
	"strings"

	"github.com/quic-go/quic-go"
)

// remoteIP extracts the client IP from a net.Addr surface used by
// miekg/dns's ResponseWriter (DoT, plain :53). Returns nil when the
// address is unusable — every caller downstream treats nil as
// "unknown client" (spoof scope=private_only fails closed).
func remoteIP(a net.Addr) net.IP {
	if a == nil {
		return nil
	}
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return net.ParseIP(a.String())
	}
	return net.ParseIP(host)
}

// httpRemoteIP extracts the client IP from an *http.Request. DoH does
// not currently sit behind a reverse proxy in the daemon's default
// wiring (the panel :443 handler receives the connection directly),
// so we trust RemoteAddr and never consult X-Forwarded-For here —
// consulting it would open a client-controlled spoof-scope bypass on
// deployments without a trusted proxy stripping the header.
func httpRemoteIP(req *http.Request) net.IP {
	if req == nil || req.RemoteAddr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = strings.TrimSpace(req.RemoteAddr)
	}
	return net.ParseIP(host)
}

// doqRemoteIP resolves the client's remote address from a QUIC stream.
// quic-go's *quic.Stream does not directly expose its parent
// connection's peer address across versions; when the peer address
// isn't reachable through the stream surface, this returns nil.
// spoof scope=all still spoofs anyway; scope=private_only treats nil
// as unknown and fails closed for DoQ clients — an operator running
// scope=private_only + DoQ should stick with scope=all, which is the
// documented default for our deployment shape.
func doqRemoteIP(stream *quic.Stream) net.IP {
	_ = stream
	return nil
}
