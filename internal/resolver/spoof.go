package resolver

import (
	"net"
	"strings"
	"sync/atomic"

	"github.com/miekg/dns"
)

// SpoofScope enumerates whose queries we spoof to the gateway IP on a
// "proxy" classification. Public DoH is our normal reach, but if the
// operator wants to keep the panel usable to arbitrary DoT clients (who
// would then not benefit from — and probably not want — being tunnelled
// through our gateway), scopePrivateOnly lets them limit spoofing to a
// list of trusted CIDRs.
type SpoofScope string

const (
	// SpoofScopeAll spoofs every "proxy"-classified A/AAAA query
	// regardless of client IP. This matches the 5gpn-Go deployment
	// shape where iPhones on 5G reach a public DoH endpoint from
	// arbitrary NAT-egress IPs and are expected to be tunnelled.
	SpoofScopeAll SpoofScope = "all"

	// SpoofScopePrivateOnly spoofs only when the client IP falls
	// inside SpoofPolicy.AllowCIDR. Mirrors 5GPN-X's
	// privateClientRule = 172.22.0.0/16 dnsdist logic.
	SpoofScopePrivateOnly SpoofScope = "private_only"
)

// SpoofPolicy is the runtime-swappable configuration for DNS spoofing.
// The zero value is disabled; callers publish a fresh policy by calling
// (*Resolver).SetSpoofPolicy, which stores it atomically so an in-flight
// Resolve sees either the old or the new policy in full — never a
// half-updated blend.
type SpoofPolicy struct {
	// ServerIP4 is the gateway's public IPv4 that A answers are
	// rewritten to. Required to spoof A; if nil, A records fall
	// through to the upstream forward path unchanged.
	ServerIP4 net.IP

	// ServerIP6 is the optional IPv6 counterpart for AAAA. If nil,
	// AAAA records are left to the upstream forward path — same as
	// the pre-spoof behaviour, so we don't accidentally black-hole
	// v6 clients on a v4-only gateway.
	ServerIP6 net.IP

	// Scope selects which client population is spoofed. Empty string
	// is treated as SpoofScopeAll (safest default for the public-DoH
	// deployment shape).
	Scope SpoofScope

	// AllowCIDR is consulted only for SpoofScopePrivateOnly. A nil
	// or empty slice with scope=private_only disables spoofing
	// entirely (nothing matches), which is a safer default than
	// accidentally spoofing everyone.
	AllowCIDR []*net.IPNet

	// TTL is the DNS TTL emitted on synthesised A/AAAA answers.
	// Kept short so a policy flip (or IP change) propagates quickly
	// to caches. 60s matches the negative TTL floor used elsewhere.
	TTL uint32
}

// defaultSpoofTTL is the fallback TTL applied to synthesised answers
// when SpoofPolicy.TTL is zero.
const defaultSpoofTTL uint32 = 60

// shouldSpoof decides whether a given (client, qtype) pair falls under
// this policy. Returns the answer IP + true when the resolver should
// synthesise a reply instead of forwarding to the upstream.
func (p *SpoofPolicy) shouldSpoof(client net.IP, qtype uint16) (net.IP, bool) {
	if p == nil {
		return nil, false
	}

	var ip net.IP
	switch qtype {
	case dns.TypeA:
		ip = p.ServerIP4
	case dns.TypeAAAA:
		ip = p.ServerIP6
	default:
		return nil, false
	}
	if ip == nil {
		return nil, false
	}

	scope := p.Scope
	if scope == "" {
		scope = SpoofScopeAll
	}
	switch scope {
	case SpoofScopeAll:
		return ip, true
	case SpoofScopePrivateOnly:
		if client == nil {
			return nil, false
		}
		for _, cidr := range p.AllowCIDR {
			if cidr != nil && cidr.Contains(client) {
				return ip, true
			}
		}
		return nil, false
	}
	return nil, false
}

// spoofReply builds a NOERROR reply that answers req's first question
// with a single A/AAAA record pointing at ip. Any other RR types in
// req.Question are copied verbatim into the reply's Question section
// but produce no Answer entries — the resolver only spoofs the query
// types it can synthesise (A/AAAA).
func spoofReply(req *dns.Msg, ip net.IP, ttl uint32) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = false
	resp.RecursionAvailable = true

	if ttl == 0 {
		ttl = defaultSpoofTTL
	}
	if len(req.Question) == 0 {
		return resp
	}
	q := req.Question[0]
	name := q.Name
	hdr := dns.RR_Header{Name: name, Class: dns.ClassINET, Ttl: ttl}

	switch q.Qtype {
	case dns.TypeA:
		v4 := ip.To4()
		if v4 == nil {
			return resp
		}
		hdr.Rrtype = dns.TypeA
		resp.Answer = []dns.RR{&dns.A{Hdr: hdr, A: v4}}
	case dns.TypeAAAA:
		v6 := ip.To16()
		if v6 == nil {
			return resp
		}
		hdr.Rrtype = dns.TypeAAAA
		resp.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: v6}}
	}
	return resp
}

// SpoofPolicyHolder wraps an atomic pointer around *SpoofPolicy so a
// live Resolver can be reconfigured (settings flip, IP rediscovery)
// without tearing down every in-flight query.
//
// The zero value returns a nil policy (spoofing disabled), which is
// what tests and the Resolver constructor's default rely on.
type SpoofPolicyHolder struct {
	p atomic.Pointer[SpoofPolicy]
}

// Load returns the currently published policy, or nil if none.
func (h *SpoofPolicyHolder) Load() *SpoofPolicy {
	if h == nil {
		return nil
	}
	return h.p.Load()
}

// Store publishes p. Passing nil disables spoofing.
func (h *SpoofPolicyHolder) Store(p *SpoofPolicy) {
	if h == nil {
		return
	}
	h.p.Store(p)
}

// ParseCIDRs converts a slice of string CIDRs into net.IPNet pointers,
// skipping (rather than erroring on) unparseable entries. Callers that
// want strict parsing should use net.ParseCIDR directly; this helper
// is designed for settings-driven configuration where an operator's
// typo shouldn't take the whole resolver down.
func ParseCIDRs(in []string) []*net.IPNet {
	if len(in) == 0 {
		return nil
	}
	out := make([]*net.IPNet, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil || n == nil {
			continue
		}
		out = append(out, n)
	}
	return out
}
