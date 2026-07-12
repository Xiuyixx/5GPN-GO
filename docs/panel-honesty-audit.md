# 5GPN Panel Honesty Audit

**Audit date:** 2026-07-13

**Scope:** React routes and Settings sections under `web/src/`, cross-checked
against the current handlers in `internal/api/`. This audit distinguishes a
real handler from a production-operational control; those are not equivalent
while the privilege boundary remains unresolved.

## Status terms

- **Verified:** persistence/effect and the success signal match.
- **Conditional:** the code path is real and surfaces errors, but an external
  dependency or host privilege is required.
- **Mismatch:** visible copy omits or overstates a material behavior.
- **Disabled:** intentionally unavailable and represented as unavailable.

## Cross-cutting facts

- Panel tokens are HS256 JWTs backed by revocable SQLite sessions. The browser
  persists the token in `localStorage`; it is not an HttpOnly cookie.
- Authenticated requests send a bearer header. Logs use a fetch-based SSE client
  with the same header; the token is not placed in the query string.
- Rule/exit/rollback/import apply can return `202 Accepted` with an `apply_id`.
  The frontend polls `/api/v1/applies/{id}` before reporting completion.
- The durable lifecycle is `submitted` followed by one terminal state:
  `confirmed`, `rolled_back`, or `failed`. `failed` is distinct from
  `rolled_back`: it means the operation was not confirmed and a complete
  rollback of its prior state could not be confirmed.
- The default systemd health probe checks only whether dnsdist and mihomo are
  active. It does not check sniproxy, send a DNS query, or verify the public
  egress IP, so `confirmed` has that deliberately narrow meaning.
- `--orchestrator=noop` commits control-plane state without an external data
  plane and is intended for development/tests. A UI success in NoOp mode must
  not be interpreted as a systemd deployment result.
- The canonical installed unit cannot currently perform the system-file writes
  or `systemctl` calls required by several controls. Those handlers return
  errors rather than fabricating success, but the controls are not
  production-operational until a privileged helper/equivalent policy exists.

## Authentication, setup, and shell

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| Bootstrap claim | `POST /api/v1/bootstrap` | Verified | The setup token is checked and first-user creation is atomic. A subsequent claim conflicts. |
| Sign in | `POST /api/v1/login` | Verified | Checks bcrypt, creates a revocable session, and returns an HS256 JWT. The browser then stores it in `localStorage`, which is a documented security limitation. |
| Current identity | `GET /api/v1/me` | Verified | Returns identity from verified claims/session. |
| Log out | `POST /api/v1/logout` | Verified | Revokes the server session and clears client auth state. |
| App navigation | client-side routes | Verified | Setup, login, wizard, dashboard, rules, exits, snapshots, backup, logs, and settings are protected/routed according to bootstrap and auth state. |

## First-run wizard

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| Load/save wizard settings | `GET/POST /api/v1/settings/panel` | Verified for persistence | The final POST writes a batch to `panel_settings`; intermediate Back/Next actions are local only. It does not rewrite `/etc/5gpn/config.yaml`. |
| Telegram credentials | same POST plus bot manager update | Verified | Invalid runtime credentials are surfaced as a warning and are not silently reported as active. |
| Server/TLS values | `panel_settings` | Conditional | Values persist, but listener/TLS changes that require restart depend on the restart/privilege path. Finishing the wizard alone is not proof that a public listener is reachable. |

## Dashboard and exits

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| Metrics and DNS metrics | `GET /api/v1/metrics`, `/api/v1/metrics/dns` | Verified | Displays collected rows; it does not manufacture host samples in the browser. |
| Exit list/current exit | `GET /api/v1/exits` | Verified | Reads the DB-backed exit store and active exit. |
| Add/delete exit | `/api/v1/exits/add`, `/delete` | Verified | These are control-plane store operations. Adding an exit does not claim to switch traffic; deleting the active exit is rejected. |
| Switch exit | `POST /api/v1/exits/switch` | Conditional | Uses `core.Applier`; an observing result returns `202 + apply_id`, and the panel waits for `confirmed`, `rolled_back`, or `failed` before refreshing. It is end-to-end only when the orchestrator can write/reload the external data plane, and confirmation uses the narrow process-state probe described above. |

## Rules and rulesets

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| List/edit/reset rules | `GET /api/v1/rules` plus local draft | Verified | Edits remain local until Apply; Reset reloads the active DB rule version. |
| Dry-run | `POST /api/v1/rules/dry-run` | Verified with a narrow meaning | Validates rules and evaluates fixtures using the in-process matcher. It does **not** execute mihomo/dnsdist config-check commands or start a second data plane. |
| URL/text import preview | `POST /api/v1/rules/import` | Verified | Produces draft rules. URL fetches apply the public-address network guard. Manual imports are not incorrectly grouped as registered rulesets. |
| Ruleset register/sync/toggle/delete | `/api/v1/rulesets...` | Verified for store/cache state | These operations manage ruleset metadata/cache. Enabled rulesets affect the candidate on a later rule apply. |
| Apply rules | `POST /api/v1/rules/apply` | Conditional | Creates DB snapshot/rule-version rows, validates/builds the resolver table, calls Applier, and polls its durable apply status. The active DB pointer and live resolver advance only on `confirmed`; a failed probe attempts file restoration/reload, and an incomplete or unconfirmed rollback is recorded as `failed`, not `rolled_back`. Production external reload still needs the missing privilege path. |

## Snapshots

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| List snapshots | `GET /api/v1/snapshots` | Verified | Lists SQLite snapshot metadata. `active` is derived from the active paired rule version; the newest row is not assumed to be current. |
| Roll back | `POST /api/v1/snapshots/{id}/rollback` | Conditional | The earlier DB-only flip has been removed. Rollback loads the paired rule version and sends it through Applier/apply polling, including the same four-state lifecycle. As with Apply, external systemd effect depends on host privilege. |

Snapshot terminology is important: these are DB records paired with rules YAML.
Current apply does not write `/var/lib/5gpn/snapshots/{id}.tar.gz`; the legacy
`tarball_path` schema field is empty.

## Backup

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| Export | `GET /api/v1/backup/export` | Verified disclosure | The handler exports a **plaintext** archive containing active rules, snapshot manifest, full SQLite hot-copy, and README. The panel warns before download that `db/5gpn.db` may contain secrets. |
| Import confirmation | client-side dialog | Verified disclosure | It previews file size and an approximate active-rule count, and states before confirmation that database/manifest members will be ignored. |
| Import | `POST /api/v1/backup/import` | Verified | The handler validates the complete bounded archive, rejects unknown/duplicate/non-regular members, and applies `rules/active.yaml` through Applier. It does not restore the SQLite copy or manifest, and the panel states that scope. |

The response explicitly reports ignored entries and says panel import is
rules-only. That post-action note does not replace a pre-action plaintext and
scope warning in the UI.

## Logs

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| Live tail | `GET /api/v1/events/logs?unit=...` | Conditional | Linux uses `journalctl` for an allowlisted unit and surfaces stderr. Non-Linux uses frames explicitly labelled as stub output. The stream periodically rechecks session validity. |
| Filter/clear/reconnect | client-side/fetch SSE | Verified | Filter and Clear affect the local buffer. Reconnect creates a new authenticated fetch stream. |

## Settings

| Control | Endpoint/effect | Status | Notes |
|---|---|---|---|
| Release check | `GET /api/v1/update/check` | Verified | Displays current/latest release metadata. |
| Upgrade apply | no active UI action; backend `POST /api/v1/update/apply` | Disabled | The panel now says an external installer or privileged supervisor is required. The backend returns `updater_requires_supervisor`; it never reports an in-process upgrade success. |
| Telegram bot | `/api/v1/settings/tgbot` | Verified | Runtime update failures are surfaced. Secrets are masked on reads. |
| iOS profile/preflight | profile and `/settings/ios/...` endpoints | Disclosed limitation | The profile is unsigned XML. Its primary payload uses public DoH, but preflight proves only that the separate `127.0.0.1:853` DoT listener answers, with certificate verification disabled. Panel copy states that it does not establish public DoH/device reachability. |
| MTProxy | `/api/v1/settings/mtproxy...` | Conditional, blocked in canonical install | The controller really writes the external `mtg.service` unit and invokes `systemctl`; it is not an in-process proxy. The installed unprivileged service lacks permission for those operations. |
| Internal-only access | `/api/v1/settings/frontdoor/internal-only` | Partial | CIDRs are validated, persisted, and atomically published to the panel plus in-process SNI/QUIC gate. External `mtg.service` is not covered and needs a separate host policy. |
| Path B destination-forward settings | `/api/v1/settings/frontdoor/proxy` | Partial / experimental | Validation and live DNS-spoof policy are real. SNI/QUIC forwarders currently dial public targets directly and do not traverse the active mihomo exit; host policy routing is not provisioned. |
| Change password | `POST /api/v1/password` | Verified | Updates the bcrypt hash and revokes the user's other sessions while preserving the current JWT server-side. After success, the current frontend deliberately clears its local token and routes to Login. |
| Restart daemon | `POST /api/v1/system/restart` | Conditional, blocked in canonical install | Returns `202` only if `systemctl` accepted the job; permission and non-systemd errors are returned. No privileged helper currently makes it work under the installed service. |

## Required follow-ups

1. Implement and integration-test the narrow privilege boundary before calling
   Rules Apply, exit switch, rollback, restart, or MTG controls production-ready.
2. Align iOS preflight with the profile's actual primary DoH path before
   claiming device readiness; decide whether the profile must be signed.
3. Decide whether `localStorage` bearer tokens fit the threat model or migrate
   to an HttpOnly cookie design with CSRF controls.

## Verdict

The former snapshot-rollback and backup-import DB-only success bugs are fixed:
both now use Applier and expose the durable
`submitted`/`confirmed`/`rolled_back`/`failed` lifecycle. Update apply is also
honestly disabled. The panel is not yet production-operational because the
privileged execution boundary is absent, and the iOS preflight still probes a
different transport from the profile's primary payload. No blanket "all controls are honest" or
"production ready" conclusion is made.
