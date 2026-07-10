# 5GPN-Go

[English](README.md) · [中文](README.zh-CN.md)

Personal transparent-proxy gateway. Single Go binary with an embedded
React + Tailwind + Catalyst UI Kit panel. Refactor of the legacy
[5GPN-X](https://github.com/Xiuyixx/5GPN-X) stack (Go + Python + a
3.3k-line install.sh) into one clean daemon and one panel.

Not a SaaS. Single user, single machine, single binary. Designed for the
person who runs their own gateway on a small VPS and wants something
they can actually reason about.

## What it does

- **Rule-visual routing**: DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD /
  GEOSITE / GEOIP / IP-CIDR / RULE-SET / MATCH with per-rule priority,
  action, and enable/disable — edited from the panel or via TG bot.
- **Dry-run + auto-rollback**: every rule change runs through a static
  validator first (mihomo config-check + dnsdist config-check + fixture
  matcher). If a live apply health-checks fail, the previous snapshot is
  restored automatically. This is the differentiator against clash-verge
  / mihomo-party.
- **Immutable snapshots**: every apply lands as a tar in
  `/var/lib/5gpn/snapshots/{id}.tar.gz` plus a SQLite row. Rollback to
  any historical snapshot in one click.
- **Panel**: React 19 + Tailwind v4 + Catalyst UI. Login /
  Dashboard / Rules / Exits / Snapshots / Backup / Logs (SSE).
- **TG bot**: text commands mirroring the panel, with a chat-id
  whitelist. Empty whitelist = daemon refuses to start the bot.
- **iOS DoT profile**: signed `.mobileconfig` served + QR encoded.
- **wa-shim**: 1:1 Go port of the legacy Python shim (WA_PREFIXES,
  KNOWN handshakes, allow-CIDR, 14 env vars).
- **Blue-green self-update**: `.prev` backup + sha256 verify + health
  check + auto-rollback if the new binary fails.
- **Panel-driven backup/restore**: tar.gz containing rules, snapshots
  manifest, and a WAL-safe SQLite hot-copy via `VACUUM INTO`.
- **Installer with `--dry-run`**: `5gpn-installer install / upgrade /
  uninstall / status / doctor / migrate-from-legacy`, plus
  `--os-fixture` for previewing behavior against Ubuntu 22.04/24.04,
  Debian 12/13, CentOS 9, AlmaLinux 9, Rocky 9, RHEL 9, Fedora 40
  without provisioning a VM.

## Data plane stays external

dnsdist + mihomo (1.19.28) + sniproxy are kept as first-party systemd
units, not re-implemented in Go. The daemon renders their configs from
a single YAML source of truth and orchestrates reload/restart. Hot-swap
targets:

- dnsdist: `systemctl reload dnsdist` (SIGHUP config reload)
- mihomo:  `PUT /configs?reload=true` on the REST API
- sniproxy: `systemctl restart sniproxy` (~1s data-plane blip)

Total apply window ≤ 1.5s.

## Repository layout

```
cmd/                Go entrypoints (5gpn daemon, 5gpn-installer, 5gpn-ctl)
internal/
  api/              chi router, JWT auth, session revocation, rate limiter
  config/           YAML schema + renderers (dnsdist / mihomo / sniproxy)
  db/               SQLite migrations via goose
  exit/             10 protocol parsers (SS / VMess / Trojan / VLESS+reality / Hy2 / TUIC / AnyTLS / SS2022 / SOCKS / HTTP)
  installer/        Install / Upgrade / Uninstall / Status / Doctor / Migrate
                    with Env + Executor split (Real / Recording) so --dry-run
                    and tests share the code path
  ios/              mobileconfig + inetd-style ServeConn
  metrics/          /proc sampler → SQLite ring
  orchestrator/     systemd apply pipeline + health-check rollback
  proxy/washim.go   Go port of wa-shim.py
  rules/            model / parse / dry-run / hash / import-legacy
  tgbot/            Text-command bot with chat-id whitelist
  updater/          Blue-green swap + sha256 + auto-rollback
  web/              go:embed of the panel dist
web/                React 19 panel (Vite + Tailwind v4 + Catalyst UI)
deploy/             Slim bootstrap install.sh (fetch + sha256 verify + exec)
configs/            example.yaml (schema-authoritative)
tests/e2e/          Real-daemon smoke suite (build-tag `e2e`)
scripts/            check-file-size.sh, pre-commit
5GPN-X/             Legacy source — kept as read-only reference (subtree, own go.mod shim)
catalyst-ui-kit/    Tailwind Plus Catalyst kit (source for web/src/components/ui/)
docs/               architecture, security, milestones, tech-debt, tgbot-legacy-commands
```

## Build

```sh
make build          # daemon + installer + ctl (no panel embed)
make release        # single binary with embedded web bundle, target ≤ 40 MB
make test           # unit tests, race detector on
make coverage       # per-package coverage report
make lint           # golangci-lint + web lint
make size-check     # fail if any internal/**/*.go crosses 800 LOC
make install-hooks  # link scripts/pre-commit into .git/hooks/
```

Dev loop:

```sh
# terminal 1
cd web && npm run dev

# terminal 2
go run ./cmd/5gpn --config configs/example.yaml --data /tmp/5gpn --insecure --listen 127.0.0.1:8443
```

The daemon prints a one-shot **setup token** on cold start; POST it to
`/api/v1/bootstrap` with a username + password to claim the panel.

## Install

Production install fetches a signed binary and hands off:

```sh
curl -fsSL https://raw.githubusercontent.com/Xiuyixx/5GPN-Go/main/deploy/install.sh | sudo bash
```

Under the hood the script (83 lines) downloads
`5gpn-installer-<os>-<arch>` from the current GitHub release, verifies
its sha256, and invokes `install`. The heavy logic (user + dirs +
config + systemd unit + enable) lives in `cmd/5gpn-installer/`.

Migrate an existing 5GPN-X install:

```sh
5gpn-installer migrate-from-legacy --dry-run     # preview only
5gpn-installer migrate-from-legacy               # write /etc/5gpn/config.yaml
```

Migration extracts domain, DNS upstreams, current exit, rules,
policy map, exit list, TG token + admin ids, and iOS profile UUID from
the old `/opt/proxy-gateway/` and `/etc/proxy-gateway/` trees, never
mutating them.

## Quality gates (M4)

Every push to `main` runs:

- **`ci.yml`**: `go build` + `go test -race` on 1.22/1.23 × 22.04/24.04,
  size-check, web lint + build, e2e smoke (daemon boot + bootstrap +
  login + protected endpoint), and a 9-way installer-fixture matrix
  that dry-run installs against every supported distro.
- **`security.yml`**: `govulncheck` hard-fails, `npm audit
  --audit-level=high` hard-fails, on-push whenever `go.mod` or
  `package-lock.json` changes, plus a Monday cron.

Locally:

- `scripts/pre-commit` runs the 800-LOC guard on staged files
  (`make install-hooks` to wire it in).
- `make test` and `make lint` are green pre-push.

Coverage baseline (M4 close):

| package | coverage | notes |
|---|---|---|
| `internal/rules` | 75.8% | |
| `internal/exit` | 79.0% | |
| `internal/config/render` | 78.4% | |
| `internal/installer` | 77.9% | |
| `internal/ios` | 70.6% | |
| `internal/api` | 65.8% | SSE + `ListenAndServe` covered by `tests/e2e/` |

## Docs

- `docs/architecture.md` — high-level topology
- `docs/security.md` — threat model + hardening posture
- `docs/tech-debt.md` — accepted trade-offs with expiry
- `docs/milestones.md` — M0-M4 log
- `docs/tgbot-legacy-commands.md` — legacy `tgbot.py` audit and
  new-panel parity map (AC8 checklist)
- `.omc/plans/5gpn-refactor-consensus-plan.md` — full plan with
  RALPLAN-DR consensus, 17 risks, 15 acceptance criteria (internal;
  reference)

## License

See `LICENSE`. Catalyst UI Kit files under `web/src/components/ui/` and
`catalyst-ui-kit/` retain the Tailwind Plus license header verbatim.
