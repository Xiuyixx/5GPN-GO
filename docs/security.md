# Security posture

This is the implemented posture as of 2026-07-13. It is not a production
certification.

## Implemented controls

- **Panel authentication:** passwords use bcrypt cost 12. Tokens are HS256 JWTs
  with expiry and a random data-directory secret; every authenticated request
  also checks the backing `panel_sessions` row so logout/revocation takes
  effect server-side.
- **Browser token storage:** the React auth store persists the bearer token in
  `localStorage` under `5gpn-auth`. This survives reloads but is readable by any
  successful same-origin script injection. Tokens are sent in the
  `Authorization` header; they are not HttpOnly cookies.
- **Login limiting:** limits are keyed by the transport peer address, not an
  unauthenticated forwarding header. Records have a TTL and global capacity;
  three failed logins trigger the configured lockout.
- **HTTP headers:** authenticated and public panel responses receive CSP,
  clickjacking, MIME-sniffing, referrer, permissions, and HTTPS HSTS headers.
- **TG Bot:** a configured bot requires at least one valid administrator chat
  ID. Empty credentials keep the bot disabled.
- **Database files:** startup enforces mode `0600` on the SQLite database and
  its WAL/SHM files. Ownership follows the service account selected by the
  installer. Backup export runs `PRAGMA integrity_check` first.
- **Logs:** the installed service joins `systemd-journal` for journal reads.
  The logs API accepts only an explicit unit allowlist and periodically
  revalidates the SSE session.
- **Untrusted outbound targets:** ruleset/list fetches and SNI/QUIC forwarding
  reject loopback, private, link-local, CGNAT, and other non-public resolved
  targets and connect to the checked concrete address.

## Explicit limitations

- **No runtime privileged helper:** the canonical service is unprivileged,
  uses `NoNewPrivileges=yes` and `ProtectSystem=strict`, and can write only its
  data directory. Runtime apply, self-restart, external component reload, and
  MTG unit management still require system-file writes or `systemctl`. No
  helper/polkit design currently grants those operations. They fail with a
  surfaced permission error under the installed unit.
- **Update apply disabled:** release checking remains available, but the daemon
  refuses in-process replacement. Use an external installer; a future
  supervisor must remain alive across restart and own rollback.
- **Checksum is not a signature:** `deploy/install.sh` downloads `SHA256SUMS`
  and binaries from the same GitHub release and compares SHA-256 values. This
  checks consistency/corruption, not publisher authenticity. There is no
  cosign, GPG, or other independent signature verification.
- **Plaintext backup:** the export is not encrypted and may contain credentials,
  password hashes, sessions, and other secrets through `db/5gpn.db`. No `age`
  encryption is implemented. Panel import restores only active rules; database
  and manifest recovery is a manual offline operation.
- **Unsigned iOS profile and mismatched preflight scope:** `.mobileconfig`
  output is unsigned XML; its primary payload uses public DoH while the enable
  preflight tests only the separate local `127.0.0.1:853` DoT endpoint with
  certificate verification disabled. It does not validate the profile's public
  DoH, firewall, certificate, carrier, or device path.
- **Shared-secret JWT:** HS256 is intentional in the current single-daemon
  model, but secret compromise permits token forgery. Rotation currently
  requires replacing `jwt.key` and invalidates existing tokens operationally.

See [tech-debt.md](./tech-debt.md) for release-blocking follow-ups.
