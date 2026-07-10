// Package config defines the YAML schema for /etc/5gpn/config.yaml and
// provides loading + validation. This is the single source of truth from
// which dnsdist.conf, mihomo.yaml, and sniproxy.conf are rendered.
package config

import "time"

// Config is the root document.
type Config struct {
	Server ServerConfig `yaml:"server" validate:"required"`
	Panel  PanelConfig  `yaml:"panel"  validate:"required"`
	DNS    DNSConfig    `yaml:"dns"`
	Proxy  ProxyConfig  `yaml:"proxy"`
	Exits  []ExitConfig `yaml:"exits"  validate:"min=1,dive"`
	Rules  RulesConfig  `yaml:"rules"`
	TGBot  TGBotConfig  `yaml:"tgbot"`
	IOS    IOSConfig    `yaml:"ios"`
	LowMem LowMemConfig `yaml:"lowmem"`
}

type ServerConfig struct {
	Domain    string    `yaml:"domain"     validate:"required,hostname"`
	PanelPort int       `yaml:"panel_port" validate:"required,min=1,max=65535"`
	PanelBind string    `yaml:"panel_bind" validate:"required"`
	TLS       TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type PanelConfig struct {
	SessionTTL time.Duration   `yaml:"session_ttl" validate:"required"`
	RateLimit  RateLimitConfig `yaml:"rate_limit"`
}

type RateLimitConfig struct {
	LoginPerMinute int `yaml:"login_per_minute" validate:"min=1"`
	LockoutMinutes int `yaml:"lockout_minutes"  validate:"min=1"`
}

type DNSConfig struct {
	DoTPort         int      `yaml:"dot_port" validate:"omitempty,min=1,max=65535"`
	Upstreams       []string `yaml:"upstreams"`
	ChinaListSource string   `yaml:"chinalist_source"`
}

type ProxyConfig struct {
	SniProxy SniProxyConfig `yaml:"sniproxy"`
	WAShim   WAShimConfig   `yaml:"wa_shim"`
	QUIC     QUICConfig     `yaml:"quic"`
}

type SniProxyConfig struct {
	ListenHTTP    int `yaml:"listen_http"    validate:"omitempty,min=1,max=65535"`
	LoopbackHTTPS int `yaml:"loopback_https" validate:"omitempty,min=1,max=65535"`
}

// WAShimConfig mirrors 5GPN-X/wa-shim.py env-var contract 1:1.
type WAShimConfig struct {
	Listen         string        `yaml:"listen"          validate:"required"`
	Port           int           `yaml:"port"            validate:"required,min=1,max=65535"`
	Backend        string        `yaml:"backend"         validate:"required"`
	WAHost         string        `yaml:"wa_host"         validate:"required"`
	AllowCIDR      []string      `yaml:"allow_cidr"      validate:"min=1,dive,cidr"`
	PeekTimeout    time.Duration `yaml:"peek_timeout"`
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
	DNSTTL         time.Duration `yaml:"dns_ttl"`
	MaxConn        int           `yaml:"max_conn"        validate:"min=1"`
}

type QUICConfig struct {
	Listen string `yaml:"listen"`
}

type ExitConfig struct {
	ID       string         `yaml:"id"       validate:"required,alphanum"`
	Protocol string         `yaml:"protocol" validate:"required,oneof=direct wireguard socks5 socks5h ss ss2022 vmess trojan vless hysteria2 tuic anytls http https"`
	Config   map[string]any `yaml:"config"`
}

type RulesConfig struct {
	Sources      []RuleSource `yaml:"sources"       validate:"dive"`
	DefaultsFile string       `yaml:"defaults_file"`
}

type RuleSource struct {
	URL  string `yaml:"url"  validate:"required,url"`
	Kind string `yaml:"kind" validate:"required,oneof=mrs clash text"`
	Cron string `yaml:"cron"`
}

type TGBotConfig struct {
	Token        string  `yaml:"token"`
	AdminChatIDs []int64 `yaml:"admin_chat_ids" validate:"required_with=Token,dive,ne=0"`
}

type IOSConfig struct {
	DoTDomain   string `yaml:"dot_domain"`
	ProfileUUID string `yaml:"profile_uuid"`
	HTTPPort    int    `yaml:"http_port" validate:"omitempty,min=1,max=65535"`
}

type LowMemConfig struct {
	AutoDetectBelowMB int             `yaml:"auto_detect_below_mb"`
	HardLimits        LowMemHardLimit `yaml:"hard_limits"`
}

type LowMemHardLimit struct {
	GoMaxProcs    int    `yaml:"go_max_procs"`
	MihomoMaxRAM  string `yaml:"mihomo_max_ram"`
}
