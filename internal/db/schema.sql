-- Reference canonical schema — the authoritative source is
-- internal/db/migrations/. Use `goose sqlite3 ./5gpn.db up` to apply, or call
-- db.Migrate() from Go which embeds the migrations.

-- Managed by goose:
--   schema_migrations(version_id INTEGER PRIMARY KEY, is_applied INTEGER, tstamp DATETIME)

CREATE TABLE snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    config_hash TEXT NOT NULL UNIQUE,
    tarball_path TEXT NOT NULL,
    immutable INTEGER NOT NULL DEFAULT 1,
    note TEXT
);

CREATE TABLE audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT,
    before TEXT,
    after TEXT,
    result TEXT NOT NULL,
    ip TEXT
);

CREATE TABLE panel_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    bcrypt_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_login DATETIME
);

CREATE TABLE panel_sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES panel_users(id) ON DELETE CASCADE,
    jwt_id TEXT NOT NULL UNIQUE,
    issued_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    revoked_at DATETIME,
    ip TEXT,
    user_agent TEXT
);

CREATE TABLE bot_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id INTEGER NOT NULL UNIQUE,
    state TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE rule_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE RESTRICT,
    rules_yaml TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    active INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_rule_versions_only_one_active ON rule_versions(active) WHERE active = 1;

CREATE TABLE rule_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL,
    last_synced DATETIME,
    etag TEXT
);

CREATE TABLE metrics_snapshot (
    ts DATETIME PRIMARY KEY,
    cpu REAL,
    mem INTEGER,
    conns INTEGER,
    tx_bytes INTEGER,
    rx_bytes INTEGER
);

CREATE TABLE rule_test_fixtures (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domain TEXT NOT NULL UNIQUE,
    expected_exit TEXT NOT NULL,
    notes TEXT
);
