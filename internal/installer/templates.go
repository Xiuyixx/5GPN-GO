package installer

import (
	"bytes"
	"text/template"
)

// UnitTemplate is the canonical 5gpn.service. Hardened defaults come from
// the M2 systemd survey — NoNewPrivileges, ProtectSystem=strict, and a
// carve-out for state dirs. Kept small so operators can `systemctl edit`
// without wrestling with 40 lines of policy.
const UnitTemplate = `[Unit]
Description=5GPN personal gateway daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
# systemd-journal grants the daemon read access to the system journal so
# the panel's /api/v1/events/logs SSE can tail journalctl -u <unit>.
# Without it journalctl refuses with "No journal files were opened due
# to insufficient permissions." and the Logs page stays disconnected.
SupplementaryGroups=systemd-journal
ExecStart={{.BinaryPath}} --config {{.ConfigPath}} --data {{.DataDir}}
Restart=on-failure
RestartSec=3s
TimeoutStopSec=15s

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
# BinaryDir is included so panel-driven upgrades can rewrite the binary
# in place (blue-green swap in internal/updater). Without it, ProtectSystem
# would make /usr/local/bin read-only and one-click upgrade fails EROFS.
ReadWritePaths={{.DataDir}} {{.ConfigDir}} {{.BinaryDir}}
PrivateTmp=yes
# CAP_NET_ADMIN is needed by the DNS front-door's upstream socket to set
# SO_MARK=0 so its DoT egress bypasses the pgw_exit nftables rules that
# would otherwise route the resolver's own queries back through a proxy
# tunnel. CAP_NET_BIND_SERVICE lets the non-root daemon bind :443/:853/:53.
# Both must be Ambient (in addition to Bounding) for a non-root User to
# actually retain them at exec time.
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
# Four listeners × per-listener connection tables + certmagic + QUIC
# read buffers can chew through the 1024 default; 65536 gives ~16× headroom
# for the 1-core VPS profile the plan targets.
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

// DefaultConfigTemplate is what fresh installs get. Every required
// schema field (internal/config/schema.go) is populated with a working
// default so `config.Load` succeeds on first boot without operator
// intervention. Operators customize by editing /etc/5gpn/config.yaml
// or through the panel after bootstrap.
//
// Env-var expansion is `${VAR}` only (see internal/config/loader.go's
// envRe). Bash-style `${VAR:-default}` is NOT supported — the regex
// only accepts `[A-Z_][A-Z0-9_]*` between the braces, so anything else
// stays literal and pollutes the config. Hard-coded values below avoid
// that trap.
const DefaultConfigTemplate = `# 5GPN panel config — written by 5gpn-installer install.
# Full schema: internal/config/schema.go
#
# All required fields are pre-filled with safe defaults so the daemon
# validates and boots on first run. Change these before exposing the
# panel to the internet:
#   - server.domain: hostname the panel serves under (TLS SAN, iOS profile)
#   - server.panel_bind: 0.0.0.0 accepts external, 127.0.0.1 tunnel-only
#   - proxy.wa_shim.*: WA-shim listener; leave localhost-only unless used

server:
  domain: "panel.local"
  panel_bind: "0.0.0.0"
  panel_port: 8443
  tls:
    cert: ""
    key: ""

panel:
  session_ttl: 8h
  rate_limit:
    login_per_minute: 5
    lockout_minutes: 15

proxy:
  # WAShim mirrors 5GPN-X/wa-shim.py. Its fields carry required tags in
  # the config schema, so we ship working localhost-only defaults; if
  # you aren't routing WA traffic this listener is harmless (bound to
  # 127.0.0.1 with a 127.0.0.1/32 allow-list).
  wa_shim:
    listen: "127.0.0.1"
    port: 8447
    backend: "127.0.0.1:1080"
    wa_host: "www.apple.com"
    allow_cidr:
      - "127.0.0.1/32"
    max_conn: 100

tgbot:
  token: ""
  admin_chat_ids: []

ios:
  http_port: 8111
`

// Render fills UnitTemplate against the target Env.
func Render(unit string, env Env) ([]byte, error) {
	tpl, err := template.New("").Parse(unit)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]string{
		"User":       env.User,
		"Group":      env.Group,
		"BinaryPath": env.BinaryPath(),
		"BinaryDir":  filepathDir(env.BinaryPath()),
		"ConfigPath": env.ConfigPath(),
		"ConfigDir":  env.ConfigDir,
		"DataDir":    env.DataDir,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// filepathDir is a tiny wrapper around path/filepath.Dir kept local so
// templates.go does not sprout a new import for a one-liner. Called only
// from Render.
func filepathDir(p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[:i]
	}
	return "."
}

func lastSlash(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return i
		}
	}
	return -1
}
