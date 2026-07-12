-- +goose Up
-- +goose StatementBegin
-- wg_peers reserves schema for a future kernel-WireGuard split-tunnel
-- plane. v0.4.0 has no runtime package, enrollment API, profile generator,
-- or reconciler that reads or writes this table.
--
-- Fields:
--   device_name        operator-supplied label shown on the Devices
--                       page (e.g. "iPhone"). Not unique — purely
--                       cosmetic.
--   pubkey             the peer's WireGuard public key, base64. Unique
--                       because it is both the wg(8) peer identifier
--                       and the enrollment/profile-URL lookup key.
--   private_key        reserved opaque storage. No encryption or key
--                       lifecycle is implemented by the current runtime.
--   address             the peer's tunnel IP, CIDR form (e.g.
--                       10.66.66.2/32). Assigned at enrollment.
--   endpoint            the server's public host:port the peer dials
--                       (e.g. lioh.myrouteserver.com:51820). Embedded
--                       in the rendered WgQuickConfig.
--   allowed_ips_hash    SHA-256 of the last AllowedIPs set issued to
--                       this peer. Compared against the reconciler's
--                       freshly-derived hash so unchanged RuleTables
--                       are a no-op (Phase 6).
--   profile_uuid        the mobileconfig PayloadUUID for this peer.
--                       Rotates only when allowed_ips_hash changes, so
--                       an unchanged RuleTable does not force a
--                       re-install on the device.
--   payload_uuid        the WireGuard payload's own PayloadUUID inside
--                       the mobileconfig (distinct from profile_uuid,
--                       which is the top-level profile identifier).
--   mtu                 WireGuard interface MTU pushed to the peer.
--                       Defaults to 1280 — the safe lower bound that
--                       avoids the cellular MTU-blackhole failure mode
--                       (see plan pre-mortem F2), not the iOS default
--                       of 1420.
--   dns_address         DNS server pushed to the peer's [Interface]
--                       block. Defaults to the gateway's own wg0
--                       address (10.66.66.1) so DNS keeps flowing
--                       through the existing resolver plane.
--   revoked             soft-delete flag. Revoked peers are dropped
--                       from the next SyncConf but the row is kept for
--                       audit/history.
--   state               reserved enrollment lifecycle value. The current
--                       runtime does not transition or reconcile it.
--   created_at          unix epoch of enrollment.
--   last_reconcile_at   unix epoch of the most recent reconcile that
--                       touched this peer's allowed_ips_hash /
--                       profile_uuid. Null until the first reconcile.
CREATE TABLE wg_peers (
    id                  INTEGER PRIMARY KEY,
    device_name         TEXT NOT NULL,
    pubkey              TEXT NOT NULL UNIQUE,
    private_key         BLOB NOT NULL,
    address             TEXT NOT NULL,
    endpoint            TEXT NOT NULL,
    allowed_ips_hash    TEXT NOT NULL,
    profile_uuid        TEXT NOT NULL,
    payload_uuid        TEXT NOT NULL,
    mtu                 INTEGER NOT NULL DEFAULT 1280,
    dns_address         TEXT NOT NULL DEFAULT '10.66.66.1',
    revoked             INTEGER NOT NULL DEFAULT 0,
    state               TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','active','failed','revoked')),
    created_at          INTEGER NOT NULL,
    last_reconcile_at   INTEGER
);
CREATE INDEX idx_wg_peers_active ON wg_peers(revoked, created_at);
CREATE INDEX idx_wg_peers_state ON wg_peers(state);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_wg_peers_state;
DROP INDEX IF EXISTS idx_wg_peers_active;
DROP TABLE IF EXISTS wg_peers;
-- +goose StatementEnd
