// Package db owns the SQLite lifecycle (open with sane pragmas + goose-managed
// migrations). All persistent state — panel users, snapshots, audit log,
// rule versions, bot sessions, metrics — lives in one 5gpn.db file.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Config controls Open.
type Config struct {
	Path string
}

// ErrNotImplemented is returned by M0 query stubs; removed in M1 as real
// implementations land.
var ErrNotImplemented = errors.New("db query not implemented")

// Open dials sqlite3 with WAL + foreign_keys + secure_delete enabled and
// returns a *sql.DB ready for Migrate.
func Open(cfg Config) (*sql.DB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("db: empty path")
	}
	dsn := cfg.Path + "?_journal_mode=WAL&_foreign_keys=on&_secure_delete=on&_busy_timeout=5000"
	handle, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", cfg.Path, err)
	}
	if err := handle.Ping(); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("db: ping %s: %w", cfg.Path, err)
	}
	return handle, nil
}

// Migrate applies embedded goose migrations up to head.
func Migrate(handle *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("db: set dialect: %w", err)
	}
	if err := goose.Up(handle, "migrations"); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}
