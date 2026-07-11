-- +goose Up
-- +goose StatementBegin
-- panel_settings holds panel-driven configuration that the daemon needs
-- to persist across restarts without touching /etc/5gpn/config.yaml
-- (which is owned by the installer / operator and gated by systemd
-- ProtectSystem=strict on the deployed unit).
--
-- Values are JSON-encoded so structured payloads (admin_chat_ids: []int64,
-- allow_cidr: []string) can live alongside plain strings and ints without
-- extra tables. The Go side owns the schema per key; the DB just stores
-- opaque JSON.
--
-- The wizard flow (v0.2.5) writes here for keys under:
--   server.*  -> domain, panel_bind, panel_port
--   tls.*     -> acme_enabled, acme_email
--   tgbot.*   -> token, admin_chat_ids
--   washim.*  -> enabled, listen, port, backend, wa_host, allow_cidr
--   wizard.*  -> complete
--
-- On boot the daemon reads panel_settings, layers it over the YAML
-- Config from config.yaml, and hands the merged view downstream. YAML
-- stays authoritative for anything not present in panel_settings.
CREATE TABLE panel_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_panel_settings_updated_at ON panel_settings(updated_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS panel_settings;
-- +goose StatementEnd
