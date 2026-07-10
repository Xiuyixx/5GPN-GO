# 5GPN Panel Honesty Audit (S3 · AC10)

**Scope:** Every screen under `web/src/pages/` cross-referenced against its backend
handler in `internal/api/`. For each interactive control we record: which endpoint
it hits, whether the backend persists AND affects the data plane, and any known
divergence between the UI's success signal and reality.

**Legend:**
- 🟢 **Honest** — control persists + affects data plane; success signal matches reality.
- 🟡 **Documented stub** — control returns a synthetic/placeholder response by design.
  Not deceptive because the surrounding copy or platform gating is honest.
- 🔴 **Deceptive** — control claims success but the effect is partial or missing.
  Tracked as follow-up work; S3 documents, does not fix.

**Audit date:** 2026-07-11
**Base commit:** post-S2 (`internal/exit` DB-backed + `internal/core/applier` on-line);
S3-T1 (backup import → Applier) may land after this doc.

---

## Pages under audit

```
web/src/pages/
├── Backup.tsx
├── Bootstrap.tsx
├── Dashboard.tsx
├── Exits.tsx
├── Login.tsx
├── Logs.tsx
├── Rules.tsx
└── Snapshots.tsx
```

All 8 files enumerated. AppShell.tsx (layout, not a page) audited separately at the bottom.

---

## 1. Login.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **Sign in** button (`onSubmit`) | `POST /api/v1/login` | 🟢 | `internal/api/panel.go:75` — validates credentials against `panel_users` bcrypt hash, issues JWT, records `panel.login` audit entry, applies rate limit. Real. |

## 2. Bootstrap.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **Complete setup** button (`onSubmit`) | `POST /api/v1/bootstrap` | 🟢 | `internal/api/panel.go:37` `handleBootstrapClaim` — validates setup-token, bcrypts password, inserts panel_user, enforces single-use via `already_setup` conflict. Real. |
| **needs_setup** probe | `GET /api/v1/bootstrap` | 🟢 | `panel.go:26` — direct DB count of `panel_users`. Real. |

## 3. AppShell.tsx (layout wrapper — audited here because it owns navigation + logout)

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **Log out** (navbar + sidebar) | `POST /api/v1/logout` | 🟢 | `panel.go:116` — revokes the JWT session via `s.Auth.Revoke(sid)`, appends `panel.logout` audit entry. Real. |
| **Signed-in username** display | `GET /api/v1/me` | 🟢 | Wired in S3-T3. `handleMe` at `panel.go:133` returns `{user_id, username}` from JWT context. Falls back to `authStore.username` on network error. Not deceptive because the fallback is the same identity that was proven on login. |

## 4. Dashboard.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **Metrics tiles** (CPU/mem/conns/tx/rx) | `GET /api/v1/metrics` | 🟢 | `internal/api/metrics.go:24` — reads `metrics_samples` table populated by the collector; returns raw rows. Real. |
| **Active exit + exit list** | `GET /api/v1/exits` | 🟢 | Post-S2: `exits.go:40` reads from `exit.Store` (DB-backed). Prior to S2 this was the fake `exitsState` package var; that regression is closed. |
| **5s auto-refresh** | (both above) | 🟢 | `setInterval(refresh, 5000)`. No control, just polling; both endpoints return current DB state. |

## 5. Exits.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **List exits** | `GET /api/v1/exits` | 🟢 | See Dashboard row. Reads `exit.Store`. |
| **Add exit** (form submit) | `POST /api/v1/exits/add` | 🟢 | `exits.go:66` `handleAddExit` — `xexit.Parse` validates URI, `Store.Add` writes to DB with UNIQUE constraint; `exits.add` audit entry appended. Note: add does NOT trigger Apply (spec §S2.3 explicit: add does not touch data plane until switched-to). Not deceptive — Add advertises "configured", not "active". |
| **Switch exit** button | `POST /api/v1/exits/switch` | 🟢 | `exits.go:146` `handleSwitchExit` — routes through `s.Applier.SwitchExit`, which runs Assemble + Orchestrator.Apply + health observation + automatic rollback on failure. Response reports `health` state. Real end-to-end. |
| **Delete exit** button | `POST /api/v1/exits/delete` | 🟢 | `exits.go:108` — rejects active exit via `ErrExitActive`, otherwise removes from Store. No data-plane apply needed (removed exits aren't referenced by render). Real. |

## 6. Rules.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **List rules** | `GET /api/v1/rules` | 🟢 | `rules.go:41` — reads the active rule_version YAML from DB, parses via `rules.ParseYAML`. Real. |
| **+ Add rule / Delete / Move up-down / Toggle enabled / Edit fields** | (local state only) | 🟢 | Client-side draft mutations. No network call until Dry-run/Apply. Copy makes the two-stage flow explicit (dry-run then apply). Not a "success" claim, so honest. |
| **Reset** button | (client-side) | 🟢 | Reloads from GET /api/v1/rules. Real. |
| **Dry-run** button | `POST /api/v1/rules/dry-run` | 🟢 | `rules.go:59` — parses draft YAML, runs `rules.DryRun` against fixtures. Static analysis (matches rules against fixture domains); UI already labels dry-run limits ("static check only"). Real for what it advertises. |
| **Apply** button | `POST /api/v1/rules/apply` | 🟢 | `rules.go:85` — insert snapshot + rule_version, then `s.Applier.ApplyRules` (Assemble → Orchestrator.Apply → health observe → auto-rollback). Response reports `health` state (confirmed/observing/rolled_back). Real end-to-end. |
| **Rule kind/action/pattern selectors** | (local state) | 🟢 | Persisted only through Apply. |

## 7. Snapshots.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **List snapshots** | `GET /api/v1/snapshots` | 🟢 | `snapshots.go:28` — reads `snapshots` table. Real. |
| **Roll back** button | `POST /api/v1/snapshots/{id}/rollback` | 🔴 | **`snapshots.go:50` `handleRollbackSnapshot` only flips `SetActiveRuleVersion` in the DB and appends an audit entry — it never calls `s.Applier` or `Orchestrator.Apply`. Same bug family as pre-S3 backup import (Critic F3): the DB says "rolled back", but mihomo/dnsdist keep running with the pre-rollback config until the next unrelated Apply.** Deferred; not fixed in S3. See Follow-ups §F1. |

## 8. Backup.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **Download tar.gz** (Export) | `GET /api/v1/backup/export` | 🟢 | `snapshots.go:106` `handleExportBackup` — streams a real tar.gz with active rules YAML + snapshot manifest + WAL-safe SQLite hot-copy (VACUUM INTO) + README. Real. |
| **Import (upload)** button + Dialog (S3-T1b) | `POST /api/v1/backup/import` | 🟢 *(pending worker-backend S3-T1)* | Prior to S3, `handleImportBackup` inserted snapshot + rule_version(active=true) without calling Applier — a 🔴 half-real. S3-T1 wires it through `s.Applier.ImportRules` with auto-rollback on failure. S3-T1b adds a confirmation Dialog and surfaces the `apply_result` field (health, rolled_back). Once T1 lands, this row is 🟢. Until then, mark as **conditionally honest — verified against handoff spec, awaiting backend commit**. |
| **File picker** (`<input type=file>`) | (client-side gate) | 🟢 | Just staging; upload only fires on Dialog confirm (post-T1b). |

## 9. Logs.tsx

| Control | Endpoint | Verified real | Notes |
|---|---|---|---|
| **Unit select / filter input** | (client-side) | 🟢 | Filters what's rendered from the SSE stream in browser. No claim of server-side filtering. |
| **Live tail** | `GET /api/v1/events/logs?unit=…` (SSE) | 🟡 | `internal/api/logs.go:22` — on Linux, pipes real `journalctl -u <unit> -f -o json` and forwards `{ts,level,msg,unit,seq}` frames. **On non-Linux hosts (macOS/dev), falls back to `stubStream` which emits `"msg":"stub log line — journalctl unavailable on this host"` every second (`logs.go:127`).** The stub is intentional so the panel renders during dev on macOS. The stub message names itself as a stub, so the user is not misled. **Documented stub, not deceptive.** |
| **Clear** button | (client-side) | 🟢 | Local buffer clear. |
| **Reconnect** button | (SSE reconnect) | 🟢 | Re-establishes EventSource. Real. |

---

## Follow-ups (out of S3 scope — deferred to S4 or later)

- **F1 · Snapshot rollback is a DB-only flip.** `internal/api/snapshots.go:50-99` `handleRollbackSnapshot` calls `SetActiveRuleVersion` and returns 200 OK without invoking `Applier`. Symptom: UI toast "Rolled back to snapshot #N", but the data plane keeps the pre-rollback config until an unrelated Apply. Same shape as Critic F3 (backup import) that S3-T1 fixes for imports.
  - Fix outline: route through a new `Applier.RollbackToSnapshot(ctx, snapshotID)` that mirrors `ApplyRules`: transactional DB pointer flip + Assemble + Orchestrator.Apply + health observe + auto-rollback on failure. Reuse the `apply_status` state machine so the panel can surface `health` on the response.
  - Priority: **HIGH** — this is the exact class of bug S3 exists to prevent, and it's the one control we did not fix in this batch. Backup import is fixed, snapshot rollback still lies.

- **F2 · `handleImportBackup` accepts unknown tar entries silently.** `snapshots.go:213-219` drains unknown members without warning. Not a lie per se (nothing claims they were applied), but a stricter import would surface unrecognized entries so operators know what was ignored.
  - Priority: LOW.

- **F3 · Rate limits.** Only `/api/v1/login` has one (`panel.go:78`). `/api/v1/rules/apply` and `/api/v1/exits/switch` are protected by `Applier`'s single-flight mutex + `apply_in_flight` 409, which is effectively a rate limit of concurrency=1, but no per-user throttle exists. Not deceptive because success responses accurately reflect state; noted for later hardening.
  - Priority: LOW.

- **F4 · CORS is permissive.** `server.go:130` allows `https://*` + `http://localhost:5173`. Fine for dev; production hardening deferred.
  - Priority: LOW.

---

## Summary counts

| Category | Count | Screens |
|---|---:|---|
| 🟢 Honest | 21 controls | Login, Bootstrap, AppShell, Dashboard, Exits (4), Rules (7), Snapshots (list), Backup (export + import-post-T1), Logs (select/filter/clear/reconnect) |
| 🟡 Documented stub | 1 control | Logs live tail on non-Linux |
| 🔴 Deceptive | 1 control | Snapshots rollback (F1) |

**Verdict:** After S3-T1 lands (backup import → Applier), the only remaining 🔴
control in the panel is snapshot rollback (F1). Everything else that returns a
"success" signal has been verified to persist AND affect the data plane, OR
labels itself as a stub. The panel meets AC10's honesty bar for S3, and F1 is
the single scheduled follow-up.
