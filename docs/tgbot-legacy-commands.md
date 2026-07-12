# Legacy `tgbot.py` command & callback inventory

**Source:** local-only `5GPN-X/tgbot.py` when that excluded nested repository is
present (1,408 lines at the audit baseline). It is not shipped by this repo.
**Purpose:** historical migration inventory and an explicit record of which
verbs are, and are not, implemented by the current Go bot.

Legacy dispatch is hand-rolled: no `python-telegram-bot` framework, no
`CommandHandler` / `CallbackQueryHandler` registrations. The main loop
receives an update dict and branches on:

- `text.startswith("/")` for slash commands
- `data ==` or `data.startswith(...)` for inline-keyboard callback payloads
- `PENDING[chat_id]["action"]` for conversational follow-ups (multi-turn
  flows started by a callback that arm state and consume the *next* text)

Enumeration below is drawn from lines 1082-1350.

---

## 1. Slash commands

| Command | Auth | Handler | Behavior |
|---------|------|---------|----------|
| `/id` | anyone | inline | Replies with caller's numeric chat id (bootstrap helper for populating `TG_ADMIN_IDS`). |
| `/cancel` | admin | inline | Clears `PENDING[chat_id]`, returns to main menu. |
| `/start` | admin | inline | Opens main menu keyboard. |
| `/menu` | admin | inline | Alias for `/start`. |
| `/status` | admin | `op_status()` | System status: exit, domain, DNS, service health, `/proc` metrics. |
| `/exits` | admin | `exits_overview_text` + `exits_menu()` | Async fetch of current exit info + connectivity probe; keyboard of exit switches. |
| `/rules` | admin | `rules_menu()` | Opens the smart-routing rules keyboard. |
| _anything else_ | admin | fallback | "未知命令。发送 /menu 打开操作面板。" |

Unauthorized: any command except `/id` replies "⛔ 未授权。把你的 ID 加入 TG_ADMIN_IDS 后重试。".

---

## 2. Callback data — menu navigation

| `data` | Renders |
|--------|---------|
| `menu:main` | Main menu keyboard |
| `menu:rules` | Smart-routing rules keyboard |
| `menu:policy` | Policy-category-to-exit mapping menu |
| `menu:exits` | Async exits-overview text + `exits_menu()` |
| `menu:exits_del` | Exit-deletion keyboard (`exits_del_menu()`) |
| `menu:dot` | DoT status text + `dot_menu()` |
| `menu:logs` | Service picker for log viewing |

---

## 3. Callback data — conversational starts (arms PENDING)

Each of these sets `PENDING[chat_id] = {"action": <key>}` and edits the
current message to a prompt. The next plain-text message from the same
chat is consumed by the matching PENDING handler.

| `data` | PENDING action | Follow-up input | Follow-up op |
|--------|----------------|-----------------|--------------|
| `rules:set` | `rules_set` | Whole ruleset paste | `op_set_rules(text)` |
| `rules:add` | `rules_add` | One rule line | `op_add_rule(line)` |
| `rules:addset` | `rules_addset` | `<url> <target>` | `op_add_ruleset(text)` |
| `rules:del` | `rules_del` | Rule index number | `op_del_rule(num)` |
| `exit_add` | `add_exit_link` | Node link (or `<name> <link>`) | `op_add_exit(name, config)` via `parse_add_exit_input` |
| `dot:domain` | `dot_domain` | New DoT domain | `op_set_dot_domain(domain)` |
| `dot:dns_remote` | `dot_dns_remote` | Overseas DNS list | `op_set_dns("remote", text)` |
| `dot:dns_local` | `dot_dns_local` | Local DNS list | `op_set_dns("local", text)` |
| `dot:force_domain` | _immediate_ | (uses `LAST_FAILED_DOT_DOMAIN[chat_id]`) | `op_force_set_dot_domain(domain)` |

`PENDING` state also carries scratch data (e.g. parsed link name) between
turns.

---

## 4. Callback data — read-only views

| `data` | Renders |
|--------|---------|
| `rules:show` | Full ruleset dump via `op_show_rules()` |
| `act:status` | Same as `/status`, back-button keyboard |
| `logs:<service>` | Async `journalctl -u <service>` via `op_logs(svc)`, mono-formatted |
| `exits:check` | Async connectivity probe over every configured exit (`op_check_exits`) |

Legacy service names surfaced under `logs:` (from `services_menu("logs")`):
`proxy-gateway-router`, `dnsdist`, `mihomo`, `sniproxy`, `wa-shim`, `5gpn`,
`nginx` — filtered at runtime by `_read_available_services()`.

---

## 5. Callback data — action verbs (⏳ then result)

| `data` | Op | Notes |
|--------|----|-------|
| `act:update_rules` | `op_update_rules()` | Fetch every remote ruleset, re-render mihomo. |
| `act:renew` | `op_renew_cert()` | certbot renew + dnsdist reload. |
| `act:restart` | `op_restart_services()` | `systemctl restart` the whole stack. |
| `act:ios` | `edit_ios_async(cb, chat_id)` | Generate iOS DoT profile QR code, upload as photo. |
| `rules:enable` | `op_set_exit("smart")` | Enable smart-routing mode. |
| `exit:<name>` | `op_set_exit(name)` | Switch current exit to `<name>`. |
| `exitdel:<name>` | `op_del_exit(name)` | Remove exit `<name>`. |
| `pol:<idx>` | opens `policy_targets_menu(idx)` | Two-step; category selection. |
| `ps:<idx>:<target>` | `op_set_policy(cat, target)` | Two-step; commits the mapping. |

`<name>` and `<idx>` / `<target>` are validated inline before dispatch;
invalid indexes bounce back to `policy_menu()`.

---

## 6. Current Go bot coverage

| Legacy verb | New surface |
|-------------|-------------|
| `/id` | Implemented and public; returns only the caller's own chat ID. |
| `/start`, `/menu`, `/help` | Implemented. `/menu` has inline buttons for current read-only commands. |
| `/status`, `/exits`, `/rules` | Implemented. `/exits add|switch|del` provides the current mutation subset. |
| `/a`, `/dev`, `/ip`, `/json`, `/loadavg`, `/meminfo`, `/nf_conntrack_count`, `/route`, `/stat`, `/tcp`, `/uptime`, `/wireguard`, `/www` | Implemented diagnostics. `/ip` reports direct host egress, not a selected-exit proof. |
| Legacy rule editing/ruleset sync | Not implemented in the bot; use the panel Rules surface. |
| Legacy DoT mutation/certificate renewal | Not implemented in the bot. |
| Legacy service restart/log viewing | Not implemented in the bot; the panel exposes separate restart and Logs surfaces. |
| Legacy iOS QR upload | Not implemented; the panel serves `/ios-dot.mobileconfig`. |
| Legacy policy-map and exit connectivity probe | Not implemented in the bot. |

The Go bot therefore does **not** have full legacy command parity. The
authoritative command list is `internal/tgbot/handlers.go:DispatchCommands`;
unknown slash commands return the `/menu` hint rather than invoking a hidden
handler.

---

## 7. Auth model differences

- Legacy: `TG_ADMIN_IDS` env, comma-separated numeric ids. `/id` was public.
- Current Go bot: YAML plus SQLite-layered `tgbot.admin_chat_ids`; there is no
  installer `--admin-chat-id` flag. A token with an empty whitelist is a hard
  bot-start refusal. `/id` is public for enrollment; every other command and
  callback requires the whitelist.
- Rejected messages are silent and append audit action
  `tgbot.unauthorized`, actor `tgbot:<username>`, target `<chat_id>`.

---

## 8. Enumerated `op_*` functions (with line numbers)

For reviewers cross-checking behavior:

| Fn | Line | Purpose |
|----|------|---------|
| `op_status` | 523 | System + exit + service snapshot |
| `op_set_exit` | 560 | Switch active exit |
| `exits_overview_text` | 577 | Exits list w/ IP probe |
| `op_add_exit` | 593 | Add exit from link |
| `op_del_exit` | 675 | Remove exit |
| `op_update_rules` | 684 | Refresh remote rulesets |
| `op_renew_cert` | 698 | certbot renew + dnsdist reload |
| `op_dot_status` | 705 | DoT domain + DNS status |
| `op_set_dot_domain` | 721 | Change DoT domain (with cert re-issue) |
| `op_force_set_dot_domain` | 737 | Change DoT domain without cert re-issue |
| `op_set_dns` | 772 | Update remote/local DNS list |
| `op_restart_services` | 794 | Restart the whole stack |
| `op_logs` | 808 | `journalctl -u <svc>` |
| `op_show_rules` | 833 | Dump ruleset |
| `op_set_rules` | 841 | Replace ruleset |
| `op_add_rule` | 853 | Append one rule |
| `op_add_ruleset` | 862 | Import remote ruleset |
| `op_del_rule` | 878 | Remove one rule by index |
| `op_set_policy` | 908 | Set category → exit mapping |
| `op_check_exits` | 920 | Connectivity probe |
| `op_ios_send` | 949 | iOS profile QR |

---

Last audited against `5GPN-X/tgbot.py` at the pre-M0 subtree snapshot.
