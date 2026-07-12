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
CREATE INDEX idx_snapshots_created_at ON snapshots(created_at);

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
CREATE INDEX idx_audit_log_ts ON audit_log(ts);
CREATE INDEX idx_audit_log_actor ON audit_log(actor);

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
CREATE INDEX idx_panel_sessions_active ON panel_sessions(revoked_at, expires_at);

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
CREATE INDEX idx_rule_versions_created_at ON rule_versions(created_at);

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

CREATE TABLE apply_status (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'submitted'
        CHECK(state IN ('submitted', 'confirmed', 'rolled_back', 'failed')),
    reason TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_apply_status_snapshot ON apply_status(snapshot_id);
CREATE INDEX idx_apply_status_state ON apply_status(state);

CREATE TABLE exits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    exit_id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    protocol TEXT NOT NULL,
    uri TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_exits_only_one_active ON exits(active) WHERE active = 1;
CREATE INDEX idx_exits_created_at ON exits(created_at);
INSERT INTO exits(exit_id, name, protocol, uri, active)
VALUES('direct', 'direct', 'direct', 'direct://', 1);

CREATE TABLE panel_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_panel_settings_updated_at ON panel_settings(updated_at);

CREATE TABLE rulesets (
    name TEXT PRIMARY KEY,
    source_url TEXT NOT NULL,
    kind TEXT NOT NULL,
    action TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 500,
    enabled INTEGER NOT NULL DEFAULT 1,
    etag TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    last_synced_at INTEGER,
    last_error TEXT,
    rule_count INTEGER NOT NULL DEFAULT 0,
    content BLOB,
    created_at INTEGER NOT NULL,
    created_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_rulesets_enabled ON rulesets(enabled);

CREATE TABLE wg_peers (
    id INTEGER PRIMARY KEY,
    device_name TEXT NOT NULL,
    pubkey TEXT NOT NULL UNIQUE,
    private_key BLOB NOT NULL,
    address TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    allowed_ips_hash TEXT NOT NULL,
    profile_uuid TEXT NOT NULL,
    payload_uuid TEXT NOT NULL,
    mtu INTEGER NOT NULL DEFAULT 1280,
    dns_address TEXT NOT NULL DEFAULT '10.66.66.1',
    revoked INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK(state IN ('pending', 'active', 'failed', 'revoked')),
    created_at INTEGER NOT NULL,
    last_reconcile_at INTEGER
);
CREATE INDEX idx_wg_peers_active ON wg_peers(revoked, created_at);
CREATE INDEX idx_wg_peers_state ON wg_peers(state);
