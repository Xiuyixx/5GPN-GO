package rules

import "testing"

func mustParse(t *testing.T, body string) *RuleSet {
	t.Helper()
	set, err := ParseYAML([]byte(body))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	return set
}

func dryRunWithFallthrough(set *RuleSet, fixtures []TestFixture) []DryRunResult {
	ft, _ := ResolveFallthrough(set, "", "")
	return DryRun(set, fixtures, ft)
}

func TestParseYAMLGood(t *testing.T) {
	body := `
rules:
  - id: cn-suffix
    kind: DOMAIN-SUFFIX
    pattern: cn
    action: direct
    priority: 10
    enabled: true
  - id: fallback
    kind: MATCH
    pattern: ""
    action: wg1
    priority: 100
    enabled: true
`
	set := mustParse(t, body)
	if len(set.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(set.Rules))
	}
}

func TestParseYAMLRejectsUnknownKind(t *testing.T) {
	body := `rules:
  - id: x
    kind: WHATEVER
    pattern: foo
    action: direct
    priority: 1
    enabled: true`
	if _, err := ParseYAML([]byte(body)); err == nil {
		t.Fatal("expected validation error for unknown kind")
	}
}

func TestParseYAMLRejectsDuplicateID(t *testing.T) {
	body := `rules:
  - id: same
    kind: DOMAIN-SUFFIX
    pattern: cn
    action: direct
    priority: 1
    enabled: true
  - id: same
    kind: DOMAIN-SUFFIX
    pattern: us
    action: direct
    priority: 2
    enabled: true`
	if _, err := ParseYAML([]byte(body)); err == nil {
		t.Fatal("expected duplicate-id error")
	}
}

func TestDryRunDomainSuffix(t *testing.T) {
	set := mustParse(t, `rules:
  - id: cn-direct
    kind: DOMAIN-SUFFIX
    pattern: cn
    action: direct
    priority: 10
    enabled: true
  - id: fallback
    kind: MATCH
    pattern: ""
    action: wg1
    priority: 100
    enabled: true`)
	fixtures := []TestFixture{
		{Domain: "baidu.cn", ExpectedExit: "direct"},
		{Domain: "example.com", ExpectedExit: "wg1"},
		{Domain: "wrong.cn", ExpectedExit: "wg1"}, // deliberate mismatch
	}
	results := dryRunWithFallthrough(set, fixtures)
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	if !results[0].Pass || results[0].MatchedRule != "cn-direct" {
		t.Errorf("baidu.cn: %+v", results[0])
	}
	if !results[1].Pass || results[1].MatchedRule != "fallback" {
		t.Errorf("example.com: %+v", results[1])
	}
	if results[2].Pass {
		t.Errorf("wrong.cn should fail expected-exit check: %+v", results[2])
	}
}

func TestDryRunSkipsDisabled(t *testing.T) {
	set := mustParse(t, `rules:
  - id: cn-direct
    kind: DOMAIN-SUFFIX
    pattern: cn
    action: direct
    priority: 10
    enabled: false
  - id: fallback
    kind: MATCH
    pattern: ""
    action: wg1
    priority: 100
    enabled: true`)
	results := dryRunWithFallthrough(set, []TestFixture{{Domain: "baidu.cn", ExpectedExit: "wg1"}})
	if !results[0].Pass || results[0].MatchedRule != "fallback" {
		t.Errorf("disabled rule should be skipped: %+v", results[0])
	}
}

func TestDryRunIPCIDR(t *testing.T) {
	set := mustParse(t, `rules:
  - id: private
    kind: IP-CIDR
    pattern: 10.0.0.0/8
    action: direct
    priority: 5
    enabled: true
  - id: fallback
    kind: MATCH
    pattern: ""
    action: wg1
    priority: 100
    enabled: true`)
	results := dryRunWithFallthrough(set, []TestFixture{
		{Domain: "internal.example", IP: "10.1.2.3", ExpectedExit: "direct"},
		{Domain: "pub.example", IP: "8.8.8.8", ExpectedExit: "wg1"},
	})
	if !results[0].Pass {
		t.Errorf("10.1.2.3: %+v", results[0])
	}
	if !results[1].Pass {
		t.Errorf("8.8.8.8: %+v", results[1])
	}
}

func TestDryRunFallthroughNoMatchRule(t *testing.T) {
	// Ruleset with no MATCH rule — fallthrough should route unmatched fixtures to activeExit.
	set := mustParse(t, `rules:
  - id: cn-direct
    kind: DOMAIN-SUFFIX
    pattern: cn
    action: direct
    priority: 10
    enabled: true`)
	ft, hasUserMatch := ResolveFallthrough(set, "wg1", "")
	if hasUserMatch {
		t.Fatal("expected no user MATCH")
	}
	if ft != "wg1" {
		t.Fatalf("want fallthrough=wg1, got %q", ft)
	}
	results := DryRun(set, []TestFixture{
		{Domain: "baidu.cn", ExpectedExit: "direct"},
		{Domain: "example.com", ExpectedExit: "wg1"},
	}, ft)
	if !results[0].Pass {
		t.Errorf("baidu.cn should match cn-direct: %+v", results[0])
	}
	if !results[1].Pass {
		t.Errorf("example.com should route via fallthrough wg1: %+v", results[1])
	}
	if results[1].MatchedRule != "fallthrough" {
		t.Errorf("unmatched fixture should show MatchedRule=fallthrough: %+v", results[1])
	}
}

func TestResolveFallthroughPriority(t *testing.T) {
	setWithMatch := mustParse(t, `rules:
  - id: m
    kind: MATCH
    pattern: ""
    action: proxy
    priority: 100
    enabled: true`)
	target, hasUserMatch := ResolveFallthrough(setWithMatch, "wg1", "direct")
	if !hasUserMatch {
		t.Fatal("expected hasUserMatch=true")
	}
	if target != "proxy" {
		t.Fatalf("want proxy (user MATCH action), got %q", target)
	}

	emptySet := &RuleSet{}
	target, hasUserMatch = ResolveFallthrough(emptySet, "wg1", "direct")
	if hasUserMatch {
		t.Fatal("expected hasUserMatch=false")
	}
	if target != "wg1" {
		t.Fatalf("want activeExit=wg1, got %q", target)
	}

	target, _ = ResolveFallthrough(emptySet, "", "direct")
	if target != "direct" {
		t.Fatalf("want defaultAction=direct, got %q", target)
	}

	target, _ = ResolveFallthrough(emptySet, "", "")
	if target != "PROXY" {
		t.Fatalf("want hardcoded PROXY, got %q", target)
	}
}

func TestToMihomoLine(t *testing.T) {
	cases := []struct {
		rule Rule
		want string
	}{
		{Rule{ID: "a", Kind: KindDomain, Pattern: "example.com", Action: "direct"}, "DOMAIN,example.com,direct"},
		{Rule{ID: "b", Kind: KindDomainSuffix, Pattern: "cn", Action: "direct"}, "DOMAIN-SUFFIX,cn,direct"},
		{Rule{ID: "c", Kind: KindIPCIDR, Pattern: "10.0.0.0/8", Action: "direct"}, "IP-CIDR,10.0.0.0/8,direct"},
		{Rule{ID: "d", Kind: KindMatch, Pattern: "", Action: "wg1"}, "MATCH,wg1"},
		{Rule{ID: "e", Kind: KindGeoSite, Pattern: "cn", Action: "direct"}, "GEOSITE,cn,direct"},
	}
	for _, c := range cases {
		got, err := c.rule.ToMihomoLine()
		if err != nil {
			t.Errorf("ToMihomoLine(%v): unexpected error: %v", c.rule.ID, err)
			continue
		}
		if got != c.want {
			t.Errorf("ToMihomoLine(%v): want %q, got %q", c.rule.ID, c.want, got)
		}
	}
}

