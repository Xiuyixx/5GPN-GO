package rules

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Package-level machinery for parsing gfwlist / AutoProxy-format rulesets.
//
// gfwlist.txt is Adblock Plus / AutoProxy syntax, NOT Clash/mihomo, so
// feeding it directly to ImportLegacy drops every line. This file
// converts a gfwlist body into Clash-style "KIND,VALUE,POLICY" lines and
// hands the result to ImportLegacy so the downstream categorization +
// modifier stripping stays identical to native Clash inputs.
//
// Reference for the syntax we accept:
//
//   ||domain.tld^          -> DOMAIN-SUFFIX,domain.tld,PROXY
//   ||domain.tld/path      -> DOMAIN-SUFFIX,domain.tld,PROXY (path dropped)
//   |http(s)://host/path   -> DOMAIN,host,PROXY
//   .example.com           -> DOMAIN-SUFFIX,example.com,PROXY
//   example.com            -> DOMAIN-KEYWORD,example.com,PROXY
//   @@||host^              -> DOMAIN-SUFFIX,host,DIRECT (whitelist)
//   *.example.com          -> DOMAIN-SUFFIX,example.com,PROXY
//   /regex/                -> skipped (we do not compile URL regexes)
//   !comment               -> skipped
//   [AutoProxy ...] header -> skipped
//
// gfwlist as shipped on github is base64-encoded to survive the same
// upstream that gfwlist targets. Import() below detects and decodes.

// gfwlistPolicyPROXY is the tag we emit for gfwlist entries. It gets
// rewritten to the caller-picked action by the api layer so imported
// rules point at a real exit.
const gfwlistPolicyPROXY = "Proxy"

// gfwlistPolicyDIRECT is emitted for @@ whitelist entries (bypass proxy).
// Lowercase matches the case ImportLegacy's catFn normalizes DIRECT
// aliases to, so downstream sees the canonical form.
const gfwlistPolicyDIRECT = "direct"

var (
	// hostRE matches a bare hostname (allowing Punycode + hyphens).
	// Rejects things like "1.2.3.4", paths, spaces.
	hostRE = regexp.MustCompile(`^(?:[a-zA-Z0-9\-]+(?:\.[a-zA-Z0-9\-]+)+)$`)
)

// Import auto-detects the input format and dispatches to the right
// parser. Currently recognizes:
//
//   - base64-encoded gfwlist  (github.com/gfwlist/gfwlist raw layout)
//   - decoded gfwlist / AutoProxy
//   - native Clash/mihomo lines
//
// Report.Categories includes an "gfwlist" entry when the gfwlist path
// was taken so callers can surface the detected format to the operator.
func Import(input string, opts ImportLegacyOptions) ([]string, ImportLegacyReport) {
	// gfwlist raw is base64-encoded with soft-wrap newlines. Detect + decode
	// before any other classification so the downstream heuristics see the
	// actual body.
	if decoded, ok := decodeIfBase64GFWList(input); ok {
		input = decoded
	}
	if looksLikeGFWList(input) {
		clashLines := gfwlistToClash(input)
		rules, rep := ImportLegacy(strings.Join(clashLines, "\n"), opts)
		// Tag the report so the UI can render "detected: gfwlist" without
		// re-implementing detection client-side.
		rep.Categories = mergeUnique(rep.Categories, "gfwlist")
		return rules, rep
	}
	return ImportLegacy(input, opts)
}

// decodeIfBase64GFWList tries base64-decoding the whole input after
// stripping whitespace. Returns (decoded, true) only when the result
// looks like a gfwlist body (has an AutoProxy header OR at least a
// handful of ||host^ / |http:// lines).
func decodeIfBase64GFWList(input string) (string, bool) {
	trimmed := stripAllWhitespace(input)
	if len(trimmed) < 32 {
		return "", false
	}
	// Cheap prefilter: base64 alphabet only. If any char is outside the
	// standard/URL-safe base64 set we skip the decode attempt entirely.
	for _, r := range trimmed {
		if !isBase64Char(r) {
			return "", false
		}
	}
	// Add missing padding — some gfwlist mirrors ship without it.
	padded := trimmed
	if pad := len(padded) % 4; pad != 0 {
		padded += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.StdEncoding.DecodeString(padded)
	if err != nil {
		// Try URL-safe fallback for mirrors that emit -_ instead of +/.
		raw, err = base64.URLEncoding.DecodeString(padded)
		if err != nil {
			return "", false
		}
	}
	decoded := string(raw)
	if looksLikeGFWList(decoded) {
		return decoded, true
	}
	return "", false
}

// looksLikeGFWList returns true when the input body has enough
// distinctive AutoProxy markers to justify running gfwlistToClash. The
// threshold is deliberately loose (a single `[AutoProxy` header OR three
// `||host^` lines) so we do not silently reject a rule set that a human
// would clearly recognize as gfwlist.
func looksLikeGFWList(input string) bool {
	if strings.Contains(input, "[AutoProxy") {
		return true
	}
	pipeHits := 0
	commaHits := 0
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "!") {
			continue
		}
		if strings.HasPrefix(line, "||") || strings.HasPrefix(line, "|http") || strings.HasPrefix(line, "@@") {
			pipeHits++
		}
		if strings.Contains(line, ",") {
			commaHits++
		}
		if pipeHits >= 3 && commaHits == 0 {
			return true
		}
	}
	return pipeHits >= 3 && pipeHits > commaHits
}

// gfwlistToClash converts a gfwlist body into an array of Clash-style
// "KIND,VALUE,POLICY" lines. Invalid entries (regex, IP literals, empty
// host after stripping) are silently dropped — ImportLegacy still counts
// what makes it through in Converted so the UI reports honest numbers.
func gfwlistToClash(input string) []string {
	seen := map[string]bool{}
	out := []string{}
	push := func(kind, value, policy string) {
		if value == "" {
			return
		}
		key := kind + "|" + value + "|" + policy
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("%s,%s,%s", kind, value, policy))
	}

	for _, raw := range strings.Split(input, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "!") || strings.HasPrefix(line, "[") {
			continue // comment or [AutoProxy 0.2.9] header
		}

		// Whitelist prefix: strip and remember the intended policy.
		policy := gfwlistPolicyPROXY
		if strings.HasPrefix(line, "@@") {
			policy = gfwlistPolicyDIRECT
			line = strings.TrimPrefix(line, "@@")
		}

		// Regex rules (delimited /.../ ) — we cannot compile those safely
		// into a categorical match; skip.
		if strings.HasPrefix(line, "/") && strings.HasSuffix(line, "/") && len(line) > 2 {
			continue
		}

		// ||host^ or ||host/path — anchored to hostname boundary.
		if strings.HasPrefix(line, "||") {
			body := strings.TrimPrefix(line, "||")
			body = trimGFWTail(body)
			host := hostFromBody(body)
			if host != "" && hostRE.MatchString(host) {
				push("DOMAIN-SUFFIX", host, policy)
			}
			continue
		}

		// |http(s)://host/path — anchored to protocol.
		if strings.HasPrefix(line, "|http://") || strings.HasPrefix(line, "|https://") {
			body := strings.TrimPrefix(line, "|")
			// Drop scheme.
			if idx := strings.Index(body, "://"); idx >= 0 {
				body = body[idx+3:]
			}
			host := hostFromBody(body)
			if host != "" && hostRE.MatchString(host) {
				push("DOMAIN", host, policy)
			}
			continue
		}

		// Wildcards + leading dot both indicate suffix intent.
		if strings.HasPrefix(line, "*.") {
			body := strings.TrimPrefix(line, "*.")
			body = trimGFWTail(body)
			if hostRE.MatchString(body) {
				push("DOMAIN-SUFFIX", body, policy)
			}
			continue
		}
		if strings.HasPrefix(line, ".") {
			body := strings.TrimPrefix(line, ".")
			body = trimGFWTail(body)
			if hostRE.MatchString(body) {
				push("DOMAIN-SUFFIX", body, policy)
			}
			continue
		}

		// Bare host (or host with path fragment). Treat as DOMAIN-KEYWORD
		// so partial matches catch subdomains — gfwlist's own semantics.
		body := trimGFWTail(line)
		host := hostFromBody(body)
		if host == "" {
			continue
		}
		if hostRE.MatchString(host) {
			push("DOMAIN-KEYWORD", host, policy)
		}
	}
	return out
}

// hostFromBody peels off any path/query fragment and returns just the
// hostname. Empty string when no meaningful host remains.
func hostFromBody(body string) string {
	if body == "" {
		return ""
	}
	// Cut path first.
	if idx := strings.IndexAny(body, "/?#"); idx >= 0 {
		body = body[:idx]
	}
	// Cut user@ prefix.
	if idx := strings.Index(body, "@"); idx >= 0 {
		body = body[idx+1:]
	}
	// Cut port suffix.
	if idx := strings.Index(body, ":"); idx >= 0 {
		body = body[:idx]
	}
	return strings.TrimSpace(body)
}

// trimGFWTail strips the trailing `^` marker AutoProxy uses to indicate a
// domain boundary. Also drops any trailing wildcards / whitespace.
func trimGFWTail(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "^")
	s = strings.TrimSuffix(s, "/")
	return s
}

func stripAllWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isBase64Char(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z':
		return true
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	}
	return r == '+' || r == '/' || r == '=' || r == '-' || r == '_'
}

func mergeUnique(existing []string, extra ...string) []string {
	seen := map[string]bool{}
	for _, s := range existing {
		seen[s] = true
	}
	out := append([]string(nil), existing...)
	for _, s := range extra {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
