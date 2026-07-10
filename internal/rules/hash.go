package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// HashRules produces a stable sha256 over the enabled ruleset, useful for
// snapshot identity + change detection.
func HashRules(set *RuleSet) string {
	rows := make([]string, 0, len(set.Rules))
	for _, r := range set.Rules {
		if !r.Enabled {
			continue
		}
		rows = append(rows, strings.Join([]string{
			r.ID, string(r.Kind), r.Pattern, r.Action, itoa(r.Priority),
		}, "\x1f"))
	}
	sort.Strings(rows)
	h := sha256.Sum256([]byte(strings.Join(rows, "\x1e")))
	return hex.EncodeToString(h[:])
}

func itoa(i int) string {
	// small local helper to avoid strconv import churn
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
