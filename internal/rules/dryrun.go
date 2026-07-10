package rules

import (
	"net"
	"sort"
	"strings"
)

// TestFixture is one probe against which the ruleset is simulated.
type TestFixture struct {
	Domain       string `json:"domain"`
	IP           string `json:"ip,omitempty"`
	ExpectedExit string `json:"expected_exit"`
	Notes        string `json:"notes,omitempty"`
}

// DryRunResult reports one probe outcome.
type DryRunResult struct {
	Domain        string `json:"domain"`
	MatchedRule   string `json:"matched_rule"`
	MatchedKind   Kind   `json:"matched_kind"`
	ActualExit    string `json:"actual_exit"`
	ExpectedExit  string `json:"expected_exit"`
	Pass          bool   `json:"pass"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// DryRun applies the candidate ruleset against a list of fixtures using a
// static matcher — no external processes, no TUN interfaces. GEOSITE/GEOIP
// rules are treated as matching only if the fixture explicitly names them
// in Notes (M1 keeps the fixture surface deliberately small; M2 loads real
// geosite dat files).
//
// fallthroughTarget is the exit name applied to fixtures that match no rule
// (equivalent to the renderer's synthetic MATCH fallback). Pass the result of
// ResolveFallthrough to keep dry-run and production behaviour identical.
func DryRun(set *RuleSet, fixtures []TestFixture, fallthroughTarget string) []DryRunResult {
	sorted := make([]Rule, 0, len(set.Rules))
	for _, r := range set.Rules {
		if !r.Enabled {
			continue
		}
		sorted = append(sorted, r)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })

	out := make([]DryRunResult, 0, len(fixtures))
	for _, f := range fixtures {
		res := DryRunResult{Domain: f.Domain, ExpectedExit: f.ExpectedExit}
		match := findMatch(sorted, f)
		if match == nil {
			// No rule matched — apply fallthrough target (mirrors renderer MATCH fallback).
			if fallthroughTarget != "" {
				res.MatchedRule = "fallthrough"
				res.MatchedKind = KindMatch
				res.ActualExit = fallthroughTarget
				res.Pass = f.ExpectedExit == "" || f.ExpectedExit == fallthroughTarget
				if !res.Pass {
					res.FailureReason = "actual exit differs from expected"
				}
			} else {
				res.FailureReason = "no rule matched"
			}
		} else {
			res.MatchedRule = match.ID
			res.MatchedKind = match.Kind
			res.ActualExit = match.Action
			res.Pass = f.ExpectedExit == "" || match.Action == f.ExpectedExit
			if !res.Pass {
				res.FailureReason = "actual exit differs from expected"
			}
		}
		out = append(out, res)
	}
	return out
}

func findMatch(rules []Rule, f TestFixture) *Rule {
	for i := range rules {
		r := &rules[i]
		if ruleMatchesFixture(*r, f) {
			return r
		}
	}
	return nil
}

func ruleMatchesFixture(r Rule, f TestFixture) bool {
	switch r.Kind {
	case KindDomain:
		return equalFold(f.Domain, r.Pattern)
	case KindDomainSuffix:
		return domainHasSuffix(f.Domain, r.Pattern)
	case KindDomainKeyword:
		return strings.Contains(strings.ToLower(f.Domain), strings.ToLower(r.Pattern))
	case KindIPCIDR:
		if f.IP == "" {
			return false
		}
		_, cidr, err := net.ParseCIDR(r.Pattern)
		if err != nil {
			return false
		}
		ip := net.ParseIP(f.IP)
		return ip != nil && cidr.Contains(ip)
	case KindGeoSite, KindGeoIP, KindRuleSet:
		// M1 stub: fixture must opt in explicitly via Notes token "hint:<pattern>".
		token := "hint:" + r.Pattern
		return strings.Contains(f.Notes, token)
	case KindMatch:
		return true
	}
	return false
}

func equalFold(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

func domainHasSuffix(domain, suffix string) bool {
	d := strings.ToLower(strings.TrimSpace(domain))
	s := strings.ToLower(strings.TrimSpace(suffix))
	if d == s {
		return true
	}
	return strings.HasSuffix(d, "."+s)
}

