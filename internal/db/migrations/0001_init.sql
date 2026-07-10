-- +goose Up
-- +goose StatementBegin
CREATE TABLE snapshots (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    config_hash   TEXT NOT NULL UNIQUE,
    tarball_path  TEXT NOT NULL,
    immutable     INTEGER NOT NULL DEFAULT 1,
    note          TEXT
);
CREATE INDEX idx_snapshots_created_at ON snapshots(created_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE audit_log (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ts       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor    TEXT NOT NULL,
    action   TEXT NOT NULL,
    target   TEXT,
    before   TEXT,
    after    TEXT,
    result   TEXT NOT NULL,
    ip       TEXT
);
CREATE INDEX idx_audit_log_ts ON audit_log(ts);
CREATE INDEX idx_audit_log_actor ON audit_log(actor);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE panel_users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    username     TEXT NOT NULL UNIQUE,
    bcrypt_hash  TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_login   DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE panel_sessions (
    id          TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL,
    jwt_id      TEXT NOT NULL UNIQUE,
    issued_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  DATETIME NOT NULL,
    revoked_at  DATETIME,
    ip          TEXT,
    user_agent  TEXT,
    FOREIGN KEY (user_id) REFERENCES panel_users(id) ON DELETE CASCADE
);
CREATE INDEX idx_panel_sessions_active ON panel_sessions(revoked_at, expires_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE bot_sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id     INTEGER NOT NULL,
    state       TEXT NOT NULL,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(chat_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE rule_versions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id   INTEGER NOT NULL,
    rules_yaml    TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    active        INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX idx_rule_versions_only_one_active ON rule_versions(active) WHERE active = 1;
CREATE INDEX idx_rule_versions_created_at ON rule_versions(created_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE rule_sources (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    url           TEXT NOT NULL UNIQUE,
    kind          TEXT NOT NULL,
    last_synced   DATETIME,
    etag          TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE metrics_snapshot (
    ts         DATETIME PRIMARY KEY,
    cpu        REAL,
    mem        INTEGER,
    conns      INTEGER,
    tx_bytes   INTEGER,
    rx_bytes   INTEGER
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE rule_test_fixtures (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    domain         TEXT NOT NULL UNIQUE,
    expected_exit  TEXT NOT NULL,
    notes          TEXT
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS rule_test_fixtures;
DROP TABLE IF EXISTS metrics_snapshot;
DROP TABLE IF EXISTS rule_sources;
DROP TABLE IF EXISTS rule_versions;
DROP TABLE IF EXISTS bot_sessions;
DROP TABLE IF EXISTS panel_sessions;
DROP TABLE IF EXISTS panel_users;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS snapshots;
-- +goose StatementEnd
