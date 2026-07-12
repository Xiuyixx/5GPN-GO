// Package db owns the SQLite lifecycle (open with sane pragmas + goose-managed
// migrations). All persistent state — panel users, snapshots, audit log,
// rule versions, bot sessions, metrics — lives in one 5gpn.db file.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Config controls Open.
type Config struct {
	Path string
}

// Open dials sqlite3 with WAL + foreign_keys + secure_delete enabled and
// returns a *sql.DB ready for Migrate.
func Open(cfg Config) (*sql.DB, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("db: empty path")
	}
	// Pre-create the main database with a private mode. SQLite derives the
	// WAL/SHM sidecar mode from the database file on supported platforms.
	// Existing databases are tightened as part of every open.
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("db: create %s: %w", cfg.Path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("db: chmod %s: %w", cfg.Path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("db: close %s: %w", cfg.Path, err)
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
	for _, path := range []string{cfg.Path, cfg.Path + "-wal", cfg.Path + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
			_ = handle.Close()
			return nil, fmt.Errorf("db: chmod %s: %w", path, err)
		}
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
