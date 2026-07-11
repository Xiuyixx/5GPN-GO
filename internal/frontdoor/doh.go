// Package frontdoor implements the DNS plane's client-facing transports
// (:53 UDP/TCP, :853 DoT, :443 DoH, DoQ). This file implements RFC 8484
// DNS-over-HTTPS as a plain http.Handler; DoH does not own a listener —
// the panel mounts Handler() at /dns-query on its existing chi router /
// :443 bind (plan §4 Phase 4).
package frontdoor

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
)

// dnsMessageContentType is the RFC 8484 §6 media type for both request
// (POST) bodies and response bodies.
const dnsMessageContentType = "application/dns-message"

// maxDoHMessageBytes bounds both the POST body and the decoded GET ?dns=
// parameter. DNS wire messages are framed elsewhere in this codebase with
// a 2-byte length prefix (see resolver/upstream.go), so 65535 is the
// natural ceiling; anything larger is refused rather than allocated.
const maxDoHMessageBytes = 65535

// errUnsupportedMediaType signals a Content-Type mismatch on POST; the
// handler maps it to HTTP 415.
var errUnsupportedMediaType = errors.New("frontdoor: unsupported media type")

// DoH implements RFC 8484 DNS-over-HTTPS on top of an existing
// *resolver.Resolver. It owns no listener or socket of its own.
type DoH struct {
	resolver *resolver.Resolver
	logger   *slog.Logger
}

// NewDoH wires a DoH handler around an existing resolver. logger may be
// nil, in which case slog.Default() is used.
func NewDoH(r *resolver.Resolver, logger *slog.Logger) *DoH {
	if logger == nil {
		logger = slog.Default()
	}
	return &DoH{resolver: r, logger: logger}
}

// Handler returns an http.Handler implementing RFC 8484:
//
//	POST application/dns-message -> body is the raw DNS wire message
//	GET  ?dns=<base64url>        -> query param is the base64url wire message
func (d *DoH) Handler() http.Handler {
	return http.HandlerFunc(d.serveHTTP)
}

func (d *DoH) serveHTTP(w http.ResponseWriter, req *http.Request) {
	// Belt-and-suspenders: a malformed query must never take the shared
	// panel process down with it (plan §7.5 shared-fate consequence).
	// miekg/dns's Unpack is designed to error rather than panic on
	// garbage bytes, but this handler has no listener of its own to rely
	// on Phase 2's recover() middleware, so it carries its own.
	defer func() {
		if rec := recover(); rec != nil {
			d.logger.Error("doh: panic recovered", "panic", rec)
			http.Error(w, "bad request: malformed dns message", http.StatusBadRequest)
		}
	}()

	var wire []byte
	var err error
	switch req.Method {
	case http.MethodPost:
		wire, err = readPostWire(req)
	case http.MethodGet:
		wire, err = readGetWire(req)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		if errors.Is(err, errUnsupportedMediaType) {
			http.Error(w, "unsupported media type: expected "+dnsMessageContentType, http.StatusUnsupportedMediaType)
			return
		}
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	in := new(dns.Msg)
	if err := in.Unpack(wire); err != nil {
		http.Error(w, "bad request: malformed dns message", http.StatusBadRequest)
		return
	}

	// Defence-in-depth: refuse AXFR/IXFR at the DoH layer too, redundant
	// with the resolver's own refusal (plan §4 Phase 4 explicit request).
	if isZoneTransfer(in) {
		writeDNSMessage(w, refusedReply(in))
		return
	}

	out, resolveErr := d.resolver.Resolve(req.Context(), in)
	if resolveErr != nil {
		d.logger.Warn("doh: resolve error", "error", resolveErr, "qname", qnameOf(in))
	}
	if out == nil {
		// Resolver contract always pairs an error with a non-nil SERVFAIL
		// reply, but guard anyway rather than pack a nil message.
		out = servFailReply(in)
	}
	writeDNSMessage(w, out)
}

// readPostWire validates the Content-Type header and reads the raw DNS
// wire message from the request body, capped at maxDoHMessageBytes.
func readPostWire(req *http.Request) ([]byte, error) {
	mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mt != dnsMessageContentType {
		return nil, errUnsupportedMediaType
	}

	data, err := io.ReadAll(io.LimitReader(req.Body, maxDoHMessageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("empty body")
	}
	if len(data) > maxDoHMessageBytes {
		return nil, errors.New("body too large")
	}
	return data, nil
}

// readGetWire decodes the base64url "dns" query parameter (RFC 8484 §4.1).
func readGetWire(req *http.Request) ([]byte, error) {
	q := req.URL.Query().Get("dns")
	if q == "" {
		return nil, errors.New("missing dns query parameter")
	}
	if len(q) > maxDoHMessageBytes*2 {
		// base64 expands size ~4/3x; reject early before decoding.
		return nil, errors.New("dns parameter too large")
	}

	data, decErr := base64.RawURLEncoding.DecodeString(q)
	if decErr != nil {
		// Some clients send padded base64url; retry before failing.
		data, decErr = base64.URLEncoding.DecodeString(q)
		if decErr != nil {
			return nil, fmt.Errorf("invalid base64url dns parameter: %w", decErr)
		}
	}
	if len(data) == 0 {
		return nil, errors.New("empty dns parameter")
	}
	if len(data) > maxDoHMessageBytes {
		return nil, errors.New("dns parameter too large")
	}
	return data, nil
}

// writeDNSMessage packs msg and writes it with the Cache-Control policy
// from plan §4 Phase 4 (Round 3 refinements):
//   - positive (NOERROR with answers): max-age = smallest RR TTL in Answer.
//   - NXDOMAIN / NODATA: fixed 60s floor (see cacheControlFor doc).
//   - other error rcodes (SERVFAIL, REFUSED, FORMERR, ...): no-store.
func writeDNSMessage(w http.ResponseWriter, msg *dns.Msg) {
	packed, err := msg.Pack()
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "internal error packing response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", dnsMessageContentType)
	w.Header().Set("Cache-Control", cacheControlFor(msg))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(packed)
}

// cacheControlFor implements RFC 8484 §5.1's Cache-Control policy.
func cacheControlFor(msg *dns.Msg) string {
	switch msg.Rcode {
	case dns.RcodeSuccess:
		if len(msg.Answer) == 0 {
			// NODATA (NOERROR, empty Answer): RFC 2308 says negative TTL
			// should come from the SOA MINIMUM field. This resolver does
			// not synthesize a SOA authority record today.
			// TODO(RFC 2308 §5 follow-up): source max-age from a real SOA
			// MINIMUM lookup once the resolver returns SOA in Ns; until
			// then fall back to a conservative fixed floor.
			return "max-age=60"
		}
		return fmt.Sprintf("max-age=%d", minAnswerTTL(msg.Answer))
	case dns.RcodeNameError:
		// NXDOMAIN: same RFC 2308 §5 SOA MINIMUM caveat as NODATA above —
		// no SOA authority record is synthesized, so fall back to the 60s
		// floor rather than caching indefinitely or not at all.
		// TODO(RFC 2308 §5 follow-up): source max-age from a real SOA
		// MINIMUM lookup; tracked as a v0.4.x follow-up.
		return "max-age=60"
	default:
		// SERVFAIL, REFUSED, FORMERR, NOTIMP, etc. are not safe to cache.
		return "no-store"
	}
}

// minAnswerTTL returns the smallest RR TTL across the Answer section,
// floored to 0 (uint32 TTLs cannot be negative, so no explicit floor is
// needed beyond returning the value as-is).
func minAnswerTTL(rrs []dns.RR) uint32 {
	min := rrs[0].Header().Ttl
	for _, rr := range rrs[1:] {
		if ttl := rr.Header().Ttl; ttl < min {
			min = ttl
		}
	}
	return min
}

// isZoneTransfer / refusedReply / servFailReply mirror the resolver
// package's unexported helpers of the same shape. They're duplicated
// (rather than exported from resolver) so the DoH layer's AXFR/IXFR
// refusal is a genuine second line of defence, not a shared call path
// that a single bug could remove from both layers at once.
func isZoneTransfer(msg *dns.Msg) bool {
	for _, q := range msg.Question {
		if q.Qtype == dns.TypeAXFR || q.Qtype == dns.TypeIXFR {
			return true
		}
	}
	return false
}

func refusedReply(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeRefused)
	return resp
}

func servFailReply(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, dns.RcodeServerFailure)
	return resp
}

func qnameOf(msg *dns.Msg) string {
	if len(msg.Question) == 0 {
		return ""
	}
	return msg.Question[0].Name
}
