-- +goose Up
-- +goose StatementBegin
-- rulesets is the panel-owned registry of remote rule providers. Each
-- row = one URL the operator has pointed 5GPN at. Unlike the older
-- rule_sources table (used only by chinalist for its ETag cache), this
-- one carries the full lifecycle metadata + cached content the panel
-- needs to render a "Rulesets" section and periodically re-sync from
-- upstream without re-parsing every apply.
--
-- Fields:
--   name           user-facing id + primary key. Frontend renders it as
--                  the card title; auto-generated at import time as
--                  imp-<sourceKind>-<hex> unless the operator picked one.
--   source_url     the URL we fetch from. Kind is auto-detected on each
--                  sync so a redirect from gfwlist raw to a mirror still
--                  works.
--   kind           parsed format after auto-detect. 'clash' for native
--                  KIND,VALUE,POLICY lines; 'gfwlist' for AutoProxy.
--   action         the panel-picked policy imported non-terminal rules
--                  are rewritten to. direct/block are always preserved
--                  as-is.
--   priority       insertion priority when the ruleset is expanded into
--                  the effective ruleset. All rules from this ruleset
--                  share this priority; insertion order within them is
--                  preserved.
--   enabled        soft-off toggle. Disabled rulesets stay in the DB
--                  but are skipped during apply.
--   etag /
--   last_modified  HTTP cache headers we replay on the next sync.
--   last_synced_at unix epoch. Null before the first successful sync.
--   last_error     the error string from the most recent failed sync.
--                  Null when the last sync succeeded.
--   rule_count     how many rules the last successful sync produced.
--                  Kept for O(1) rendering — no need to re-parse content
--                  to draw the card.
--   content        cached body from the last successful sync. Used by
--                  apply so we don't hit the upstream on every apply.
CREATE TABLE rulesets (
    name            TEXT PRIMARY KEY,
    source_url      TEXT NOT NULL,
    kind            TEXT NOT NULL,
    action          TEXT NOT NULL,
    priority        INTEGER NOT NULL DEFAULT 500,
    enabled         INTEGER NOT NULL DEFAULT 1,
    etag            TEXT NOT NULL DEFAULT '',
    last_modified   TEXT NOT NULL DEFAULT '',
    last_synced_at  INTEGER,
    last_error      TEXT,
    rule_count      INTEGER NOT NULL DEFAULT 0,
    content         BLOB,
    created_at      INTEGER NOT NULL,
    created_by      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_rulesets_enabled ON rulesets(enabled);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS rulesets;
-- +goose StatementEnd
