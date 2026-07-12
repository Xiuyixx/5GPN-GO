# Architecture

This document describes the current code, not the intended final deployment.

## Runtime layers

```text
React panel
    |
    v
internal/api (chi, bearer JWT, SSE)
    |
    +--> internal/settings / internal/db (SQLite control-plane state)
    +--> internal/core.Applier / internal/orchestrator
    +--> resolver, frontdoor, proxy, Telegram bot, iOS profile renderer
                         |
                         v
             rendered third-party configuration
                         |
                         v
             dnsdist / mihomo / sniproxy systemd units
```

`cmd/5gpn` embeds the built panel under the `embed` build tag. SQLite under the
data directory stores panel users and sessions, settings, exits, snapshots,
rule versions, audit rows, and metrics. The HS256 JWT secret is a separate
mode-`0600` `jwt.key` file in the data directory.

## Apply and snapshot model

Rule Dry-run validates the rule model and evaluates fixtures with the
in-process matcher. It does not execute mihomo or dnsdist config-check tools.

An apply creates an inactive `snapshots` row and a paired `rule_versions` row,
validates and builds the resolver table, renders the effective configuration,
then asks the selected orchestrator to apply it. With the systemd orchestrator,
the code snapshots the existing rendered files, writes replacements, runs
`systemctl reload dnsdist`, `systemctl reload mihomo`, and
`systemctl restart sniproxy`, and starts a health-observation phase. The active
database rule version and in-process resolver table advance only after the
health probe succeeds. The default probe only runs `systemctl is-active` for
dnsdist and mihomo; it does not check sniproxy, issue a DNS query, or verify the
public egress IP. A failed probe makes the orchestrator attempt to restore the
previous rendered files and reload/restart all three units.

Each Applier-driven change has a durable `apply_status` lifecycle. `submitted`
is the non-terminal observation state; the terminal states are `confirmed`
(the candidate was committed), `rolled_back` (the prior state was restored),
and `failed` (the operation was not confirmed and a complete rollback could
not be confirmed). The API can return `202 Accepted` with an `apply_id`, which
clients poll until one of those terminal states is stored.

Snapshots are database rule-version records. The schema retains a legacy
`tarball_path` column, but current rule apply writes it empty and does not create
a tarball per snapshot. Snapshot rollback sends the paired rule version through
the same Applier path.

## Orchestrator modes and privilege boundary

`--orchestrator=auto` selects systemd on Linux when `systemctl` is present and
NoOp otherwise. NoOp is for development/tests and does not mutate the external
data plane.

The installed service runs as an unprivileged user with
`NoNewPrivileges=yes`, `ProtectSystem=strict`, and only its data directory
writable. The systemd orchestrator and several Settings controls still write
system paths or invoke `systemctl`. No privileged helper or narrowly scoped
elevation policy is shipped. Consequently, production apply, restart, and MTG
service controls are not operational under the canonical installed unit until
that boundary is implemented.

The systemd apply path unconditionally restarts `sniproxy`; a working
`sniproxy.service` is therefore an apply prerequisite even though the legacy
reference unit under `deploy/systemd/` declares only dnsdist and mihomo in its
`Requires=` line. The canonical installer unit does not order any of the three
external units, so operators must provision them separately.

## Backup model

Backup export performs `PRAGMA integrity_check` and creates a plaintext tar.gz
containing `rules/active.yaml`, `snapshots/manifest.json`, a WAL-safe
`db/5gpn.db` hot-copy, and `README.txt`. Panel import validates the complete
archive but applies `rules/active.yaml` only. The database and manifest are
included for administrator-led offline recovery; importing them through the
panel is intentionally unsupported.

## Authentication and clients

The panel uses HS256 bearer JWTs backed by revocable SQLite session rows. The
React client persists the token in `localStorage` and sends it in the
`Authorization` header, including for the fetch-based SSE stream. This is not
an HttpOnly-cookie design.

The generated iOS encrypted-DNS profile is unsigned XML. Its primary payload
uses DoH at `https://<domain>/dns-query` and its optional fallback uses DoT.
The current preflight connects only to `127.0.0.1:853`, disables certificate
verification for that loopback probe, and sends a sample DNS query. It does not
validate the primary public DoH endpoint or reachability from an iOS device.

## Path B forwarding boundary

The in-process TCP SNI and UDP QUIC forwarders parse a destination and dial its
public address directly. They do not connect through mihomo's loopback proxy,
set a routing mark, or install policy-routing rules. Selecting an active exit
therefore changes rendered mihomo state but does not put these forwarders on
that exit. Treat Path B as experimental destination forwarding until an
active-exit transport and an end-to-end egress-IP test are implemented.

## Update boundary

The update API can query release metadata and checksums. `POST
/api/v1/update/apply` is deliberately disabled and returns an error requiring
an external installer or privileged supervisor. A safe updater must remain
alive across daemon replacement/restart so it can verify health and restore the
previous binary.
