package frontdoor

import (
	"context"
	"fmt"

	"github.com/miekg/dns"
)

// ServeDNS is the single entry point every plain-:53 listener hands its
// inbound dns.Msg to. It wraps resolver.Resolve in a recover() so a
// scanner's malformed query, or a bug in the resolver itself, can never
// escape this goroutine and take the shared panel process down with it
// (plan §7.5 "shared-fate" mitigation) — miekg/dns's own dispatch loop
// carries no panic recovery of its own.
//
// Once the plain-listener supervisor has exhausted its restart budget (see
// Supervisor.Run / Frontdoor.enterDegraded), any subsequent direct ServeDNS
// call short-circuits to SERVFAIL. The failed sockets themselves have already
// been closed by serveAll.
func (fd *Frontdoor) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	defer func() {
		if rec := recover(); rec != nil {
			fd.logger.Error("frontdoor: recovered panic serving dns query",
				"event", "frontdoor.recover",
				"panic", fmt.Sprint(rec),
				"remote", w.RemoteAddr(),
			)
			_ = w.WriteMsg(servFailReply(r))
		}
	}()

	if fd.degraded.Load() {
		_ = w.WriteMsg(servFailReply(r))
		return
	}

	resp, err := fd.resolver.Resolve(context.Background(), r, remoteIP(w.RemoteAddr()))
	if err != nil {
		fd.logger.Warn("frontdoor: resolve error", "err", err, "remote", w.RemoteAddr())
	}
	if resp == nil {
		resp = servFailReply(r)
	}
	if werr := w.WriteMsg(resp); werr != nil {
		fd.logger.Warn("frontdoor: write response failed", "err", werr, "remote", w.RemoteAddr())
	}
}
