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
ExecStart={{.BinaryPath}} --config {{.ConfigPath}} --data {{.DataDir}}
Restart=on-failure
RestartSec=3s
TimeoutStopSec=15s

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths={{.DataDir}} {{.ConfigDir}}
PrivateTmp=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
`

// DefaultConfigTemplate is what fresh installs get. Values come straight
// from cfg schema defaults — the interview happens later inside the panel.
const DefaultConfigTemplate = `# 5GPN panel config — written by 5gpn-installer install.
# Full schema: internal/config/schema.go
server:
  domain: "${5GPN_DOMAIN:-panel.local}"
  panel_bind: "127.0.0.1"
  panel_port: 8443
  tls:
    cert: ""
    key: ""

panel:
  session_ttl: 8h
  rate_limit:
    login_per_minute: 5
    lockout_minutes: 15

tgbot:
  token: "${5GPN_TG_TOKEN:-}"
  admin_chat_ids: []

ios:
  profile_port: 8444
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
		"ConfigPath": env.ConfigPath(),
		"ConfigDir":  env.ConfigDir,
		"DataDir":    env.DataDir,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
