# 5gpn systemd units

- `5gpn.service` — main daemon. `DynamicUser=yes` creates an ephemeral user; state persists via `StateDirectory=5gpn` (mapped to `/var/lib/5gpn`), config via `ConfigurationDirectory=5gpn` (`/etc/5gpn`), logs via `LogsDirectory=5gpn` (`/var/log/5gpn`).
- `5gpn-ios.socket` + `5gpn-ios.service` — systemd socket activation for the iOS DoT profile HTTP endpoint on port 8111. The service is only spawned when a request arrives.
- `Requires=dnsdist.service mihomo.service` — the daemon depends on the third-party data-plane components; sniproxy is optional (started as needed).
- The service reloads dnsdist/mihomo through their own reload interfaces (dnsdist SIGHUP, mihomo REST API `PUT /configs?reload=true`); sniproxy is `systemctl restart`ed (no reload support, ≤ 1.5s outage).
