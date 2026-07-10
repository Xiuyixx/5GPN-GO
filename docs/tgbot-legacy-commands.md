# TG Bot legacy command surface

Auto-generated from 5GPN-X/tgbot.py. M2 will refine this into a checklist per handler.

## Slash commands

```
1082:    if text.startswith("/id"):
1098:        if text.startswith(("/start", "/menu")):
1100:        elif text.startswith("/status"):
1102:        elif text.startswith("/exits"):
1104:        elif text.startswith("/rules"):
```

## Callback data

```
```

## Handler entry points

```
81:def tg(method, **params):
106:def background(fn, *args):
116:def answer_callback_async(cb_id):
133:def send(chat_id, text, keyboard=None, mono=False):
155:def _chunks(text, size):
163:def edit(cb, text, keyboard=None, mono=False):
188:def _busy_key_from_cb(cb):
195:def edit_async(cb, text_fn, keyboard=None, mono=False):
208:def edit_ios_async(cb, chat_id):
225:def send_async(chat_id, text_fn, keyboard=None, mono=False, keyboard_fn=None):
234:def back_kb(target="menu:main", label="« 返回"):
238:def send_photo(chat_id, path, caption=""):
271:def pre(text):
282:def run(argv, timeout=120, inp=None):
304:def validate_mgmt_path():
314:def _strip_ansi(s):
318:def run2(argv, timeout=120, inp=None):
332:def _reason(out, n=4):
341:def _tail_output(out, n=20, limit=1800):
347:def _exit_ip():
369:def _read_file(path):
377:def _parse_env(path):
387:def _is_active(unit):
400:def _read_int(path, default=0):
407:def _cpu_idle_total():
416:def _default_iface():
427:def _iface_bytes(iface):
442:def _established():
454:def _fmt_bytes(n):
463:def system_metrics():
523:def op_status():
560:def op_set_exit(name):
577:def exits_overview_text():
593:def op_add_exit(name, payload):
610:def b64decode_text(s):
620:def clean_exit_name(name):
629:def unique_exit_name(name):
644:def exit_name_from_uri(uri):
657:def parse_add_exit_input(payload):
675:def op_del_exit(name):
684:def op_update_rules():
698:def op_renew_cert():
705:def op_dot_status():
721:def op_set_dot_domain(domain):
737:def op_force_set_dot_domain(domain):
749:def force_dot_domain_kb():
756:def _dns_arg(text):
763:def current_remote_dns():
768:def current_local_dns():
772:def op_set_dns(kind, text):
794:def op_restart_services():
808:def op_logs(svc):
824:def _rule_entries():
833:def op_show_rules():
841:def op_set_rules(text):
853:def op_add_rule(line):
862:def op_add_ruleset(text):
878:def op_del_rule(num):
898:def _policy_map():
908:def op_set_policy(cat, target):
916:def _targets():
920:def op_check_exits():
930:def parse_exit_names():
949:def op_ios_send(chat_id):
977:def main_menu():
990:def rules_menu():
1003:def policy_menu():
1014:def policy_targets_menu(idx):
1029:def exits_menu():
1040:def exits_del_menu():
1052:def dot_menu():
1062:def services_menu(prefix):
1071:def authorized(uid):
1075:def handle_message(msg):
1167:def handle_callback(cb):
1336:def set_commands():
1376:def main():
```
