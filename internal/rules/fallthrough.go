package rules

// ResolveFallthrough returns the fallthrough target for a ruleset that has no
// explicit KindMatch rule. Priority order:
//  1. user-supplied KindMatch in set (returns "" — caller should not append)
//  2. activeExit when non-empty
//  3. defaultAction when non-empty
//  4. hardcoded "PROXY"
//
// When the set already contains a KindMatch rule the second return value is
// true, signalling the renderer/dry-runner must NOT append a synthetic MATCH
// line (prevents double-MATCH, Risk R2).
func ResolveFallthrough(set *RuleSet, activeExit, defaultAction string) (target string, hasUserMatch bool) {
	for _, r := range set.Rules {
		if r.Kind == KindMatch && r.Enabled {
			return r.Action, true
		}
	}
	if activeExit != "" {
		return activeExit, false
	}
	if defaultAction != "" {
		return defaultAction, false
	}
	return "PROXY", false
}
