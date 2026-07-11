// Package rules implements the M1 rule engine: parse, validate, statically
// dry-run, snapshot, apply, and auto-rollback. The engine never spins a
// second mihomo TUN instance — that would violate the low-memory VPS budget.
// Instead dry-run compiles the candidate rules and simulates matches against
// SQLite-backed fixture domains.
package rules

import (
	"fmt"
	"strings"
)

// Kind is the classifier a rule matches on.
type Kind string

const (
	KindDomain        Kind = "DOMAIN"
	KindDomainSuffix  Kind = "DOMAIN-SUFFIX"
	KindDomainKeyword Kind = "DOMAIN-KEYWORD"
	KindGeoSite       Kind = "GEOSITE"
	KindGeoIP         Kind = "GEOIP"
	KindIPCIDR        Kind = "IP-CIDR"
	KindRuleSet       Kind = "RULE-SET"
	KindMatch         Kind = "MATCH"
)

var allKinds = map[Kind]struct{}{
	KindDomain: {}, KindDomainSuffix: {}, KindDomainKeyword: {},
	KindGeoSite: {}, KindGeoIP: {}, KindIPCIDR: {},
	KindRuleSet: {}, KindMatch: {},
}

// Rule is one entry in a rules list. Priority is 0-based; lower wins on ties.
//
// GroupID is optional UI-only metadata: rules that share a GroupID were
// added as one batch (usually an import). The panel collapses them into
// a single card. Manually-created rules leave GroupID empty and continue
// to render one-card-per-rule. GroupID has no effect on dry-run, apply,
// or the mihomo emission — it is a rendering hint only.
type Rule struct {
	ID       string `yaml:"id"       json:"id"`
	Kind     Kind   `yaml:"kind"     json:"kind"`
	Pattern  string `yaml:"pattern"  json:"pattern"`
	Action   string `yaml:"action"   json:"action"`
	Priority int    `yaml:"priority" json:"priority"`
	Enabled  bool   `yaml:"enabled"  json:"enabled"`
	Notes    string `yaml:"notes"    json:"notes,omitempty"`
	GroupID  string `yaml:"group_id,omitempty" json:"group_id,omitempty"`
}

// ToMihomoLine serializes the rule to a single mihomo rules-list entry.
// KindMatch omits the pattern field: "MATCH,<action>".
// All other kinds: "<KIND>,<pattern>,<action>".
func (r Rule) ToMihomoLine() (string, error) {
	if r.Kind == KindMatch {
		if strings.TrimSpace(r.Action) == "" {
			return "", fmt.Errorf("rule %s: MATCH rule requires an action", r.ID)
		}
		return fmt.Sprintf("MATCH,%s", r.Action), nil
	}
	if strings.TrimSpace(r.Pattern) == "" {
		return "", fmt.Errorf("rule %s: pattern required for kind %s", r.ID, r.Kind)
	}
	if strings.TrimSpace(r.Action) == "" {
		return "", fmt.Errorf("rule %s: action required", r.ID)
	}
	return fmt.Sprintf("%s,%s,%s", r.Kind, r.Pattern, r.Action), nil
}

// Validate checks a single rule for well-formedness.
func (r Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("rule: id required")
	}
	if _, ok := allKinds[r.Kind]; !ok {
		return fmt.Errorf("rule %s: unknown kind %q", r.ID, r.Kind)
	}
	if r.Kind != KindMatch && strings.TrimSpace(r.Pattern) == "" {
		return fmt.Errorf("rule %s: pattern required for kind %s", r.ID, r.Kind)
	}
	if strings.TrimSpace(r.Action) == "" {
		return fmt.Errorf("rule %s: action required", r.ID)
	}
	return nil
}

// RuleSet is the top-level document persisted in rule_versions.rules_yaml.
type RuleSet struct {
	Rules []Rule `yaml:"rules" json:"rules"`
}

// Validate checks the whole ruleset for well-formedness. Rules must have
// unique IDs; a single MATCH rule may exist and must be last after sorting
// by priority.
func (s RuleSet) Validate() error {
	seen := map[string]bool{}
	matchCount := 0
	for _, r := range s.Rules {
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.ID] {
			return fmt.Errorf("rule %s: duplicate id", r.ID)
		}
		seen[r.ID] = true
		if r.Kind == KindMatch {
			matchCount++
		}
	}
	if matchCount > 1 {
		return fmt.Errorf("ruleset: at most one MATCH rule allowed")
	}
	return nil
}
