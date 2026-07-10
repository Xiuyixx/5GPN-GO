package rules

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ImportLegacyReport summarizes an import batch.
type ImportLegacyReport struct {
	Converted    int
	Dropped      int
	OrFlattened  int
	Categories   []string
	FinalPolicy  string
}

// Server-meaningful matchers we can convert.
var legacyKeep = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true,
	"IP-CIDR": true, "IP-CIDR6": true,
	"RULE-SET": true, "GEOIP": true, "GEOSITE": true,
}

// Client-only matchers we drop silently.
var legacyDrop = map[string]bool{
	"PROCESS-NAME": true, "SRC-IP": true, "SRC-PORT": true, "DEST-PORT": true,
	"IN-PORT": true, "MAC-ADDRESS": true, "DEVICE-NAME": true, "IP-ASN": true,
	"USER-AGENT": true, "SUBNET": true, "PROTOCOL": true, "CELLULAR-RADIO": true, "SSID": true,
}

var modifierRE = regexp.MustCompile(`(?i)^(no-resolve|extended-matching|dns-failed|pre-matching|update-interval=.*|interval=.*)$`)
var leadingSymbols = regexp.MustCompile(`^[^\w\p{Han}]+`)

// ImportLegacyOptions matches PGW_KEEP_CATEGORIES + PGW_DIRECT_CATEGORIES env
// vars that rules-import.py accepts.
type ImportLegacyOptions struct {
	// If non-empty, ONLY these categories are kept distinct; everything else
	// collapses to direct / block / Proxy.
	KeepCategories []string
	// Any policy in this list (matched case-insensitively) is forced to "direct".
	DirectCategories []string
}

// ImportOptionsFromEnv reads PGW_KEEP_CATEGORIES + PGW_DIRECT_CATEGORIES to
// match the Python legacy behavior exactly.
func ImportOptionsFromEnv() ImportLegacyOptions {
	split := func(s string) []string {
		out := []string{}
		for _, part := range regexp.MustCompile(`[,\s]+`).Split(s, -1) {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return ImportLegacyOptions{
		KeepCategories:   split(os.Getenv("PGW_KEEP_CATEGORIES")),
		DirectCategories: split(os.Getenv("PGW_DIRECT_CATEGORIES")),
	}
}

// ImportLegacy parses a legacy rule list (one rule per line, common clash /
// mihomo syntax) and returns rewritten gateway rules + report.
//
// Output format is the CSV form: "KIND,VALUE,CATEGORY" per line.
// FINAL rules become "FINAL,CATEGORY" at the tail.
func ImportLegacy(input string, opts ImportLegacyOptions) ([]string, ImportLegacyReport) {
	rep := ImportLegacyReport{}
	catSet := map[string]bool{}
	final := ""
	keepMap := map[string]bool{}
	for _, k := range opts.KeepCategories {
		keepMap[k] = true
	}
	directMap := map[string]bool{}
	for _, k := range opts.DirectCategories {
		directMap[strings.ToLower(k)] = true
	}

	catFn := func(policy string) string {
		cat := normCategory(policy)
		low := strings.ToLower(cat)
		if directMap[low] {
			return "direct"
		}
		if len(keepMap) == 0 || keepMap[cat] {
			return cat
		}
		if low == "dir" || low == "direct" || low == "china" || low == "lan" || low == "domestic" {
			return "direct"
		}
		for _, marker := range []string{"advert", "hijack", "privacy", "reject", "广告", "malware"} {
			if strings.Contains(low, marker) {
				return "block"
			}
		}
		return "Proxy"
	}

	emit := func(kind, value, policy string, out *[]string) {
		kind = strings.ToUpper(kind)
		if kind == "IP-CIDR6" {
			kind = "IP-CIDR"
		}
		cat := catFn(policy)
		*out = append(*out, fmt.Sprintf("%s,%s,%s", kind, value, cat))
		catSet[cat] = true
	}

	var rules []string

	for _, raw := range strings.Split(input, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		typ := strings.ToUpper(strings.TrimSpace(strings.SplitN(line, ",", 2)[0]))

		if typ == "OR" || typ == "AND" {
			members, policy := parseLogical(line)
			if policy == "" {
				continue
			}
			took := false
			for _, m := range members {
				parts := csvSplit(m)
				mt := strings.ToUpper(parts[0])
				if legacyKeep[mt] && len(parts) >= 2 {
					emit(mt, parts[1], policy, &rules)
					took = true
				}
			}
			if took {
				rep.OrFlattened++
			} else {
				rep.Dropped++
			}
			continue
		}

		parts := csvSplit(line)
		if typ == "FINAL" {
			if len(parts) > 1 {
				final = catFn(parts[1])
			}
			continue
		}
		if legacyDrop[typ] {
			rep.Dropped++
			continue
		}
		if !legacyKeep[typ] || len(parts) < 3 {
			rep.Dropped++
			continue
		}
		rest := []string{}
		for _, p := range parts[2:] {
			trimmed := strings.Trim(strings.TrimSpace(p), `"`)
			if !modifierRE.MatchString(trimmed) {
				rest = append(rest, p)
			}
		}
		if len(rest) == 0 {
			rep.Dropped++
			continue
		}
		emit(typ, parts[1], rest[0], &rules)
	}

	if final != "" {
		rules = append(rules, "FINAL,"+final)
		catSet[final] = true
		rep.FinalPolicy = final
	}
	rep.Converted = len(rules)
	cats := make([]string, 0, len(catSet))
	for c := range catSet {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	rep.Categories = cats
	return rules, rep
}

func normCategory(policy string) string {
	p := strings.TrimSpace(policy)
	p = strings.Trim(p, `"`)
	p = leadingSymbols.ReplaceAllString(p, "")
	p = strings.TrimSpace(p)
	if p == "" {
		return "Proxy"
	}
	return p
}

// csvSplit splits by comma, respecting double-quoted segments.
func csvSplit(s string) []string {
	out := []string{}
	cur := strings.Builder{}
	inQ := false
	for _, ch := range s {
		switch {
		case ch == '"':
			inQ = !inQ
		case ch == ',' && !inQ:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(ch)
		}
	}
	out = append(out, strings.TrimSpace(cur.String()))
	return out
}

// parseLogical extracts (members, policy) from a "OR/AND,((a),(b)),POLICY"
// line, or returns ([], "") on failure.
func parseLogical(line string) ([]string, string) {
	i := strings.Index(line, "(")
	if i < 0 {
		return nil, ""
	}
	depth := 0
	end := -1
	for j := i; j < len(line); j++ {
		if line[j] == '(' {
			depth++
		} else if line[j] == ')' {
			depth--
			if depth == 0 {
				end = j
				break
			}
		}
	}
	if end < 0 {
		return nil, ""
	}
	group := line[i+1 : end]
	tail := strings.TrimLeft(line[end+1:], ",")
	policy := ""
	if tail != "" {
		policy = csvSplit(tail)[0]
	}
	return splitTopParens(group), policy
}

func splitTopParens(s string) []string {
	out := []string{}
	depth := 0
	start := -1
	for i, ch := range s {
		switch ch {
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		}
	}
	return out
}
