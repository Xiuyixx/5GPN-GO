# Tech debt

The entries below are current, observable limitations. They are not accepted as
production-safe merely because the code has a fail-closed path.

| Area | Current state | Required resolution |
|---|---|---|
| Runtime privilege | The installed daemon is unprivileged and restricted to its data directory, while apply/restart/MTG paths write system locations or call `systemctl`. There is no privileged helper or polkit policy. | Introduce a small authenticated helper with an allowlisted command/file surface, or redesign ownership so the daemon never needs those operations. Add real installed-unit integration tests. |
| Updates | Release check works; daemon-side update apply returns `updater_requires_supervisor`. | Put download, replacement, restart, health verification, and rollback in an external installer/supervisor that survives daemon exit. |
| Browser auth | HS256 JWTs are revocable server-side, but Zustand persists the bearer token in `localStorage`. | Choose an explicit threat model; prefer a Secure/HttpOnly/SameSite cookie flow if browser script access is unnecessary, with CSRF handling and migration tests. |
| Backups | Export is plaintext and includes a full SQLite hot-copy; panel import applies only `rules/active.yaml`. | Add authenticated encryption if required. Define and test a separate offline or transactional full-database recovery procedure instead of implying panel import restores everything. |
| Snapshot terminology | Snapshots are SQLite metadata plus paired rule YAML. The legacy `tarball_path` column remains but current applies leave it empty. | Remove/migrate the legacy column or keep documentation and API names explicitly rule-version based. |
| iOS profile | Profile XML is unsigned. Its primary payload uses DoH, while preflight probes only a separate loopback DoT listener and skips certificate verification for that local connection. | Decide the intended primary/fallback protocol contract, then preflight the actual public profile path and add signing if device-ready deployment is claimed. |
| Installer authenticity | SHA-256 is checked against a manifest downloaded from the same release; no publisher signature is verified. | Sign release artifacts/manifest with an independently verifiable identity and enforce verification in the bootstrap. |
| Licensing | The repository includes first- and third-party material under potentially different terms. No compatibility conclusion is recorded here. | Obtain confirmation from the relevant rights holders or qualified legal counsel before redistribution claims or release decisions. |
| File-size guard limit | `internal/core/applier.go` reached 806 lines during the v0.4.2 refactor. The guard ceiling was raised from 800 to 900 in `scripts/check-file-size.sh` to unblock the release rather than force an in-flight split. | Split `applier.go` along the exit-switch vs rule-apply boundary and lower the guard back to 800. |

The source-size guard is an engineering gate, not a substitute for the release
blockers above. CI currently enforces it for first-party non-test Go and
TypeScript sources.
