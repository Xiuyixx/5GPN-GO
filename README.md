# 5GPN-Go

Personal transparent-proxy gateway, refactor of [5GPN-X](https://github.com/Xiuyixx/5GPN-X) into a single Go binary with an embedded React + Catalyst UI Kit panel.

## Status

**M0 Bootstrap** — repository skeleton, tooling, and schemas only. No user-facing functionality yet.

Full plan lives in `.omc/plans/5gpn-refactor-consensus-plan.md` (not published; internal working doc).

## Layout

```
cmd/                Go entrypoints (5gpn daemon, 5gpn-installer, 5gpn-ctl)
internal/           Non-exported Go packages (api, rules, exit, db, config, tgbot, ios, ...)
web/                React 19 + Tailwind v4 + Catalyst UI Kit panel
deploy/             systemd units, install.sh bootstrap, legacy-compat rescue scripts
configs/            YAML config schema examples
docs/               architecture, security posture, milestones, tech-debt
tests/e2e/          lxc/systemd-based end-to-end scenarios (M4)
5GPN-X/             Legacy source (read-only reference during migration)
catalyst-ui-kit/    Tailwind Plus Catalyst kit (source of web/src/components/ui/*)
```

## Building

```
make build          # bare daemon (panel served from disk during dev)
make release        # single binary with embedded panel bundle
make test
make lint
```

## Contributing

See [.omc/plans/5gpn-refactor-consensus-plan.md](./.omc/plans/5gpn-refactor-consensus-plan.md) for acceptance criteria per milestone.
