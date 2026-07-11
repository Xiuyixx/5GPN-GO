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
