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

// classify walks tbl to decide qname's action. All matching DOMAIN,
// DOMAIN-SUFFIX, and DOMAIN-KEYWORD rules compete in the same global priority
// order, matching the sequential rule list emitted to mihomo. The table's
// MATCH rule is consulted only when no qname rule matched. A nil table (Store
// never published) also falls back to proxy.
//
// IP-CIDR / GEOIP rules never reach here — RuleTable.CIDRs is compiled
// but unconsulted, since the DNS layer classifies on qname only (AC-R5).
func classify(tbl *RuleTable, qname string) Action {
	if tbl == nil {
		return ActionProxy
	}
	name := normalizeName(qname)

	var best rankedAction
	matched := false
	consider := func(candidate rankedAction, ok bool) {
		if ok && (!matched || candidate.rank < best.rank) {
			best = candidate
			matched = true
		}
	}

	exact, ok := tbl.exact[name]
	consider(exact, ok)
	suffix, ok := tbl.suffix.lookup(name)
	consider(suffix, ok)
	for _, kw := range tbl.keywords {
		if strings.Contains(name, kw.keyword) {
			consider(rankedAction{action: kw.action, rank: kw.rank}, true)
			break
		}
	}
	if matched {
		return toAction(best.action)
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
	case "block", "reject":
		return ActionBlock
	case "direct":
		return ActionDirect
	default:
		return ActionProxy
	}
}
