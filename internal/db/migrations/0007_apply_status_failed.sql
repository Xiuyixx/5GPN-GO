-- +goose Up
-- +goose StatementBegin
-- "failed" means the candidate was not committed to the control plane, but
-- restoration of the prior external data plane could not be confirmed.
CREATE TABLE apply_status_v2 (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id  INTEGER NOT NULL,
    state        TEXT NOT NULL DEFAULT 'submitted'
                     CHECK(state IN ('submitted', 'confirmed', 'rolled_back', 'failed')),
    reason       TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id) ON DELETE CASCADE
);
INSERT INTO apply_status_v2(id, snapshot_id, state, reason, created_at, updated_at)
SELECT id, snapshot_id, state, reason, created_at, updated_at FROM apply_status;
DROP TABLE apply_status;
ALTER TABLE apply_status_v2 RENAME TO apply_status;
CREATE INDEX idx_apply_status_snapshot ON apply_status(snapshot_id);
CREATE INDEX idx_apply_status_state ON apply_status(state);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE apply_status_v1 (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id  INTEGER NOT NULL,
    state        TEXT NOT NULL DEFAULT 'submitted'
                     CHECK(state IN ('submitted', 'confirmed', 'rolled_back')),
    reason       TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id) ON DELETE CASCADE
);
INSERT INTO apply_status_v1(id, snapshot_id, state, reason, created_at, updated_at)
SELECT id, snapshot_id,
       CASE WHEN state = 'failed' THEN 'rolled_back' ELSE state END,
       CASE WHEN state = 'failed' THEN 'downgraded from failed: ' || COALESCE(reason, '') ELSE reason END,
       created_at, updated_at
FROM apply_status;
DROP TABLE apply_status;
ALTER TABLE apply_status_v1 RENAME TO apply_status;
CREATE INDEX idx_apply_status_snapshot ON apply_status(snapshot_id);
CREATE INDEX idx_apply_status_state ON apply_status(state);
-- +goose StatementEnd
