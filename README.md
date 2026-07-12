# 5GPN-Go

[English](README.md) · [中文](README.zh-CN.md)

Experimental personal DNS and gateway control plane. Single daemon binary with an embedded
React + Tailwind + Catalyst UI Kit panel. Refactor of the legacy
[5GPN-X](https://github.com/Xiuyixx/5GPN-X) stack (Go + Python + a
3.3k-line install.sh) into one clean daemon and one panel.

Not a SaaS. Single user, single machine, single daemon process. Designed for the
person who runs their own gateway on a small VPS and wants something
they can actually reason about.

## What it does

- **Rule-visual routing**: DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD /
  GEOSITE / GEOIP / IP-CIDR / RULE-SET / MATCH with per-rule priority,
  action, and enable/disable, edited from the panel. The TG bot exposes a
  smaller text-command operations surface backed by the same stores/Applier.
- **Rule validation + observed apply**: Dry-run validates the rule model and
  evaluates user-provided fixtures with the in-process matcher. It does not
  run mihomo or dnsdist config-check commands. The systemd orchestrator has a
  post-reload process-state observation and attempts to restore previous
  rendered files on failure. Its default probe checks only whether dnsdist and
  mihomo are active; it does not check sniproxy, DNS answers, or the public
  egress IP. Apply status is persisted as `submitted`, `confirmed`,
  `rolled_back`, or `failed`, where `failed` means a complete rollback could
  not be confirmed.
- **Rule-version snapshots**: each distinct rule hash creates a `snapshots` row;
  identical retries reuse it, while each apply creates a
  `rule_versions` row in SQLite. A snapshot is a database record paired with
  rules YAML, not a per-apply tarball. Rollback re-applies that rule version.
- **Panel**: React 19 + Tailwind v4 + Catalyst UI. Setup, login, first-run
  wizard, Dashboard, Rules, Exits, Snapshots, Backup, Logs (SSE), and Settings.
- **TG bot**: text commands for selected operations, with a chat-id
  whitelist. Empty whitelist = daemon refuses to start the bot.
- **iOS encrypted-DNS profile**: an unsigned XML `.mobileconfig` with a
  primary DoH payload and fallback DoT payload is served from the panel, and
  its URL can be QR encoded. The enable preflight probes only the daemon's
  loopback DoT listener; it does not validate the profile's public DoH path.
- **WA-shim library**: the legacy parser/relay logic has an in-tree Go port,
  but the daemon and installer do not start it in v0.4.0.
- **Release update check**: the panel can inspect the latest release. In-process
  update apply is disabled; installation must be performed by the external
  installer or a future privileged supervisor that survives daemon restart.
- **Backup export and rule import**: export produces a plaintext tar.gz with
  active rules, snapshot metadata, a WAL-safe SQLite hot-copy via
  `VACUUM INTO`, and a README. Panel import restores `rules/active.yaml` only;
  the database and manifest are offline recovery artifacts.
- **Installer with `--dry-run`**: `5gpn-installer install / upgrade /
  uninstall / status / doctor / migrate-from-legacy`, plus
  `--os-fixture` for previewing behavior against Ubuntu 22.04/24.04,
  Debian 12/13, CentOS 9, AlmaLinux 9, Rocky 9, RHEL 9, Fedora 40
  without provisioning a VM.

## Current production limitations

- The canonical service runs as an unprivileged `5gpn` user with
  `NoNewPrivileges=yes` and `ProtectSystem=strict`. Runtime apply, daemon
  restart, and MTG controls still write system paths or invoke `systemctl`.
  No privileged helper, polkit policy, or equivalent narrow elevation path is
  shipped yet, so these operations are not production-operational under the
  installed unit and return permission errors.
- Panel JWTs use HS256 and are checked against revocable SQLite sessions. The
  browser currently persists the bearer token in `localStorage`; this remains
  exposed to any successful same-origin script injection.
- Backup archives are not encrypted and can contain secrets through the SQLite
  copy. Treat exports as sensitive material. Panel import is intentionally not
  a full database restore.
- The experimental SNI/QUIC forwarders connect directly from the host to the
  requested public destination. They do not traverse the selected mihomo exit,
  so the current Path B controls are not an active-exit transparent proxy.
- The internal-only gate covers the panel and in-process SNI/QUIC forwarders.
  It does not restrict the external `mtg.service`; configure a separate host
  firewall or systemd access policy for MTProxy.

## Data plane stays external

dnsdist + mihomo + sniproxy remain external systemd units and are
not re-implemented in Go. The daemon assembles effective state from base YAML
plus SQLite-backed rules, exits, and panel settings, renders their configs, and
orchestrates reload/restart. The commands are:

- dnsdist: `systemctl reload dnsdist` (SIGHUP config reload)
- mihomo: `systemctl reload mihomo`
- sniproxy: `systemctl restart sniproxy`

These commands require a privileged execution design that is not yet present
in the installed unprivileged service. Because every systemd apply runs the
sniproxy restart, a working `sniproxy.service` is an apply prerequisite. No
apply-latency SLO is currently made.

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
  orchestrator/     systemd apply pipeline + process-state probe/rollback
  proxy/washim.go   Go port of wa-shim.py
  rules/            model / parse / dry-run / hash / import-legacy
  tgbot/            Text-command bot with chat-id whitelist
  updater/          Release metadata/download primitives; runtime apply disabled
  web/              go:embed of the panel dist
web/                React 19 panel (Vite + Tailwind v4 + Catalyst UI)
deploy/             Slim bootstrap install.sh (fetch + sha256 verify + exec)
configs/            static boot-config example; runtime state is also in SQLite
tests/e2e/          Real-daemon smoke suite (build-tag `e2e`)
scripts/            check-file-size.sh, pre-commit
5GPN-X/             Legacy source — kept as read-only reference (subtree, own go.mod shim)
catalyst-ui-kit/    Tailwind Plus Catalyst kit (source for web/src/components/ui/)
docs/               architecture, security, milestones, tech-debt, tgbot-legacy-commands
```

## Build

```sh
make build          # daemon + installer + ctl (no panel embed)
make release        # release binaries; daemon embeds the web bundle
make test           # unit tests, race detector on
make web-test       # frontend Vitest suite
make coverage       # per-package coverage report
make lint           # golangci-lint + web lint
make size-check     # first-party non-test Go/TS 800-line guard
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

The host bootstrap downloads release binaries and hands off to the Go installer:

```sh
curl -fsSL https://raw.githubusercontent.com/Xiuyixx/5GPN-Go/main/deploy/install.sh | sudo bash
```

The script downloads `SHA256SUMS`, `5gpn-installer-<os>-<arch>`, and the daemon
from the same GitHub release, verifies the two binaries against that checksum
file, and invokes `install`. This detects corruption or a mismatch within the
downloaded release, but it is not a publisher signature or an independent
authenticity proof. `cmd/5gpn-installer/` is the CLI entrypoint; user, directory,
config, unit, and service actions live in `internal/installer/`.

The installer can lay down and start the service, but the runtime privileged
helper needed for system configuration writes and `systemctl` operations is
still unresolved; review [docs/tech-debt.md](docs/tech-debt.md) before treating
the installation as production-ready.

Migrate an existing 5GPN-X install:

```sh
5gpn-installer migrate-from-legacy --dry-run     # preview only
5gpn-installer migrate-from-legacy               # write /etc/5gpn/config.yaml
5gpn-installer migrate-from-legacy --allow-partial # explicitly accept omissions
```

Migration can render domain, DNS, Telegram, and iOS settings from the old
`/opt/proxy-gateway/` and `/etc/proxy-gateway/` trees without mutating them.
Rules, policy maps, exits, and a non-direct active exit have no lossless path
through the current config-only migrator. Their presence makes migration fail
closed by default; `--allow-partial` is required to omit them explicitly.

## Quality gates (M4)

Every push to `main` runs:

- **`ci.yml`**: Go 1.25 on Ubuntu 22.04/24.04 runs tidy, vet, build, and
  `go test -race`; Node 20 runs `npm test`, lint, and build as hard gates.
  The workflow also runs the source-size guard, e2e smoke, dependency-boundary
  check, and a 9-way installer-fixture dry-run matrix.
- **`security.yml`**: `govulncheck` hard-fails, `npm audit
  --audit-level=high` hard-fails, on-push whenever `go.mod` or
  `package-lock.json` changes, plus a Monday cron.

Locally:

- `scripts/pre-commit` runs the 800-LOC guard on staged files
  (`make install-hooks` to wire it in).
- `make test`, `make web-test`, and `make lint` mirror the main local gates.

## Docs

- `docs/architecture.md` — high-level topology
- `docs/security.md` — threat model + hardening posture
- `docs/tech-debt.md` — current limitations and required resolutions
- `docs/milestones.md` — implementation status and release blockers
- `docs/tgbot-legacy-commands.md` — legacy `tgbot.py` audit and
  new-panel parity map (AC8 checklist)
- Local `.omc/` plans are ignored tool state and are not shipped as project
  documentation.

## License

Before distributing this repository or builds derived from it, obtain
confirmation from the relevant rights holders or qualified legal counsel for
all included first- and third-party material. This README makes no legal
conclusion about license compatibility or redistribution rights.
