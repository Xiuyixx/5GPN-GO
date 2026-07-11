// Package rulesets owns the "native rule-provider" surface: named remote
// rulesets the panel fetches, caches, and expands into effective rules
// at apply time. Distinct from internal/rules — that package parses
// individual rule syntax; this package tracks the source-of-truth
// registration + cached content + last-sync metadata.
package rulesets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// ErrNotFound is returned when a lookup targets a name that is not in
// the rulesets table.
var ErrNotFound = errors.New("rulesets: not found")

// Kind values allowed in the kind column.
const (
	KindClash   = "clash"
	KindGFWList = "gfwlist"
)

// Ruleset is one row in the rulesets table. Content is included as raw
// bytes so callers can re-parse without hitting the upstream — the
// parser lives in internal/rules.Import(), which auto-detects Clash /
// gfwlist / base64-gfwlist so callers do not need to switch on Kind.
type Ruleset struct {
	Name         string
	SourceURL    string
	Kind         string
	Action       string
	Priority     int
	Enabled      bool
	ETag         string
	LastModified string
	LastSyncedAt *time.Time
	LastError    string
	RuleCount    int
	Content      []byte
	CreatedAt    time.Time
	CreatedBy    string
}

// Store wraps *sql.DB with the rulesets CRUD surface.
type Store struct {
	db *sql.DB
}

// New returns a Store bound to the given *sql.DB.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Upsert inserts or replaces a ruleset row. The panel wizard / import
// dialog calls this to register a new source. On conflict, name-keyed
// row is updated with the new url / kind / action / priority; content +
// sync metadata are preserved so a re-registration does not lose the
// last successful sync.
func (s *Store) Upsert(ctx context.Context, r Ruleset) error {
	if r.Name == "" {
		return errors.New("rulesets.Upsert: name required")
	}
	if r.SourceURL == "" {
		return errors.New("rulesets.Upsert: source_url required")
	}
	if r.Action == "" {
		return errors.New("rulesets.Upsert: action required")
	}
	if r.Priority == 0 {
		r.Priority = 500
	}
	created := r.CreatedAt.Unix()
	if r.CreatedAt.IsZero() {
		created = time.Now().Unix()
	}
	enabled := 1
	if !r.Enabled {
		enabled = 0
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO rulesets(
			name, source_url, kind, action, priority, enabled,
			etag, last_modified, last_synced_at, last_error, rule_count,
			content, created_at, created_by
		 ) VALUES(?, ?, ?, ?, ?, ?,  '', '', NULL, NULL, 0,  NULL, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
			source_url = excluded.source_url,
			kind = excluded.kind,
			action = excluded.action,
			priority = excluded.priority,
			enabled = excluded.enabled`,
		r.Name, r.SourceURL, r.Kind, r.Action, r.Priority, enabled,
		created, r.CreatedBy,
	)
	return err
}

// Get returns a single ruleset by name. ErrNotFound when absent.
func (s *Store) Get(ctx context.Context, name string) (*Ruleset, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT name, source_url, kind, action, priority, enabled,
		        etag, last_modified, last_synced_at, last_error, rule_count,
		        content, created_at, created_by
		 FROM rulesets WHERE name = ?`, name)
	return scanRow(row.Scan)
}

// List returns every ruleset, ordered by priority then name so the
// panel and the apply-time expander both see the same deterministic
// order.
func (s *Store) List(ctx context.Context) ([]Ruleset, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, source_url, kind, action, priority, enabled,
		        etag, last_modified, last_synced_at, last_error, rule_count,
		        content, created_at, created_by
		 FROM rulesets ORDER BY priority ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ruleset
	for rows.Next() {
		r, err := scanRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Delete removes the row; no-op if absent.
func (s *Store) Delete(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rulesets WHERE name = ?`, name)
	return err
}

// SetEnabled flips the enabled bit. Called by the panel's toggle UI.
func (s *Store) SetEnabled(ctx context.Context, name string, enabled bool) error {
	e := 0
	if enabled {
		e = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE rulesets SET enabled = ? WHERE name = ?`, e, name)
	return err
}

// UpdateContent writes the post-sync outcome for a single ruleset:
// fresh content bytes, rule_count, ETag / Last-Modified caches, and
// clears last_error. Called by the sync engine on a successful fetch.
func (s *Store) UpdateContent(ctx context.Context, name string, content []byte, ruleCount int, etag, lastModified string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE rulesets
		 SET content = ?, rule_count = ?, etag = ?, last_modified = ?,
		     last_synced_at = ?, last_error = NULL
		 WHERE name = ?`,
		content, ruleCount, etag, lastModified, time.Now().Unix(), name,
	)
	return err
}

// TouchSyncedAt bumps last_synced_at without touching content. Used
// when the upstream returned 304 Not Modified — we know it's still
// current, so record that we checked.
func (s *Store) TouchSyncedAt(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE rulesets SET last_synced_at = ?, last_error = NULL WHERE name = ?`,
		time.Now().Unix(), name)
	return err
}

// RecordError writes last_error without touching content. Used when the
// sync attempt failed so the operator sees the reason in the UI.
func (s *Store) RecordError(ctx context.Context, name, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE rulesets SET last_error = ?, last_synced_at = ? WHERE name = ?`,
		reason, time.Now().Unix(), name)
	return err
}

// Expand walks every enabled ruleset, parses the cached content, and
// returns a flat []rules.Rule ready to be merged with the panel's
// manually-authored rules at apply time. Rulesets with no cached
// content (never synced) contribute nothing. Rules within one ruleset
// keep the ruleset's Priority and get sequential ID suffixes so the
// snapshot stays deterministic.
func (s *Store) Expand(ctx context.Context) ([]rules.Rule, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []rules.Rule
	for _, rs := range list {
		if !rs.Enabled {
			continue
		}
		if len(rs.Content) == 0 {
			continue
		}
		expanded, _ := rules.Import(string(rs.Content), rules.ImportLegacyOptions{})
		for i, line := range expanded {
			parts := splitCSV3(line)
			if len(parts) < 2 {
				continue
			}
			kind := parts[0]
			var pattern, action string
			if kind == "FINAL" || kind == "MATCH" {
				kind = "MATCH"
				action = parts[1]
			} else {
				if len(parts) < 3 {
					continue
				}
				pattern = parts[1]
				action = parts[2]
			}
			low := lowerASCII(action)
			if low != "direct" && low != "block" {
				action = rs.Action
			}
			out = append(out, rules.Rule{
				ID:       fmt.Sprintf("%s-%d", rs.Name, i+1),
				Kind:     rules.Kind(kind),
				Pattern:  pattern,
				Action:   action,
				Priority: rs.Priority,
				Enabled:  true,
				GroupID:  rs.Name,
			})
		}
	}
	return out, nil
}

// scanRow reads one Ruleset row from the given Scan function shape.
// Kept out of the caller so the same struct-mapping code powers Get,
// List, and any future single-row query.
func scanRow(scan func(dest ...any) error) (*Ruleset, error) {
	var (
		r        Ruleset
		enabled  int
		syncedAt sql.NullInt64
		lastErr  sql.NullString
		content  []byte
		created  int64
	)
	err := scan(
		&r.Name, &r.SourceURL, &r.Kind, &r.Action, &r.Priority, &enabled,
		&r.ETag, &r.LastModified, &syncedAt, &lastErr, &r.RuleCount,
		&content, &created, &r.CreatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	if syncedAt.Valid {
		ts := time.Unix(syncedAt.Int64, 0)
		r.LastSyncedAt = &ts
	}
	if lastErr.Valid {
		r.LastError = lastErr.String
	}
	r.Content = content
	r.CreatedAt = time.Unix(created, 0)
	return &r, nil
}

// splitCSV3 splits at commas up to 3 parts. Kept local so this package
// stays free of a heavier CSV dep.
func splitCSV3(s string) []string {
	out := make([]string, 0, 3)
	cur := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] == ',' && len(out) < 2 {
			out = append(out, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, s[i])
	}
	out = append(out, string(cur))
	return out
}

func lowerASCII(s string) string {
	buf := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	return string(buf)
}
