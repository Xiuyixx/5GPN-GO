// Package core is the single-writer variation service: Assemble builds an
// effective config from base + DB state, and Applier funnels every data-plane
// change through render → reload → health → rollback. Web API, tgbot and
// 5gpn-ctl all reach the pipeline only through this package.
package core

import (
	"fmt"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// ExitRecord is the DB-projected view of one exit row. The concrete Store
// implementation (db-backed in cmd/5gpn/main.go, in-memory in tests) is
// responsible for calling xexit.Parse on the URI and stripping the "name"/
// "type" keys before handing Config back — Assemble strips them defensively
// anyway (Critic minor 4).
type ExitRecord struct {
	ID       string
	Protocol string
	Config   map[string]any
}

// Store is the read-only projection of DB state Assemble needs. It is
// intentionally minimal so the boot path and Applier hot path both use the
// same interface; concrete implementations live in cmd/5gpn/main.go and
// in-test fakes.
//
// ActiveRulesYAML returns the yaml of the active rule_versions row. ok=false
// means "no active row" (fresh install / empty DB) — NOT an error.
// ListExits returns all exits. A nil slice + nil err means "no exits yet".
type Store interface {
	ActiveRulesYAML() (yaml string, ok bool, err error)
	ListExits() ([]ExitRecord, error)
}

// NoStore is a Store that reports "nothing persisted yet". It is the default
// Applier.Store when api.New is called without an explicit store — tests get
// a fully wired Applier without needing to import internal/db.
type NoStore struct{}

func (NoStore) ActiveRulesYAML() (string, bool, error) { return "", false, nil }
func (NoStore) ListExits() ([]ExitRecord, error)       { return nil, nil }

// Assemble builds the effective config by deep-copying base and layering DB
// state onto it. base is NEVER mutated. An absent active rules row or nil
// exits slice is a valid empty state; DB errors and malformed persisted state
// fail closed so an Apply cannot silently drop policy.
//
// The returned *Config has EffectiveRules populated (yaml:"-" so it is
// nowhere near disk) and Exits potentially rewritten from DB.
func Assemble(base *config.Config, s Store) (*config.Config, error) {
	if base == nil {
		return nil, fmt.Errorf("core.Assemble: nil base config")
	}
	cfg := cloneConfig(base)

	if s == nil {
		return cfg, nil
	}

	if yaml, ok, err := s.ActiveRulesYAML(); err != nil {
		return nil, fmt.Errorf("core.Assemble: read active rule_version: %w", err)
	} else if ok {
		set, err := rules.ParseYAML([]byte(yaml))
		if err != nil {
			return nil, fmt.Errorf("core.Assemble: parse active rule_version: %w", err)
		} else if set != nil {
			if err := set.Validate(); err != nil {
				return nil, fmt.Errorf("core.Assemble: validate active rule_version: %w", err)
			}
			cfg.EffectiveRules = append([]rules.Rule(nil), set.Rules...)
		}
	}

	exits, err := s.ListExits()
	if err != nil {
		return nil, fmt.Errorf("core.Assemble: list exits: %w", err)
	} else if exits != nil {
		cfg.Exits = make([]config.ExitConfig, 0, len(exits))
		for _, e := range exits {
			cfg.Exits = append(cfg.Exits, config.ExitConfig{
				ID:       e.ID,
				Protocol: e.Protocol,
				Config:   stripNameType(e.Config),
			})
		}
	}

	return cfg, nil
}

// cloneConfig returns a shallow-immutable copy of cfg: the top-level Config
// struct is copied by value, and the slices/maps we might mutate later
// (Exits, EffectiveRules, and each ExitConfig.Config map) are deep-copied.
// Other fields are structs of scalars — assignment is enough.
func cloneConfig(cfg *config.Config) *config.Config {
	out := *cfg

	if cfg.Exits != nil {
		out.Exits = make([]config.ExitConfig, len(cfg.Exits))
		for i, e := range cfg.Exits {
			out.Exits[i] = config.ExitConfig{
				ID:       e.ID,
				Protocol: e.Protocol,
				Config:   cloneStringAnyMap(e.Config),
			}
		}
	}

	if cfg.EffectiveRules != nil {
		out.EffectiveRules = append([]rules.Rule(nil), cfg.EffectiveRules...)
	}

	if cfg.DNS.Upstreams != nil {
		out.DNS.Upstreams = append([]string(nil), cfg.DNS.Upstreams...)
	}
	if cfg.Proxy.WAShim.AllowCIDR != nil {
		out.Proxy.WAShim.AllowCIDR = append([]string(nil), cfg.Proxy.WAShim.AllowCIDR...)
	}
	if cfg.TGBot.AdminChatIDs != nil {
		out.TGBot.AdminChatIDs = append([]int64(nil), cfg.TGBot.AdminChatIDs...)
	}
	if cfg.Rules.Sources != nil {
		out.Rules.Sources = append([]config.RuleSource(nil), cfg.Rules.Sources...)
	}

	return &out
}

func cloneStringAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneAnyValue(v)
	}
	return out
}

// cloneAnyValue recursively deep-copies containers that YAML/JSON decoding
// commonly produces inside ExitConfig.Config (nested maps, slices).
// Scalars pass through unchanged.
func cloneAnyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneStringAnyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, elem := range t {
			out[i] = cloneAnyValue(elem)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	case []int:
		return append([]int(nil), t...)
	case []int64:
		return append([]int64(nil), t...)
	default:
		return v
	}
}

// stripNameType removes the "name" and "type" keys that xexit.Parse injects
// into its output — those clobber ExitConfig.ID/Protocol when the mihomo
// renderer merges the map (Critic minor 4).
func stripNameType(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "name" || k == "type" {
			continue
		}
		out[k] = cloneAnyValue(v)
	}
	return out
}
