# M4 e2e smoke suite

Runs against a real 5GPN daemon binary on an ephemeral state dir, without
touching any live systemd unit. Purpose is to catch cross-package
regressions (JWT + snapshot pipeline + SPA fallback) that unit tests miss.

Build-tag `e2e` gates every file here so `go test ./...` on a dev machine
skips them, while CI (see [../../.github/workflows/ci.yml](../../.github/workflows/ci.yml))
runs them explicitly with `go test -tags e2e ./tests/e2e/...`.

## How the harness works

1. `E2E_BINARY` (or `../../dist/5gpn` by default) points at a freshly
   built daemon. CI runs `make build` first so the suite exercises the
   same commit under test.
2. Each test spins the daemon on a random localhost port under
   `t.TempDir()`, waits for `GET /api/v1/bootstrap` to return `200`,
   exercises whatever surface it needs, and tears down.
3. TLS is skipped via `--insecure`. Setup token is captured from stdout.
4. Failures print the daemon's stderr for post-mortem.

## Adding a test

Follow [bootstrap_test.go](bootstrap_test.go): call `startDaemon(t)`,
hit `d.URL(path)`, assert the response.

## Why not lxc / KVM here?

`plan.md` M4 mentions "lxc + systemd" for the *installer* e2e (kicking
systemctl, service unit hardening). That layer is orthogonal — the files
here cover the daemon-API surface which is by far the most churn-prone.
The installer path already has --os-fixture CI coverage.

See [legacy-policies.md](./legacy-policies.md) for regression targets
carried over from `5GPN-X/tests/`.
