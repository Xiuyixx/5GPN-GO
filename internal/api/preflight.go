// iOS DoT preflight — the hard gate in front of the mobileconfig endpoint
// (plan §4 Phase 8). The v0.2.9 regression let an operator flip on the
// iOS profile (or a user re-download an already-broken one) before the
// panel's own DoT listener was actually answering queries, which sent an
// iPhone's *entire* DNS resolution to a dead server: total connectivity
// loss until the profile was manually removed. The fix is procedural, not
// just cosmetic: enabling frontdoor.ios_profile_enabled requires a
// preflight that proves the local :853 DoT listener completes a TLS
// handshake AND answers a real query, all within 30s. Disabling never
// needs a preflight — turning DNS off can't make a broken resolver worse.
package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/miekg/dns"

	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// defaultIOSPreflightDoTAddr is the loopback DoT listener the preflight
// dials. It is a package var (not a const) so tests can point it at a
// fake listener's ephemeral port instead of the privileged :853 port.
var defaultIOSPreflightDoTAddr = "127.0.0.1:853"

// iosPreflightTimeout bounds the whole preflight (handshake + sample
// query) at 30s per plan §4 Phase 8 / AC-I2. Package var so tests can
// shorten it instead of waiting out a real 30s ceiling.
var iosPreflightTimeout = 30 * time.Second

// iosPreflightSampleQName is the query preflight sends once the TLS
// handshake completes — "A dns.google" per plan §4 Phase 8.
const iosPreflightSampleQName = "dns.google."

// defaultIOSFallbackDoT is used when the operator has never set
// settings.KeyFrontdoorFallbackDoT — Cloudflare's 1.1.1.1 resolver, whose
// certificate carries "1.1.1.1" as an IP SAN so DNSProtocol=TLS +
// ServerName="1.1.1.1" validates correctly.
const defaultIOSFallbackDoT = "1.1.1.1"

// preflightResult captures the fine-grained outcome of the iOS DoT
// preflight so callers (and the persisted settings) can distinguish "TLS
// never connected" from "TLS connected but the DNS query never came
// back" — both matter for diagnosing a broken profile toggle.
type preflightResult struct {
	OK           bool      `json:"ok"`
	DotHandshake bool      `json:"dot_handshake"`
	SampleQuery  bool      `json:"sample_query"`
	Error        string    `json:"error,omitempty"`
	CheckedAt    time.Time `json:"checked_at"`
}

// runIOSPreflight dials dotAddr (defaulting to defaultIOSPreflightDoTAddr)
// over TLS with ALPN "dot", then reuses the connection to send a framed
// "A dns.google" query and waits for an answer. InsecureSkipVerify is
// deliberate: this is a loopback dial against our own DoT listener, not a
// public peer, so there is no PKI to validate against. Both the handshake
// and the sample query must succeed within iosPreflightTimeout for
// OK=true.
func runIOSPreflight(ctx context.Context, dotAddr string) preflightResult {
	if dotAddr == "" {
		dotAddr = defaultIOSPreflightDoTAddr
	}

	res := preflightResult{CheckedAt: time.Now()}

	ctx, cancel := context.WithTimeout(ctx, iosPreflightTimeout)
	defer cancel()

	dialer := &tls.Dialer{
		Config: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // loopback dial to our own DoT listener, not a public peer
			NextProtos:         []string{"dot"},
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", dotAddr)
	if err != nil {
		res.Error = fmt.Sprintf("dot handshake: %v", err)
		return res
	}
	defer conn.Close()
	res.DotHandshake = true

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	co := &dns.Conn{Conn: conn}
	query := new(dns.Msg)
	query.SetQuestion(iosPreflightSampleQName, dns.TypeA)
	if err := co.WriteMsg(query); err != nil {
		res.Error = fmt.Sprintf("sample query write: %v", err)
		return res
	}
	resp, err := co.ReadMsg()
	if err != nil {
		res.Error = fmt.Sprintf("sample query read: %v", err)
		return res
	}
	if len(resp.Answer) == 0 {
		res.Error = fmt.Sprintf("sample query: no answer (rcode=%s)", dns.RcodeToString[resp.Rcode])
		return res
	}
	res.SampleQuery = true
	res.OK = true
	return res
}

// persistPreflightResult records the outcome in panel_settings so the
// panel and handleIOSMobileconfig's neighbors can read "last known good"
// state without re-running the check. CheckedAt (KeyFrontdoorPreflightLastAt)
// is only advanced on success, so it always reflects the last PASS, not
// the last attempt. The error string is always written (cleared to "" on
// success) so a later successful retry visibly clears a prior failure.
func (s *Server) persistPreflightResult(ctx context.Context, res preflightResult) {
	const actor = "system"
	if res.OK {
		_ = s.Settings.SetJSON(ctx, settings.KeyFrontdoorPreflightLastAt, res.CheckedAt.Unix(), actor)
	}
	_ = s.Settings.SetString(ctx, settings.KeyFrontdoorPreflightLastError, res.Error, actor)
}

// handleIOSPreflight runs the DoT preflight check on demand (e.g. a "Run
// Preflight" button in the panel) and persists the result. POST
// /api/v1/settings/ios/preflight.
func (s *Server) handleIOSPreflight(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable",
			"settings store not wired at daemon boot")
		return
	}
	res := runIOSPreflight(r.Context(), defaultIOSPreflightDoTAddr)
	s.persistPreflightResult(r.Context(), res)
	writeJSON(w, http.StatusOK, res)
}

// iosProfileToggleRequest is the body for POST /api/v1/settings/ios/profile-enabled.
type iosProfileToggleRequest struct {
	Enabled bool `json:"enabled"`
}

// handleIOSProfileToggle flips settings.KeyFrontdoorIOSProfileEnabled.
// Enabling runs the preflight FIRST and refuses (400) on failure, leaving
// the flag untouched — this is the hard gate described at the top of this
// file. Disabling never runs a preflight and always succeeds.
func (s *Server) handleIOSProfileToggle(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable",
			"settings store not wired at daemon boot")
		return
	}
	var req iosProfileToggleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	actor := actorFromCtx(r)
	ctx := r.Context()

	if !req.Enabled {
		if err := s.Settings.SetBool(ctx, settings.KeyFrontdoorIOSProfileEnabled, false, actor); err != nil {
			writeError(w, http.StatusInternalServerError, "write_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}

	res := runIOSPreflight(ctx, defaultIOSPreflightDoTAddr)
	s.persistPreflightResult(ctx, res)
	if !res.OK {
		reason := res.Error
		if reason == "" {
			reason = "preflight failed"
		}
		writeError(w, http.StatusBadRequest, "preflight_failed", reason)
		return
	}
	if err := s.Settings.SetBool(ctx, settings.KeyFrontdoorIOSProfileEnabled, true, actor); err != nil {
		writeError(w, http.StatusInternalServerError, "write_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "preflight": res})
}
