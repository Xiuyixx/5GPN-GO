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

- **install.sh slim-down** — resolved. `deploy/install.sh` is now 83 lines and delegates to `cmd/5gpn-installer`.

## M4 policy

Rules under `.github/workflows/` are strict gates unless listed here.

- **File-size guard**: `scripts/check-file-size.sh`; any non-test file
  under `internal/` above 800 lines fails CI and blocks the pre-commit
  hook. To exempt a file, split it or bump `MAX_LINES` in the script
  and record the reason here with an expiry.
- **Security workflow** (`.github/workflows/security.yml`): govulncheck
  hard-fails; `npm audit` fails on `high`+. Weekly cron + on-demand + on-push
  when dep manifests change. Any accepted finding must be pinned and
  logged here with an expiry date.
- **e2e smoke** (`tests/e2e/`): `-tags e2e` gated. CI builds the daemon
  and runs it against a random localhost port. Local devs skip by default.
- **Coverage baseline** as of M4 kickoff (M4 target ≥ 70% on key packages):
  - `internal/rules` 75.8%, `internal/exit` 79.0%, `internal/config/render` 78.4%,
    `internal/installer` 77.9%, `internal/ios` 70.6% — clear.
  - `internal/api` 51.7% — needs a second pass in the next M4 batch.
  - `internal/db` 8.6%, `internal/tgbot` 25.6%, `internal/proxy` 17.8`%,
    `internal/metrics` 35.2%, `internal/orchestrator` 47.5% — not on the
    plan's ≥70% key-package list; separate follow-up tickets.
