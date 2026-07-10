package rules

import (
	"strings"
	"testing"
)

func TestImportLegacyBasicKinds(t *testing.T) {
	input := strings.Join([]string{
		"# comment",
		"[section header]",
		"DOMAIN-SUFFIX,google.com,🚀 Proxy",
		"DOMAIN,example.com,Netflix",
		"IP-CIDR6,2001:db8::/32,Proxy",
		"IP-CIDR,10.0.0.0/8,DIRECT",
		"FINAL,MATCH",
	}, "\n")
	rules, rep := ImportLegacy(input, ImportLegacyOptions{})
	if len(rules) != 5 {
		t.Fatalf("want 5 rules, got %d: %v", len(rules), rules)
	}
	joined := strings.Join(rules, "\n")
	// IP-CIDR6 must have been rewritten to IP-CIDR.
	if strings.Contains(joined, "IP-CIDR6") {
		t.Errorf("IP-CIDR6 should be rewritten to IP-CIDR: %v", rules)
	}
	// leading emoji stripped
	if !strings.Contains(joined, "DOMAIN-SUFFIX,google.com,Proxy") {
		t.Errorf("emoji not stripped from category: %v", rules)
	}
	// FINAL emitted last
	if rules[len(rules)-1] != "FINAL,MATCH" {
		t.Errorf("FINAL should be last, got %s", rules[len(rules)-1])
	}
	if rep.Converted != 5 || rep.FinalPolicy != "MATCH" {
		t.Errorf("report: %+v", rep)
	}
}

func TestImportLegacyDropsClientOnly(t *testing.T) {
	input := "PROCESS-NAME,chrome.exe,Proxy\nUSER-AGENT,curl,Proxy\nSRC-IP,1.2.3.4,Proxy\n"
	rules, rep := ImportLegacy(input, ImportLegacyOptions{})
	if len(rules) != 0 {
		t.Fatalf("client-only rules should be dropped, got: %v", rules)
	}
	if rep.Dropped != 3 {
		t.Errorf("want 3 dropped, got %d", rep.Dropped)
	}
}

func TestImportLegacyOrFlatten(t *testing.T) {
	input := "OR,((DOMAIN-SUFFIX,a.example),(DOMAIN-SUFFIX,b.example)),ProxyGroup\n"
	rules, rep := ImportLegacy(input, ImportLegacyOptions{})
	if len(rules) != 2 {
		t.Fatalf("expected 2 flattened rules, got: %v", rules)
	}
	if rep.OrFlattened != 1 {
		t.Errorf("or_flattened metric wrong: %+v", rep)
	}
}

func TestImportLegacyKeepCategoriesCollapse(t *testing.T) {
	input := strings.Join([]string{
		"DOMAIN-SUFFIX,foo.example,AI",
		"DOMAIN-SUFFIX,bar.example,SomeOther",
		"DOMAIN-SUFFIX,gg.reject,Advertising",
		"DOMAIN-SUFFIX,cn.example,DIRECT",
	}, "\n")
	rules, _ := ImportLegacy(input, ImportLegacyOptions{KeepCategories: []string{"AI"}})
	joined := strings.Join(rules, " || ")
	if !strings.Contains(joined, ",AI") {
		t.Errorf("AI should stay distinct: %v", rules)
	}
	if !strings.Contains(joined, ",Proxy") {
		t.Errorf("SomeOther should collapse to Proxy: %v", rules)
	}
	if !strings.Contains(joined, ",block") {
		t.Errorf("advertising should collapse to block: %v", rules)
	}
	if !strings.Contains(joined, ",direct") {
		t.Errorf("DIRECT should stay direct: %v", rules)
	}
}

func TestImportLegacyDirectCategoriesForce(t *testing.T) {
	input := "DOMAIN-SUFFIX,bilibili.com,Bilibili\nDOMAIN-SUFFIX,example.com,Proxy\n"
	rules, _ := ImportLegacy(input, ImportLegacyOptions{DirectCategories: []string{"bilibili"}})
	if !strings.Contains(strings.Join(rules, " "), "bilibili.com,direct") {
		t.Errorf("bilibili should be forced to direct: %v", rules)
	}
	if !strings.Contains(strings.Join(rules, " "), "example.com,Proxy") {
		t.Errorf("example.com should stay Proxy: %v", rules)
	}
}

func TestImportLegacyStripsModifiers(t *testing.T) {
	// value trailed by no-resolve modifier should still emit correctly.
	input := "DOMAIN-SUFFIX,google.com,Proxy,no-resolve\n"
	rules, _ := ImportLegacy(input, ImportLegacyOptions{})
	if len(rules) != 1 || !strings.Contains(rules[0], "google.com,Proxy") {
		t.Fatalf("modifier not stripped: %v", rules)
	}
}
