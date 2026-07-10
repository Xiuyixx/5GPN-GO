# Architecture

## Layers

```
┌────────────────────────────────────────────────────────────────────┐
│  React 19 + Tailwind v4 + Catalyst UI Kit  (single-page panel)     │
├────────────────────────────────────────────────────────────────────┤
│  internal/api  (chi router + JWT auth + SSE events)                │
├────────────────────────────────────────────────────────────────────┤
│  internal/rules  internal/exit  internal/dns  internal/proxy       │
│  internal/tgbot  internal/ios   internal/updater                    │
├────────────────────────────────────────────────────────────────────┤
│  internal/orchestrator + internal/config/render                    │
│  ⇩ renders  ⇩ reloads via systemd + REST                            │
├────────────────────────────────────────────────────────────────────┤
│  dnsdist   mihomo (1.19.28)   sniproxy                              │
└────────────────────────────────────────────────────────────────────┘
                     ⇩ Persistence
             internal/db (SQLite 5gpn.db)
```

The daemon (`cmd/5gpn`) embeds the panel bundle via `go:embed all:web/dist` under the `embed` build tag. State — snapshots, rule versions, audit log, panel sessions, bot sessions, metric ring — lives in a single SQLite file under `/var/lib/5gpn/`.

## Data-plane discipline

- Three-party components (dnsdist, mihomo, sniproxy) are never re-implemented.
- Their configuration files are always rendered from `configs/*.yaml`; hand-editing them is a bug.
- Runtime changes travel through the pipeline: `parse → validate → dry-run → snapshot → apply → auto-rollback`.

## Two entrypoints

- Web panel (React) — primary UI.
- TG Bot — mirror of the panel via `chat_id` whitelist; shares `internal/api` handlers.

## Milestones

See [milestones.md](./milestones.md).
