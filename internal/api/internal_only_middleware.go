// Internal-only access middleware.
//
// Attached to the chi router after cors and before all other
// middlewares/routes (see server.go Router()). When Gate is nil or
// disabled, the middleware is a pass-through — one atomic load per
// request, no per-connection allocation. When enabled, the peer's
// source IP is checked against the allowlist and disallowed clients
// get an immediate 403 with a machine-readable error code.
//
// The following endpoints are ALWAYS bypassed regardless of gate
// state, because they need to be reachable from any client IP for the
// panel to function under a hostile network:
//
//   /api/v1/health          — monitoring probes may come from anywhere
//   /api/v1/bootstrap/*     — first-login claim before any auth exists
//   /ios-dot.mobileconfig   — Apple OTA fetch pulls from public internet
//   /dns-query              — public DoH endpoint, iOS profile clients
//   OPTIONS preflight       — browsers must always receive CORS reply
package api

import (
	"net/http"
	"strings"
)

// internalOnlyMiddleware enforces the source-IP allowlist on all
// panel API + SPA routes except the public-by-design exceptions
// listed above.
func (s *Server) internalOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Gate == nil || !s.Gate.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		// Always let CORS preflights through — the cors middleware
		// upstream already replied with the appropriate headers by
		// the time we run, but Go's ServeHTTP contract lets the
		// preflight terminate at the cors handler; this branch is
		// defensive for any preflight that reaches us.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if isInternalOnlyBypass(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		remote := remoteAddrFromRequest(r)
		if s.Gate.Allow(remote) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusForbidden, "internal_only_access",
			"panel is restricted to internal network clients")
	})
}

// isInternalOnlyBypass reports whether p is one of the four public-
// by-design paths listed in the package doc.
func isInternalOnlyBypass(p string) bool {
	switch {
	case p == "/api/v1/health":
		return true
	case p == "/ios-dot.mobileconfig":
		return true
	case p == "/dns-query":
		return true
	case strings.HasPrefix(p, "/api/v1/bootstrap"):
		return true
	}
	return false
}

// remoteAddrFromRequest returns an net.Addr-shaped view of r's peer
// suitable for Gate.Allow. r.RemoteAddr is the standard net/http
// "host:port" string; we hand it back inside a stringAddr so
// access.Gate's SplitHostPort fallback path can parse it.
func remoteAddrFromRequest(r *http.Request) stringAddr {
	return stringAddr(r.RemoteAddr)
}

// stringAddr adapts an http.Request.RemoteAddr string into the
// net.Addr interface Gate.Allow expects.
type stringAddr string

func (s stringAddr) Network() string { return "tcp" }
func (s stringAddr) String() string  { return string(s) }
