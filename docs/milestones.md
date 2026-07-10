# Milestones

Milestone goals per [.omc/plans/5gpn-refactor-consensus-plan.md](../.omc/plans/5gpn-refactor-consensus-plan.md).

## M0 — Bootstrap (this milestone)

Repository skeleton, tooling, and schemas. No functionality yet.

Deliverables: `go.mod` + `web/package.json` install cleanly; `make build` produces stub binaries; SQLite migrations apply; config schema validates `configs/example.yaml`.

## M1 — MVP

Rule visual CRUD, sandbox dry-run, auto-rollback on failure, single binary ≤ 40 MB, panel login. AC1-AC5.

## M2 — Python components to Go + panel expansion

wa-shim.py, mihomo-\*-config.py, ios-http.py, tgbot.py all move to Go. Dashboard, one-click upgrade, backup/restore, live logs. AC6-AC8 + AC12-AC15.

## M3 — install.sh slim-down

`cmd/5gpn-installer` subcommands replace the 130KB install.sh; bootstrap script under 20KB with sha256 verification. AC9.

## M4 — Engineering quality gates

70% test coverage on hot paths, no source file over 800 LOC, CI matrix green. AC10-AC11.
