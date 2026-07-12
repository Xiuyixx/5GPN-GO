package exit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"github.com/Xiuyixx/5GPN-Go/internal/core"
)

// ErrExitActive is returned by Store.Delete when the target row is the
// currently active exit. Callers must Switch to another exit first.
var ErrExitActive = errors.New("exit: cannot delete the active exit")

// ErrNoActive is returned by Store.Active when no row has active=1. Post
// migration seed this should never happen; the check exists to keep the
// invariant explicit for callers and tests.
var ErrNoActive = errors.New("exit: no active exit")

// ErrExitNotFound is returned when a lookup targets an exit_id that is
// not present in the DB.
var ErrExitNotFound = errors.New("exit: not found")

// ErrInvalidExitID is returned by Add when exitID fails the slug regex.
var ErrInvalidExitID = errors.New("exit: invalid exit id")

// directProtocol is the sentinel protocol whose URI xexit.Parse rejects
// on purpose (Parse is a strict scheme dispatcher). The store bypasses
// the parser for this protocol so the seed row is always addable.
const directProtocol = "direct"

// directURI is the sentinel URI matching the migration seed.
const directURI = "direct://"

var exitIDRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Exit is the panel-visible view of one exits row. ProxyConfig is the
// mihomo-shaped map produced by xexit.Parse (nil for the direct sentinel).
type Exit struct {
	ExitID      string
	Name        string
	Protocol    string
	URI         string
	Active      bool
	ProxyConfig map[string]any
}

// Store is the DB-backed exit registry. Concrete implementation is
// *SQLStore; tests may substitute an in-memory fake with the same
// method set.
type Store interface {
	List(ctx context.Context) ([]Exit, error)
	Add(ctx context.Context, exitID, uri string) (Exit, error)
	Delete(ctx context.Context, exitID string) error
	Switch(ctx context.Context, exitID string) error
	Active(ctx context.Context) (Exit, error)
	Records(ctx context.Context) ([]core.ExitRecord, error)
}

// SQLStore is the sqlite-backed Store implementation.
type SQLStore struct {
	db *sql.DB
}

// NewStore wraps a *sql.DB (post-Migrate) as a Store.
func NewStore(db *sql.DB) *SQLStore {
	return &SQLStore{db: db}
}

// List returns every exit in the DB, active row first, then created_at ASC.
// The active-first contract keeps render/mihomo.go correct without cfg
// surgery — see internal/config/render/mihomo.go.
func (s *SQLStore) List(ctx context.Context) ([]Exit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT exit_id, name, protocol, uri, active
		FROM exits
		ORDER BY active DESC, created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("exit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Exit
	for rows.Next() {
		var (
			e         Exit
			activeInt int
		)
		if err := rows.Scan(&e.ExitID, &e.Name, &e.Protocol, &e.URI, &activeInt); err != nil {
			return nil, fmt.Errorf("exit: list scan: %w", err)
		}
		e.Active = activeInt == 1
		e.ProxyConfig = parseURIOrNil(e.Protocol, e.URI)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("exit: list rows: %w", err)
	}
	return out, nil
}

// Add validates exitID and uri, then inserts a new inactive exit row.
// The direct sentinel (exitID=="direct" OR uri=="direct://") bypasses
// xexit.Parse; everything else must parse successfully.
func (s *SQLStore) Add(ctx context.Context, exitID, uri string) (Exit, error) {
	if !exitIDRE.MatchString(exitID) {
		return Exit{}, fmt.Errorf("%w: %q", ErrInvalidExitID, exitID)
	}

	protocol := ""
	var proxyCfg map[string]any
	if exitID == directProtocol || uri == directURI {
		protocol = directProtocol
	} else {
		proxy, err := Parse(uri)
		if err != nil {
			return Exit{}, fmt.Errorf("exit: parse uri: %w", err)
		}
		if t, ok := proxy["type"].(string); ok && t != "" {
			protocol = t
		} else {
			protocol = "unknown"
		}
		proxyCfg = proxy
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO exits(exit_id, name, protocol, uri, active) VALUES(?, ?, ?, ?, 0)`,
		exitID, exitID, protocol, uri,
	); err != nil {
		return Exit{}, fmt.Errorf("exit: insert: %w", err)
	}

	return Exit{
		ExitID:      exitID,
		Name:        exitID,
		Protocol:    protocol,
		URI:         uri,
		Active:      false,
		ProxyConfig: proxyCfg,
	}, nil
}

// Delete removes an exit row. Deleting the active row is rejected with
// ErrExitActive so callers must Switch first.
func (s *SQLStore) Delete(ctx context.Context, exitID string) error {
	var active int
	err := s.db.QueryRowContext(ctx,
		`SELECT active FROM exits WHERE exit_id = ?`, exitID,
	).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %q", ErrExitNotFound, exitID)
	}
	if err != nil {
		return fmt.Errorf("exit: delete lookup: %w", err)
	}
	if active == 1 {
		return ErrExitActive
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM exits WHERE exit_id = ?`, exitID)
	if err != nil {
		return fmt.Errorf("exit: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %q", ErrExitNotFound, exitID)
	}
	return nil
}

// Switch promotes exitID to active atomically: demote-all then promote-one
// inside a single txn. The DB partial unique index gives us an extra
// enforcement layer if the Go path ever slips.
func (s *SQLStore) Switch(ctx context.Context, exitID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("exit: switch begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM exits WHERE exit_id = ?`, exitID,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("exit: switch lookup: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: %q", ErrExitNotFound, exitID)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE exits SET active = 0 WHERE active = 1`); err != nil {
		return fmt.Errorf("exit: switch demote: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE exits SET active = 1 WHERE exit_id = ?`, exitID); err != nil {
		return fmt.Errorf("exit: switch promote: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("exit: switch commit: %w", err)
	}
	return nil
}

// Active returns the row where active=1 or ErrNoActive.
func (s *SQLStore) Active(ctx context.Context) (Exit, error) {
	var (
		e         Exit
		activeInt int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT exit_id, name, protocol, uri, active
		FROM exits WHERE active = 1`,
	).Scan(&e.ExitID, &e.Name, &e.Protocol, &e.URI, &activeInt)
	if errors.Is(err, sql.ErrNoRows) {
		return Exit{}, ErrNoActive
	}
	if err != nil {
		return Exit{}, fmt.Errorf("exit: active: %w", err)
	}
	e.Active = activeInt == 1
	e.ProxyConfig = parseURIOrNil(e.Protocol, e.URI)
	return e, nil
}

// Records returns the core.ExitRecord projection Assemble consumes.
// Active row is at index 0 — see mihomo.go for the reader-side contract.
func (s *SQLStore) Records(ctx context.Context) ([]core.ExitRecord, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.ExitRecord, 0, len(items))
	for _, e := range items {
		out = append(out, core.ExitRecord{
			ID:       e.ExitID,
			Protocol: e.Protocol,
			Config:   e.ProxyConfig,
		})
	}
	return out, nil
}

// parseURIOrNil re-parses the stored URI to reconstruct ProxyConfig on
// read. Failures return nil — Add is the authoritative validation gate,
// so a re-parse failure here means the DB was corrupted out-of-band.
func parseURIOrNil(protocol, uri string) map[string]any {
	if protocol == directProtocol || uri == directURI {
		return nil
	}
	proxy, err := Parse(uri)
	if err != nil {
		return nil
	}
	return proxy
}
