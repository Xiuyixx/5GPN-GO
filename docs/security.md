# Security posture

- **Single-user, single-machine** — the daemon assumes one operator on one VPS.
- **Panel auth** — bcrypt password (cost=12) + RS256 JWT + server-side `panel_sessions.revoked_at` gate on every request.
- **Rate limiting** — login attempts capped at 5/minute per IP; 3 failures locks the source IP for 15 minutes.
- **TG Bot** — `chat_id` whitelist mandatory; empty list refuses to start the bot; unauthorized requests are logged but not answered.
- **TLS** — reuse the existing `renew-hook.sh` ACME/Let's Encrypt flow. Do not add a second certificate manager (double-manager risk).
- **install.sh** — bootstrap script downloads `5gpn-installer` with mandatory sha256 verification (no unsigned curl-pipe).
- **Backup tar** — export contains plaintext TG token and password hashes; the panel prompts before download and offers an optional `age` passphrase.
- **Dry-run** — runs as a static configuration validator (no second data-plane instance), keeping the low-memory-VPS envelope.
- **SQLite** — `5gpn.db` is `chmod 0600` and owned by the DynamicUser assigned to `5gpn.service`. `PRAGMA integrity_check` is invoked before each backup export.
- **journalctl access** — the daemon reads other units' journals via the `systemd-journal` group; no `CAP_SYSLOG`.

See [tech-debt.md](./tech-debt.md) for accepted trade-offs.
