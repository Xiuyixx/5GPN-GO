package resolver

import "strings"

// Action is the three-state outcome of classifying a query name against
// the active RuleTable.
type Action string

const (
	ActionBlock  Action = "block"
	ActionDirect Action = "direct"
	ActionProxy  Action = "proxy"
)

// classify walks tbl to decide qname's action. Precedence: exact DOMAIN
// match, then the most specific DOMAIN-SUFFIX match, then the first
// DOMAIN-KEYWORD match in priority order, then the table's default (its
// MATCH rule, or "proxy" per AC-R4 when no MATCH rule exists). A nil
// table (Store never published) also falls back to proxy.
//
// IP-CIDR / GEOIP rules never reach here — RuleTable.CIDRs is compiled
// but unconsulted, since the DNS layer classifies on qname only (AC-R5).
func classify(tbl *RuleTable, qname string) Action {
	if tbl == nil {
		return ActionProxy
	}
	name := normalizeName(qname)

	if action, ok := tbl.exact[name]; ok {
		return toAction(action)
	}
	if action, ok := tbl.suffix.lookup(name); ok {
		return toAction(action)
	}
	for _, kw := range tbl.keywords {
		if strings.Contains(name, kw.keyword) {
			return toAction(kw.action)
		}
	}
	if tbl.defaultAction != "" {
		return toAction(tbl.defaultAction)
	}
	return ActionProxy
}

// toAction normalizes a rule's free-form Action string (e.g. "block",
// "direct", "Proxy" — the rules package doesn't constrain case) into the
// resolver's three-state Action. Anything unrecognized collapses to
// proxy, matching the "no match -> proxy" default (AC-R4).
func toAction(s string) Action {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "block":
		return ActionBlock
	case "direct":
		return ActionDirect
	default:
		return ActionProxy
	}
}
