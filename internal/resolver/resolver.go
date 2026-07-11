package resolver

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/miekg/dns"
)

// Resolver answers a dns.Msg with a dns.Msg, using the pinned RuleTable
// snapshot at query start to decide block / direct / proxy, then either
// synthesizing a REFUSED/NXDOMAIN reply locally (block, AXFR/IXFR) or
// forwarding via Upstream (direct/proxy).
//
// The zero value is not usable; construct via NewResolver so the required
// Store and Upstream are always non-nil.
type Resolver struct {
	Store    *Store             // ruletable holder; Load() pinned per Resolve call
	Upstream *Upstream          // DoT forwarder
	Metrics  *Metrics           // optional; nil => noopMetrics
	Spoof    *SpoofPolicyHolder // optional; nil-safe — nil holder = spoofing disabled
}

// NewResolver wires the three collaborators. store and up must be non-nil;
// metrics may be nil (a discard counter set is used). Spoofing is
// initialised disabled; publish a policy via (*Resolver).SetSpoofPolicy.
func NewResolver(store *Store, up *Upstream, metrics *Metrics) *Resolver {
	if metrics == nil {
		metrics = discardMetrics()
	}
	return &Resolver{
		Store:    store,
		Upstream: up,
		Metrics:  metrics,
		Spoof:    &SpoofPolicyHolder{},
	}
}

// SetSpoofPolicy publishes p atomically. Passing nil disables spoofing.
// Safe to call concurrently with in-flight Resolve calls.
func (r *Resolver) SetSpoofPolicy(p *SpoofPolicy) {
	if r == nil || r.Spoof == nil {
		return
	}
	r.Spoof.Store(p)
}

// Resolve is the single entry point every listener (:53 UDP/TCP, :853 DoT,
// :443 DoH, DoQ, DoH3) funnels through. Ownership contract: caller owns
// req; Resolve returns a freshly allocated *dns.Msg that the caller sends
// on the wire.
//
// client is the query's origin IP as observed by the listener. Pass nil
// when unknown (in-process tests, unit-test callers) — spoof scoping
// treats nil as "not in any allow-CIDR", so scope=private_only fails
// closed. scope=all ignores client entirely.
//
// Behavior:
//   - AXFR/IXFR (or any query with no Question section that would race for
//     one) is refused before classification (RFC 5936, plan Missing #2).
//   - EDNS0 client-subnet is stripped from a defensive copy of req before
//     the upstream ever sees it (plan §4 Phase 1 R14 mitigation).
//   - EDNS0 UDP payload size is clamped to 1232 (DNS Flag Day 2020 / plan
//     R13 mitigation).
//   - The RuleTable is pinned via Store.Load() once per call; a Publish
//     that races with an in-flight Resolve does not change what this call
//     sees (plan Fork C invariant).
//   - When Spoof policy matches on a "proxy"-classified A/AAAA query,
//     the resolver synthesises the answer locally (pointing at the
//     gateway IP) instead of forwarding to the Proxy upstream — this is
//     what redirects the client's next TCP/UDP :443 attempt onto our
//     SNI/QUIC forwarders (plan §5, path B).
func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg, client net.IP) (*dns.Msg, error) {
	r.Metrics.IncQueries()

	if req == nil {
		return nil, errors.New("resolver: nil request")
	}

	if isZoneTransfer(req) {
		r.Metrics.IncRefusedAXFR()
		return refusedReply(req), nil
	}
	if len(req.Question) == 0 {
		return formatErrorReply(req), nil
	}

	tbl := r.Store.Load()
	qname := req.Question[0].Name
	action := classify(tbl, qname)

	switch action {
	case ActionBlock:
		r.Metrics.IncBlock()
		return nxDomainReply(req), nil
	case ActionDirect:
		r.Metrics.IncDirect()
		return r.forward(ctx, req, "direct")
	default: // ActionProxy
		// Spoof takes precedence over upstream forwarding for A/AAAA:
		// see doc comment. Non-A/AAAA (e.g. HTTPS, SVCB, MX) fall
		// through to the upstream forward path so browsers still get
		// authoritative CNAME/HTTPS answers — spoofing only the
		// address records is enough to redirect the flow onto our
		// gateway IP.
		if policy := r.Spoof.Load(); policy != nil {
			if ip, ok := policy.shouldSpoof(client, req.Question[0].Qtype); ok {
				r.Metrics.IncSpoof()
				return spoofReply(req, ip, policy.TTL), nil
			}
		}
		r.Metrics.IncProxy()
		return r.forward(ctx, req, "proxy")
	}
}

func (r *Resolver) forward(ctx context.Context, req *dns.Msg, category string) (*dns.Msg, error) {
	q := sanitizeForUpstream(req)
	resp, err := r.Upstream.Query(ctx, q, category)
	if err != nil {
		r.Metrics.IncUpstreamError()
		// Return a SERVFAIL to the client rather than dropping — clients
		// then know to fall back to a cached / secondary resolver
		// instead of hanging on their own timeout.
		return servFailReply(req), fmt.Errorf("resolver: forward %s: %w", category, err)
	}
	resp.Id = req.Id
	return resp, nil
}

// isZoneTransfer catches AXFR + IXFR, which the resolver refuses so the
// public DoT/DoH surface can't be used to enumerate our (empty) zone or
// confirm the resolver's identity to scanners (plan Missing #4).
func isZoneTransfer(req *dns.Msg) bool {
	for _, q := range req.Question {
		if q.Qtype == dns.TypeAXFR || q.Qtype == dns.TypeIXFR {
			return true
		}
	}
	return false
}

// sanitizeForUpstream returns a defensive copy with EDNS0 client-subnet
// stripped and UDP buffer size clamped to 1232. The original req is not
// mutated so the caller can still inspect what the client actually asked.
func sanitizeForUpstream(req *dns.Msg) *dns.Msg {
	q := req.Copy()
	opt := q.IsEdns0()
	if opt == nil {
		return q
	}
	// EDNS0 UDP payload size clamp — DNS Flag Day 2020 says 1232 is the
	// safe path-MTU-derived ceiling. Only clamp downward.
	if opt.UDPSize() > 1232 {
		opt.SetUDPSize(1232)
	}
	// Client-subnet strip: build a fresh slice, don't reuse opt.Option's
	// backing array — dns.Msg.Copy() shares the Option slice with the
	// caller, and an in-place trim would race with any goroutine still
	// reading the original request.
	kept := make([]dns.EDNS0, 0, len(opt.Option))
	for _, o := range opt.Option {
		if _, isECS := o.(*dns.EDNS0_SUBNET); isECS {
			continue
		}
		kept = append(kept, o)
	}
	opt.Option = kept
	return q
}

// refusedReply / nxDomainReply / servFailReply / formatErrorReply are the
// three canned client-facing responses we synthesize without ever touching
// the upstream. Kept as free functions so tests can assert exact rcodes
// without exercising the full Resolve path.

func refusedReply(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeRefused)
	return resp
}

func nxDomainReply(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeNameError)
	return resp
}

func servFailReply(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeServerFailure)
	return resp
}

func formatErrorReply(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeFormatError)
	return resp
}
