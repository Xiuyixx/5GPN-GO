-- +goose Up
-- +goose StatementBegin
-- exits stores the DB-backed truth for panel-managed exit endpoints.
-- One row per exit; exactly one row has active=1 at any given time
-- (enforced by the partial unique index below and by exit.Store txns).
-- The seed row 'direct' is inserted here so a fresh install always has
-- at least one selectable exit; xexit.Parse does NOT parse 'direct://'
-- so the exit.Store bypasses the parser for the sentinel protocol.
CREATE TABLE exits (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    exit_id     TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL,
    protocol    TEXT    NOT NULL,
    uri         TEXT    NOT NULL,
    active      INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_exits_only_one_active ON exits(active) WHERE active = 1;
CREATE INDEX idx_exits_created_at ON exits(created_at);
INSERT INTO exits(exit_id, name, protocol, uri, active)
VALUES('direct', 'direct', 'direct', 'direct://', 1);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS exits;
-- +goose StatementEnd
