// Package rules implements parsing, validation, static dry-run matching, and
// serialization for the rules applied by the control plane.
package rules

import (
	"fmt"
	"net"
	"strings"

	"github.com/miekg/dns"
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
// GroupID identifies materialized managed-ruleset entries. Historical
// one-shot imports may also carry a non-empty value; callers must compare it
// against the current ruleset registry before treating the rule as managed.
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
	if strings.ContainsAny(r.Action, ",\r\n") {
		return fmt.Errorf("rule %s: action contains a CSV delimiter or newline", r.ID)
	}
	if strings.ContainsAny(r.Pattern, ",\r\n") {
		return fmt.Errorf("rule %s: pattern contains a CSV delimiter or newline", r.ID)
	}
	switch r.Kind {
	case KindDomain, KindDomainSuffix:
		if _, ok := dns.IsDomainName(strings.TrimSpace(r.Pattern)); !ok {
			return fmt.Errorf("rule %s: invalid domain pattern %q", r.ID, r.Pattern)
		}
	case KindIPCIDR:
		if _, _, err := net.ParseCIDR(strings.TrimSpace(r.Pattern)); err != nil {
			return fmt.Errorf("rule %s: invalid CIDR pattern %q", r.ID, r.Pattern)
		}
	case KindMatch:
		if strings.TrimSpace(r.Pattern) != "" {
			return fmt.Errorf("rule %s: MATCH rule must not have a pattern", r.ID)
		}
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
	matchIndex := -1
	for i, r := range s.Rules {
		if err := r.Validate(); err != nil {
			return err
		}
		if seen[r.ID] {
			return fmt.Errorf("rule %s: duplicate id", r.ID)
		}
		seen[r.ID] = true
		if r.Kind == KindMatch {
			matchCount++
			matchIndex = i
		}
	}
	if matchCount > 1 {
		return fmt.Errorf("ruleset: at most one MATCH rule allowed")
	}
	if matchIndex >= 0 {
		match := s.Rules[matchIndex]
		for i, r := range s.Rules {
			if i == matchIndex {
				continue
			}
			if r.Priority > match.Priority || (r.Priority == match.Priority && i > matchIndex) {
				return fmt.Errorf("ruleset: MATCH rule must be last after sorting by priority")
			}
		}
	}
	return nil
}
