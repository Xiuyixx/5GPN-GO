package resolver

import (
	"encoding/hex"
	"net"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// RuleTable is an immutable, precompiled snapshot of the active ruleset.
// Once handed to Store.Publish it must never be mutated — readers pin a
// pointer via Store.Load() for the lifetime of a single query, so any
// post-publish write would be a data race (see plan §2.3 Fork C).
type RuleTable struct {
	Generation int64
	Hash       [32]byte

	exact         map[string]string // normalized fqdn -> action
	suffix        *suffixNode       // domain-suffix trie, keyed by reversed labels
	keywords      []keywordRule     // domain-keyword, priority order
	defaultAction string            // MATCH rule's action, or "" (classify falls back to proxy)

	// CIDRs is compiled from IP-CIDR / GEOIP rules for future response-IP
	// filtering. classify() never consults it — IP-based rules are
	// meaningless against a qname (AC-R5).
	CIDRs []cidrEntry
}

type keywordRule struct {
	keyword string
	action  string
}

type cidrEntry struct {
	Net    *net.IPNet
	Action string
}

type suffixNode struct {
	children  map[string]*suffixNode
	action    string
	hasAction bool
}

func newSuffixNode() *suffixNode {
	return &suffixNode{children: make(map[string]*suffixNode)}
}

// insert adds a domain-suffix rule. Only the first (highest-priority,
// since callers insert in priority order) rule for a given suffix wins;
// later duplicates targeting the same suffix are no-ops.
func (n *suffixNode) insert(labels []string, action string) {
	cur := n
	for i := len(labels) - 1; i >= 0; i-- {
		lbl := labels[i]
		child, ok := cur.children[lbl]
		if !ok {
			child = newSuffixNode()
			cur.children[lbl] = child
		}
		cur = child
	}
	if !cur.hasAction {
		cur.action = action
		cur.hasAction = true
	}
}

// lookup returns the most specific (deepest) suffix match for name, e.g.
// a rule on "sub.example.com" outranks one on "example.com" for the
// query "www.sub.example.com".
func (n *suffixNode) lookup(name string) (string, bool) {
	labels := splitLabels(name)
	cur := n
	action, ok := "", false
	for i := len(labels) - 1; i >= 0; i-- {
		child, exists := cur.children[labels[i]]
		if !exists {
			break
		}
		cur = child
		if cur.hasAction {
			action, ok = cur.action, true
		}
	}
	return action, ok
}

func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".")
}

func splitLabels(name string) []string {
	if name == "" {
		return nil
	}
	return strings.Split(name, ".")
}

// BuildTable compiles rs into an immutable RuleTable. Disabled rules are
// dropped; rules are processed in Priority order (lower wins on ties, per
// rules.Rule's documented semantics) so the first rule to claim an exact
// name or suffix wins.
//
// IP-CIDR / GEOIP rules are IP-only and are recorded in CIDRs but do not
// participate in qname classification (AC-R5) — a malformed CIDR pattern
// (or a GEOIP country code, which is never a CIDR) is silently skipped
// rather than failing the build. GEOSITE / RULE-SET entries are meta-rules
// expected to already be expanded by rulesets.Expand() before reaching
// here; any that slip through are skipped the same way.
func BuildTable(rs []rules.Rule) (*RuleTable, error) {
	t := &RuleTable{
		exact:  make(map[string]string),
		suffix: newSuffixNode(),
	}

	sorted := make([]rules.Rule, 0, len(rs))
	for _, r := range rs {
		if r.Enabled {
			sorted = append(sorted, r)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })

	for _, r := range sorted {
		compileRule(t, r)
	}

	sum := rules.HashRules(&rules.RuleSet{Rules: rs})
	if raw, err := hex.DecodeString(sum); err == nil {
		copy(t.Hash[:], raw)
	}

	return t, nil
}

func compileRule(t *RuleTable, r rules.Rule) {
	switch r.Kind {
	case rules.KindDomain:
		name := normalizeName(r.Pattern)
		if _, exists := t.exact[name]; !exists {
			t.exact[name] = r.Action
		}
	case rules.KindDomainSuffix:
		t.suffix.insert(splitLabels(normalizeName(r.Pattern)), r.Action)
	case rules.KindDomainKeyword:
		t.keywords = append(t.keywords, keywordRule{keyword: strings.ToLower(r.Pattern), action: r.Action})
	case rules.KindMatch:
		if t.defaultAction == "" {
			t.defaultAction = r.Action
		}
	case rules.KindIPCIDR, rules.KindGeoIP:
		if _, cidr, err := net.ParseCIDR(r.Pattern); err == nil {
			t.CIDRs = append(t.CIDRs, cidrEntry{Net: cidr, Action: r.Action})
		}
		// Parse failures (including GEOIP's country-code patterns, which
		// are never valid CIDRs) are silently skipped — see doc comment.
	default:
		// GEOSITE / RULE-SET: expected to be pre-expanded upstream of BuildTable.
	}
}

// Store holds the currently active RuleTable behind an atomic pointer so
// readers can pin a snapshot for a query's lifetime with zero lock
// contention, and writers publish a freshly built table with one Store().
type Store struct {
	p   atomic.Pointer[RuleTable]
	gen atomic.Int64
}

// Load returns the currently active table, or nil if Publish has never
// been called.
func (s *Store) Load() *RuleTable {
	return s.p.Load()
}

// Publish atomically swaps in t as the active table, stamping it with the
// next generation number. t must not be mutated after this call — any
// in-flight query that already pinned the previous table via Load() keeps
// seeing that snapshot, never a partially-applied t.
func (s *Store) Publish(t *RuleTable) {
	if t == nil {
		return
	}
	t.Generation = s.gen.Add(1)
	s.p.Store(t)
}
