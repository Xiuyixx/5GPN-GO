// Package settings owns the panel-driven settings surface: a thin
// key-value store over the panel_settings SQLite table, plus JSON codecs
// for each known key.
//
// Design notes:
//
//   - The DB is the source of truth for anything the wizard or the
//     Settings page collects. The YAML Config in /etc/5gpn/config.yaml
//     stays authoritative for boot-time defaults; values present in the
//     DB layer OVER the YAML at read time (Layered.Merge).
//   - Every key uses JSON encoding so both scalars and structured values
//     (admin_chat_ids []int64, allow_cidr []string) fit the same schema.
//   - Access is synchronous SQL — the panel_settings table is tiny
//     (<50 rows) and a Get on a hot path costs a single indexed lookup.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Known panel_settings keys. New keys go here so the linter catches
// typos at the callsite instead of at runtime.
const (
	KeyServerDomain    = "server.domain"
	KeyServerPanelBind = "server.panel_bind"
	KeyServerPanelPort = "server.panel_port"
	KeyTLSACMEEnabled  = "tls.acme_enabled"
	KeyTLSACMEEmail    = "tls.acme_email"
	KeyTGBotToken      = "tgbot.token"
	KeyTGBotAdminChats = "tgbot.admin_chat_ids"
	KeyWAShimEnabled   = "washim.enabled"
	KeyWAShimListen    = "washim.listen"
	KeyWAShimPort      = "washim.port"
	KeyWAShimBackend   = "washim.backend"
	KeyWAShimWAHost    = "washim.wa_host"
	KeyWAShimAllowCIDR = "washim.allow_cidr"
	KeyWizardComplete  = "wizard.complete"

	// iOS preflight + profile-gating keys (plan §4 Phase 8). The mobileconfig
	// endpoint stays 503 until KeyFrontdoorIOSProfileEnabled is true, and
	// that flag can only flip to true after a passing preflight — see
	// internal/api/preflight.go.
	KeyFrontdoorIOSProfileEnabled  = "frontdoor.ios_profile_enabled"
	KeyFrontdoorFallbackDoT        = "frontdoor.ios_fallback_dot"
	KeyFrontdoorPreflightLastAt    = "frontdoor.preflight_last_at"
	KeyFrontdoorPreflightLastError = "frontdoor.preflight_last_error"

	// DoQ / DoH3 opt-in flags (plan §4 Phase 9). Both default false —
	// Frontdoor.Config.DoQEnabled/DoH3Enabled ship gated behind an
	// explicit operator opt-in, mirroring the iOS profile-gating pattern
	// above.
	KeyFrontdoorDoQEnabled  = "frontdoor.doq_enabled"
	KeyFrontdoorDoH3Enabled = "frontdoor.doh3_enabled"

	// Path-B transparent-forwarder controls (5gpn-Go v0.4.x).
	//
	// KeyFrontdoorSpoofEnabled turns on resolver-side DNS spoofing:
	// when a "proxy" classification fires, A/AAAA answers are
	// synthesised to point at the gateway's own IP instead of the
	// real origin, so the client's next TCP/UDP :443 attempt lands
	// on our forwarders.
	//
	// KeyFrontdoorSNIForwardEnabled starts the TCP :443 SNI-split
	// forwarder. When on, the panel's own HTTPS listener is bumped
	// from :443 to KeyFrontdoorPanelBackendTCP (default 127.0.0.1:8444)
	// so the forwarder owns the public :443 socket exclusively.
	//
	// KeyFrontdoorQUICForwardEnabled starts the UDP :443 QUIC SNI
	// forwarder. DoH3 (when also enabled) is bumped to
	// KeyFrontdoorPanelBackendUDP (default 127.0.0.1:8445).
	//
	// KeyFrontdoorSpoofServerIP overrides the auto-discovered gateway
	// IP embedded in spoofed answers. Empty string means "use egress-
	// interface autodetection at daemon boot".
	KeyFrontdoorSpoofEnabled       = "frontdoor.spoof_enabled"
	KeyFrontdoorSpoofScope         = "frontdoor.spoof_scope"
	KeyFrontdoorSpoofServerIP      = "frontdoor.spoof_server_ip"
	KeyFrontdoorSpoofAllowCIDR     = "frontdoor.spoof_allow_cidr"
	KeyFrontdoorSNIForwardEnabled  = "frontdoor.sni_forward_enabled"
	KeyFrontdoorQUICForwardEnabled = "frontdoor.quic_forward_enabled"
	KeyFrontdoorPanelBackendTCP    = "frontdoor.panel_backend_tcp"
	KeyFrontdoorPanelBackendUDP    = "frontdoor.panel_backend_udp"

	// Internal-only access gate. When enabled, panel + mtproxy +
	// sniforward + quicforward accept traffic only from clients whose
	// source IP falls inside KeyFrontdoorInternalCIDRs. This mirrors
	// the operator's 5G APN slice business rule: paying customers all
	// come in via 联通 5G APN with a private RFC1918 assignment
	// (172.22.0.0/16 or similar), so any request from a public IP is
	// prima facie non-customer scanner traffic and can be dropped at
	// accept time. Default off; loopback is always allowed regardless
	// of the CIDR list so local health probes never lock themselves
	// out. See internal/access/gate.go for the runtime.
	KeyFrontdoorInternalOnlyEnabled = "frontdoor.internal_only_enabled"
	KeyFrontdoorInternalCIDRs       = "frontdoor.internal_cidrs"

	// MTProto Proxy — the panel used to store enabled/listen/secret
	// keys here for an in-tree obfuscated2 relay under internal/proxy/
	// mtproxy. The panel now drives an externally-installed
	// 9seconds/mtg systemd service via internal/mtgctl, so listen +
	// secret live in the unit file (/etc/systemd/system/mtg.service)
	// and are no longer stored in panel_settings. Existing DB rows
	// under mtproxy.* are left untouched on upgrade — they are simply
	// ignored by the new controller.

	// v0.4.0 WG plane — kernel WireGuard split-tunnel settings.
	//
	// The enabled flag is the blue-green feature flag: the WG plane
	// ships fully inert (wg0 never brought up, no mobileconfig payload
	// emitted) until the operator flips it on. Rollback is just
	// flipping it back off.
	//
	// The listen-port / server-address / DNS-address keys configure
	// the wg0 interface itself — the UDP listen port, the server's
	// tunnel-side CIDR, and the DNS address pushed to peers.
	//
	// The public-key setting is populated on first bringup once the
	// server keypair is generated/unsealed; it is not user-settable.
	//
	// The last-reconcile-at / allowed-ips-hash settings cache the
	// reconciler's last-run timestamp and the SHA-256 of the
	// last-derived AllowedIPs set, so an unchanged RuleTable is a
	// cheap no-op (Phase 6).
	//
	// The allowedips-cap setting bounds how many CIDRs the derivation
	// will emit before aggregating /24s into /16 supernets (Phase 4).
	//
	// The fwmark setting is the packet-mark value used by the
	// mangle_fwd nftables chain to steer wg0-sourced forward traffic
	// into routing table 100 (Phase 8); exposed as a setting so an
	// operator can override it if 0x100 collides with another daemon.
	KeyFrontdoorWGEnabled         = "frontdoor.wg.enabled"           // default false
	KeyFrontdoorWGListenPort      = "frontdoor.wg.listen_port"       // default 51820
	KeyFrontdoorWGServerAddress   = "frontdoor.wg.server_address"    // default 10.66.66.1/24
	KeyFrontdoorWGDNSAddress      = "frontdoor.wg.dns_address"       // default 10.66.66.1
	KeyFrontdoorWGPublicKey       = "frontdoor.wg.public_key"        // populated on first bringup
	KeyFrontdoorWGLastReconcileAt = "frontdoor.wg.last_reconcile_at"
	KeyFrontdoorWGAllowedIPsHash  = "frontdoor.wg.allowed_ips_hash"
	KeyFrontdoorWGAllowedIPsCap   = "frontdoor.wg.allowedips_cap" // default 8000
	KeyFrontdoorWGFwmark          = "frontdoor.wg.fwmark"         // default 0x100
)

// ErrNotFound is returned by Get when the key is absent.
var ErrNotFound = errors.New("settings: key not found")

// Store wraps *sql.DB with the panel_settings CRUD surface.
type Store struct {
	db *sql.DB
}

// New returns a Store bound to the given *sql.DB. The caller retains
// ownership of the handle.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Get returns the raw JSON-encoded value for key. ErrNotFound if absent.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM panel_settings WHERE key = ?`, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("settings.Get %q: %w", key, err)
	}
	return value, nil
}

// Set writes the raw JSON-encoded value for key. updatedBy is the
// requesting panel user (or "system" for internal writes) — captured in
// the row for audit visibility.
func (s *Store) Set(ctx context.Context, key, value, updatedBy string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO panel_settings(key, value, updated_at, updated_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE
		   SET value = excluded.value,
		       updated_at = excluded.updated_at,
		       updated_by = excluded.updated_by`,
		key, value, now, updatedBy,
	)
	if err != nil {
		return fmt.Errorf("settings.Set %q: %w", key, err)
	}
	return nil
}

// Delete removes the row; no-op if absent.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM panel_settings WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("settings.Delete %q: %w", key, err)
	}
	return nil
}

// Snapshot returns every key currently in the table, as a flat map. Used
// by boot-time overlay + the settings GET endpoint.
func (s *Store) Snapshot(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM panel_settings`)
	if err != nil {
		return nil, fmt.Errorf("settings.Snapshot: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// GetString reads a JSON-encoded string. Returns "" + nil when the key
// is missing (callers usually treat missing == default).
func (s *Store) GetString(ctx context.Context, key string) (string, error) {
	raw, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("settings.GetString %q: decode: %w", key, err)
	}
	return out, nil
}

// SetString encodes value as JSON and writes it.
func (s *Store) SetString(ctx context.Context, key, value, updatedBy string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.Set(ctx, key, string(raw), updatedBy)
}

// GetBool reads a JSON-encoded bool. Returns false + nil when absent.
func (s *Store) GetBool(ctx context.Context, key string) (bool, error) {
	raw, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var out bool
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return false, fmt.Errorf("settings.GetBool %q: decode: %w", key, err)
	}
	return out, nil
}

// SetBool encodes value as JSON and writes it.
func (s *Store) SetBool(ctx context.Context, key string, value bool, updatedBy string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.Set(ctx, key, string(raw), updatedBy)
}

// GetInt reads a JSON-encoded int. Returns 0 + nil when absent.
func (s *Store) GetInt(ctx context.Context, key string) (int, error) {
	raw, err := s.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var out int
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return 0, fmt.Errorf("settings.GetInt %q: decode: %w", key, err)
	}
	return out, nil
}

// SetInt encodes value as JSON and writes it.
func (s *Store) SetInt(ctx context.Context, key string, value int, updatedBy string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.Set(ctx, key, string(raw), updatedBy)
}

// GetJSON reads and decodes a JSON value into out. Returns ErrNotFound
// when the key is missing so callers can distinguish "empty struct" from
// "never set".
func (s *Store) GetJSON(ctx context.Context, key string, out any) error {
	raw, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("settings.GetJSON %q: decode: %w", key, err)
	}
	return nil
}

// SetJSON encodes value as JSON and writes it.
func (s *Store) SetJSON(ctx context.Context, key string, value any, updatedBy string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("settings.SetJSON %q: encode: %w", key, err)
	}
	return s.Set(ctx, key, string(raw), updatedBy)
}
