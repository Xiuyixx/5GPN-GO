# 5gpn systemd units

- `5gpn.service` is a reference unit for image/package builders. The Go installer renders the canonical production unit from `internal/installer.UnitTemplate`.
- iOS profiles are served by the panel at the operator-gated public HTTPS endpoint `/ios-dot.mobileconfig`. The former plaintext `:8111` socket units were removed because the daemon has no standalone `ios-http` subcommand and Apple OTA installation requires HTTPS.
- The reference unit's `Requires=` line names dnsdist and mihomo. This metadata does not make sniproxy optional: every systemd apply unconditionally runs `systemctl restart sniproxy`, so a working and controllable `sniproxy.service` is also an apply prerequisite.
- The orchestrator runs `systemctl reload dnsdist`, `systemctl reload mihomo`, and `systemctl restart sniproxy`. Its default post-reload probe checks only `systemctl is-active` for dnsdist and mihomo; it does not probe sniproxy, DNS answers, or the public egress IP. No apply-latency or outage bound is currently guaranteed.
- The reference unit does not solve authorization for those privileged reloads. Production packaging needs a narrow privileged helper/policy; granting the whole daemon root is not supported.
