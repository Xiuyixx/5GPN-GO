package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// MihomoRenderer materializes mihomo config.yaml.
//
// The output is a minimal-but-valid mihomo document: rule mode + TUN off
// (5gpn's mihomo runs behind the sniproxy/wa-shim data path, not as a
// TUN gateway on the daemon's box), a proxies list built from
// cfg.Exits, and rules from cfg.EffectiveRules (sorted by Priority,
// MATCH always last). The mixed-port provides a hermetic measurement
// channel for AC3/AC6 traffic tests.
type MihomoRenderer struct{}

const defaultMihomoMixedPort = 7890

// Render writes the mihomo YAML equivalent of cfg to w.
func (MihomoRenderer) Render(cfg *config.Config, w io.Writer) error {
	if cfg == nil {
		return fmt.Errorf("mihomo: nil config")
	}

	mixedPort := cfg.Proxy.MihomoMixedPort
	if mixedPort == 0 {
		mixedPort = defaultMihomoMixedPort
	}

	proxies := make([]map[string]any, 0, len(cfg.Exits))
	names := make([]string, 0, len(cfg.Exits))
	for _, ex := range cfg.Exits {
		p := map[string]any{"name": ex.ID, "type": ex.Protocol}
		for k, v := range ex.Config {
			// Strip name/type from the stored config map to prevent clobbering
			// the fields we set explicitly above (xexit.Parse includes these).
			if k == "name" || k == "type" {
				continue
			}
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

	// Active exit = names[0]. Contract: cfg.Exits is sorted active-first by
	// core.Assemble via exit.Store.Records(); do not reorder downstream.
	activeExitName := names[0]

	// Build PROXY select group with active exit first.
	proxyGroupNames := buildProxyGroupNames(names, activeExitName)

	// Build rules list from EffectiveRules.
	ruleLines, err := buildRuleLines(cfg.EffectiveRules, activeExitName, names)
	if err != nil {
		return fmt.Errorf("mihomo: build rules: %w", err)
	}

	doc := map[string]any{
		"mode":                "rule",
		"log-level":           "warning",
		"ipv6":                false,
		"mixed-port":          mixedPort,
		"bind-address":        "127.0.0.1",
		"external-controller": "127.0.0.1:9090",
		"profile": map[string]any{
			"store-selected": false,
		},
		"proxies": proxies,
		"proxy-groups": []map[string]any{
			{"name": "PROXY", "type": "select", "proxies": proxyGroupNames},
		},
		"rules": ruleLines,
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return enc.Close()
}

// buildProxyGroupNames returns the proxy name list for the PROXY select group
// with activeExitName moved to the front so mihomo resolves to it by default.
func buildProxyGroupNames(names []string, activeExitName string) []string {
	out := make([]string, 0, len(names))
	out = append(out, activeExitName)
	for _, n := range names {
		if n != activeExitName {
			out = append(out, n)
		}
	}
	return out
}

// buildRuleLines converts EffectiveRules into mihomo rule strings.
// Rules are sorted by Priority (ascending). MATCH, if present in the set,
// is always emitted last regardless of its Priority value. If no MATCH rule
// exists in the set a synthetic "MATCH,<activeExitName>" is appended.
func buildRuleLines(effectiveRules []rules.Rule, activeExitName string, exitNames []string) ([]string, error) {
	if len(effectiveRules) == 0 {
		// No rules at all — emit a bare MATCH fallback.
		return []string{fmt.Sprintf("MATCH,%s", activeExitName)}, nil
	}

	// Separate MATCH from non-MATCH rules.
	var nonMatch []rules.Rule
	var matchRule *rules.Rule
	for i := range effectiveRules {
		r := effectiveRules[i]
		if !r.Enabled {
			continue
		}
		if r.Kind == rules.KindMatch {
			cp := r
			matchRule = &cp
		} else {
			nonMatch = append(nonMatch, r)
		}
	}

	// Sort non-MATCH rules by Priority ascending.
	sort.SliceStable(nonMatch, func(i, j int) bool {
		return nonMatch[i].Priority < nonMatch[j].Priority
	})

	out := make([]string, 0, len(nonMatch)+1)
	for _, r := range nonMatch {
		canonical, err := mihomoAction(r.Action, exitNames)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		r.Action = canonical
		line, err := r.ToMihomoLine()
		if err != nil {
			return nil, err
		}
		out = append(out, line)
	}

	// MATCH is always last.
	if matchRule != nil {
		canonical, err := mihomoAction(matchRule.Action, exitNames)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", matchRule.ID, err)
		}
		matchRule.Action = canonical
		line, err := matchRule.ToMihomoLine()
		if err != nil {
			return nil, err
		}
		out = append(out, line)
	} else {
		out = append(out, fmt.Sprintf("MATCH,%s", activeExitName))
	}

	return out, nil
}

func mihomoAction(action string, exitNames []string) (string, error) {
	action = strings.TrimSpace(action)
	switch strings.ToLower(action) {
	case "block", "reject":
		return "REJECT", nil
	case "direct":
		return "DIRECT", nil
	case "proxy":
		return "PROXY", nil
	}
	for _, name := range exitNames {
		if action == name {
			return action, nil
		}
	}
	return "", fmt.Errorf("action %q is neither a built-in policy nor a configured exit", action)
}
