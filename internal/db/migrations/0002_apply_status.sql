-- +goose Up
-- +goose StatementBegin
-- Initial apply_status lifecycle: submitted -> confirmed | rolled_back.
-- Migration 0007 extends this CHECK with the terminal failed state.
-- SSE is the push channel; this table is the durable truth source for
-- clients that reconnect after a disconnect.
CREATE TABLE apply_status (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id  INTEGER NOT NULL,
    state        TEXT NOT NULL DEFAULT 'submitted'
                     CHECK(state IN ('submitted', 'confirmed', 'rolled_back')),
    reason       TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id) ON DELETE CASCADE
);
CREATE INDEX idx_apply_status_snapshot ON apply_status(snapshot_id);
CREATE INDEX idx_apply_status_state    ON apply_status(state);
-- +goose StatementEnd

-- Extend audit_log.action vocabulary with apply lifecycle values so that
-- the existing audit_log table can record apply-status transitions without
-- a schema change (action is free-form TEXT; this comment documents the
-- new values added by S1):
--   rules.apply.submitted   — Apply request accepted, systemd reload fired
--   rules.apply.confirmed   — Health check passed, config committed
--   rules.apply.rolled_back — Health check failed, previous config restored
-- No DDL change required; values are enforced at the application layer.

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS apply_status;
-- +goose StatementEnd
