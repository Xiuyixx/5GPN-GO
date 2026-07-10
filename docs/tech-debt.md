# Tech debt

Recorded trade-offs that need revisiting.

## M0

- **Tailwind CSS v4 is beta-pinned** to `4.0.0-beta.6` — bump to GA once released. If the panel breaks after upgrade, fall back to a Tailwind v3 + Catalyst v3 branch (documented in [milestones.md](./milestones.md)).
- **Legacy Go files** in `5GPN-X/` conflict with the parent module. Isolated via `5GPN-X/go.mod` shim. Delete `5GPN-X/` at the end of M4 once regression tests cover its behavior.

## M1 (deferred)

- **certmagic** deferred to M3. Until then, `renew-hook.sh` continues to drive Let's Encrypt renewal; the daemon reads the certificate off disk.
- **Prometheus** integration deferred indefinitely. Internal metric ring (SQLite `metrics_snapshot`) is authoritative.

## M2 (deferred)

- **wa-shim.py Python fallback** — the Python implementation stays available for a one-week double-run window while the Go port is validated.
- **mihomo binary** stays external. Embedding it (`gomobile`-style) would violate "data plane not reinvented" — revisit only if the external binary hits an EOL.

## M3 (deferred)

- **install.sh slim-down** — until M3 lands, the 130KB `5GPN-X/install.sh` remains the ground-truth installer path. New users see the M0 skeleton `deploy/install.sh` and are pointed at the plan.
