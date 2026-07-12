# Milestones

This status log separates code-complete behavior from production readiness. A
checked item means the behavior exists and is covered locally; it does not
override the blockers below.

## Implemented

- [x] SQLite-backed panel users, revocable sessions, settings, exits, audit
  rows, rule versions, and snapshot metadata.
- [x] Rule validation, fixture Dry-run, serialized apply tracking, health
  observation, and rollback of rendered configuration.
- [x] Snapshot rollback and backup rule import use `core.Applier` rather than a
  database-only active-pointer flip.
- [x] Backup export validates SQLite and emits active rules, manifest, database
  hot-copy, and recovery README; import is bounded and restores rules only.
- [x] Release checking is separate from update apply; unsafe in-process update
  apply is disabled.
- [x] Legacy migration refuses unsupported/lossy state by default and requires
  `--allow-partial` for explicit omission.
- [x] CI uses Go 1.25 on Ubuntu 22.04/24.04 and Node 20. Go vet/build/race tests,
  frontend Vitest/lint/build, lint, source-size, e2e, dependency, and installer
  fixture jobs are configured as blocking jobs.

## Release blockers

- [ ] Design and implement a narrowly scoped privileged helper or equivalent
  policy for rendered system configuration and approved `systemctl` actions.
- [ ] Move upgrade ownership to an external installer/supervisor with
  cross-restart health verification and rollback; keep daemon-side apply
  disabled until then.
- [ ] Decide and implement the browser credential model. The current HS256
  bearer token remains in `localStorage`.
- [ ] Decide whether encrypted backup and full, transactional database restore
  are required; current archives are plaintext and panel import is rules-only.
- [ ] Decide whether iOS profiles require signing and add an external
  reachability check if public-device readiness is claimed.
- [ ] Obtain rights-holder or qualified legal review before making any license
  compatibility or redistribution claim for the complete repository/bundle.
