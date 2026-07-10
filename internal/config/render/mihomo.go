package render

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// MihomoRenderer materializes mihomo config.yaml.
//
// The output is a minimal-but-valid mihomo document: rule mode + TUN off
// (5gpn's mihomo runs behind the sniproxy/wa-shim data path, not as a
// TUN gateway on the daemon's box), a proxies list built from
// cfg.Exits, and a MATCH → active-exit rule. Real 10-protocol config
// synthesis for each Exit is delegated to internal/exit.Parse when the
// panel adds them; here we only re-emit the stored map.
type MihomoRenderer struct{}

// Render writes the mihomo YAML equivalent of cfg to w.
func (MihomoRenderer) Render(cfg *config.Config, w io.Writer) error {
	if cfg == nil {
		return fmt.Errorf("mihomo: nil config")
	}
	proxies := make([]map[string]any, 0, len(cfg.Exits))
	names := make([]string, 0, len(cfg.Exits))
	for _, ex := range cfg.Exits {
		p := map[string]any{"name": ex.ID, "type": ex.Protocol}
		for k, v := range ex.Config {
			p[k] = v
		}
		proxies = append(proxies, p)
		names = append(names, ex.ID)
	}
	// mihomo rejects proxies: [] documents; inject a placeholder direct
	// entry so an empty exits list still yields a working config.
	if len(proxies) == 0 {
		proxies = append(proxies, map[string]any{"name": "direct", "type": "direct"})
		names = append(names, "direct")
	}

	doc := map[string]any{
		"mode":         "rule",
		"log-level":    "warning",
		"ipv6":         false,
		"external-controller": "127.0.0.1:9090",
		"proxies":      proxies,
		"proxy-groups": []map[string]any{
			{"name": "PROXY", "type": "select", "proxies": names},
		},
		"rules": []string{
			"MATCH,PROXY",
		},
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return enc.Close()
}
