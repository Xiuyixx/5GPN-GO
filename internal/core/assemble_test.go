package core

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

type fakeStore struct {
	yaml     string
	yamlOK   bool
	yamlErr  error
	exits    []ExitRecord
	exitsErr error
}

func (f *fakeStore) ActiveRulesYAML() (string, bool, error) {
	return f.yaml, f.yamlOK, f.yamlErr
}

func (f *fakeStore) ListExits() ([]ExitRecord, error) {
	return f.exits, f.exitsErr
}

func baseCfg() *config.Config {
	return &config.Config{
		Exits: []config.ExitConfig{
			{ID: "seed", Protocol: "direct", Config: map[string]any{"note": "base"}},
		},
	}
}

func TestAssembleDeepCopy(t *testing.T) {
	base := baseCfg()
	got, err := Assemble(base, &fakeStore{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got.Exits[0].ID = "mutated"
	got.Exits[0].Config["note"] = "mutated"
	if base.Exits[0].ID != "seed" {
		t.Fatalf("base.Exits[0].ID mutated: %q", base.Exits[0].ID)
	}
	if base.Exits[0].Config["note"] != "base" {
		t.Fatalf("base exit config map aliased: %v", base.Exits[0].Config)
	}
}

func TestAssembleEmptyStore(t *testing.T) {
	base := baseCfg()
	got, err := Assemble(base, &fakeStore{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Exits) != 1 || got.Exits[0].ID != "seed" {
		t.Fatalf("empty store must fall back to base exits, got %+v", got.Exits)
	}
	if got.EffectiveRules != nil {
		t.Fatalf("empty store must not set EffectiveRules, got %v", got.EffectiveRules)
	}
}

func TestAssembleBadYAML(t *testing.T) {
	base := baseCfg()
	got, err := Assemble(base, &fakeStore{yaml: "::: not yaml :::", yamlOK: true})
	if err != nil {
		t.Fatalf("bad YAML must not error, got %v", err)
	}
	if got.EffectiveRules != nil {
		t.Fatalf("bad YAML must leave EffectiveRules nil, got %v", got.EffectiveRules)
	}
}

func TestAssembleStoreErrorsNonFatal(t *testing.T) {
	base := baseCfg()
	got, err := Assemble(base, &fakeStore{
		yamlErr:  errors.New("db down"),
		exitsErr: errors.New("db down"),
	})
	if err != nil {
		t.Fatalf("store errors must not fail Assemble, got %v", err)
	}
	if len(got.Exits) != 1 || got.Exits[0].ID != "seed" {
		t.Fatalf("exits err must fall back to base, got %+v", got.Exits)
	}
	if got.EffectiveRules != nil {
		t.Fatalf("rules err must leave EffectiveRules nil, got %v", got.EffectiveRules)
	}
}

func TestAssembleStripsExitNameType(t *testing.T) {
	base := baseCfg()
	got, err := Assemble(base, &fakeStore{
		exits: []ExitRecord{{
			ID:       "e1",
			Protocol: "ss",
			Config: map[string]any{
				"name":     "should-be-stripped",
				"type":     "should-be-stripped",
				"server":   "1.2.3.4",
				"port":     8388,
				"password": "p",
			},
		}},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.Exits) != 1 {
		t.Fatalf("expected 1 exit, got %d", len(got.Exits))
	}
	m := got.Exits[0].Config
	if _, ok := m["name"]; ok {
		t.Fatalf("expected name stripped, got %v", m)
	}
	if _, ok := m["type"]; ok {
		t.Fatalf("expected type stripped, got %v", m)
	}
	if m["server"] != "1.2.3.4" {
		t.Fatalf("expected server preserved, got %v", m)
	}
}

func TestAssembleWithRules(t *testing.T) {
	yamlBody := "" +
		"rules:\n" +
		"  - id: r1\n" +
		"    kind: DOMAIN-SUFFIX\n" +
		"    pattern: example.com\n" +
		"    action: PROXY\n" +
		"    priority: 10\n" +
		"    enabled: true\n" +
		"  - id: r2\n" +
		"    kind: MATCH\n" +
		"    action: DIRECT\n" +
		"    priority: 100\n" +
		"    enabled: true\n"
	got, err := Assemble(baseCfg(), &fakeStore{yaml: yamlBody, yamlOK: true})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got.EffectiveRules) != 2 {
		t.Fatalf("expected 2 effective rules, got %d", len(got.EffectiveRules))
	}
	if got.EffectiveRules[0].Kind != rules.KindDomainSuffix {
		t.Fatalf("expected DOMAIN-SUFFIX, got %q", got.EffectiveRules[0].Kind)
	}
	if got.EffectiveRules[1].Kind != rules.KindMatch || got.EffectiveRules[1].Action != "DIRECT" {
		t.Fatalf("expected MATCH,DIRECT, got %+v", got.EffectiveRules[1])
	}
}

func TestAssembleNilBase(t *testing.T) {
	_, err := Assemble(nil, &fakeStore{})
	if err == nil {
		t.Fatalf("expected error on nil base")
	}
}

func TestAssembleEffectiveRulesIndependent(t *testing.T) {
	base := baseCfg()
	base.EffectiveRules = []rules.Rule{{ID: "seed-rule"}}
	got, err := Assemble(base, &fakeStore{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got.EffectiveRules[0].ID = "mutated"
	if base.EffectiveRules[0].ID != "seed-rule" {
		t.Fatalf("EffectiveRules aliased base, got %q", base.EffectiveRules[0].ID)
	}
}

// TestAssembleBootParity exercises Risk R1 boot-restart parity: two cold-start
// Assemble calls against the same base config + store must produce
// byte-equivalent EffectiveRules and Exits, and must not mutate base.
// This is what closes the loop between "user apply" and "systemd restart":
// if boot Assemble diverged from Apply-time Assemble, restart would silently
// drop the user's rules or exits.
func TestAssembleBootParity(t *testing.T) {
	yamlBody := "" +
		"rules:\n" +
		"  - id: cn-suffix\n" +
		"    kind: DOMAIN-SUFFIX\n" +
		"    pattern: cn\n" +
		"    action: direct\n" +
		"    priority: 10\n" +
		"    enabled: true\n" +
		"  - id: fallback\n" +
		"    kind: MATCH\n" +
		"    action: wg1\n" +
		"    priority: 100\n" +
		"    enabled: true\n"

	store := &fakeStore{
		yaml:   yamlBody,
		yamlOK: true,
		exits: []ExitRecord{
			{ID: "wg1", Protocol: "wireguard", Config: map[string]any{"endpoint": "1.2.3.4:51820"}},
			{ID: "ss2", Protocol: "ss", Config: map[string]any{"server": "5.6.7.8", "port": 8388}},
		},
	}

	base := baseCfg()
	// Snapshot the base's mutable state so we can prove Assemble didn't touch it.
	baseExitCount := len(base.Exits)
	baseExitID := base.Exits[0].ID
	baseExitNote := base.Exits[0].Config["note"]
	baseEffectiveRules := base.EffectiveRules

	first, err := Assemble(base, store)
	if err != nil {
		t.Fatalf("Assemble #1: %v", err)
	}
	second, err := Assemble(base, store)
	if err != nil {
		t.Fatalf("Assemble #2: %v", err)
	}

	if !reflect.DeepEqual(first.EffectiveRules, second.EffectiveRules) {
		t.Fatalf("EffectiveRules diverged between Assemble calls:\n first=%+v\nsecond=%+v",
			first.EffectiveRules, second.EffectiveRules)
	}
	if len(first.EffectiveRules) != 2 {
		t.Fatalf("expected 2 EffectiveRules, got %d", len(first.EffectiveRules))
	}
	if !reflect.DeepEqual(first.Exits, second.Exits) {
		t.Fatalf("Exits diverged between Assemble calls:\n first=%+v\nsecond=%+v", first.Exits, second.Exits)
	}
	if len(first.Exits) != 2 {
		t.Fatalf("expected 2 Exits from store, got %d", len(first.Exits))
	}

	if len(base.Exits) != baseExitCount || base.Exits[0].ID != baseExitID {
		t.Fatalf("base.Exits mutated by Assemble: %+v", base.Exits)
	}
	if base.Exits[0].Config["note"] != baseExitNote {
		t.Fatalf("base exit config map mutated: %v", base.Exits[0].Config)
	}
	if !reflect.DeepEqual(base.EffectiveRules, baseEffectiveRules) {
		t.Fatalf("base.EffectiveRules mutated by Assemble: %+v", base.EffectiveRules)
	}

	// Sanity: the returned Configs share nothing mutable with each other either.
	first.EffectiveRules[0].ID = "poisoned"
	if second.EffectiveRules[0].ID == "poisoned" {
		t.Fatalf("Assemble returned aliased EffectiveRules slices across calls")
	}
	first.Exits[0].Config["note"] = "poisoned"
	if v, _ := second.Exits[0].Config["note"]; v == "poisoned" {
		t.Fatalf("Assemble returned aliased Exits[].Config maps across calls")
	}
}

// TestAssembleBootRestartPreservesRules mirrors the boot-time flow: seed a
// rule_version into the store, call Assemble to build the effective config,
// then simulate a restart by throwing away the config and re-Assembling from
// the same base + store. The rebuilt config must carry the same rules —
// otherwise a systemd restart would silently drop the user's Apply.
func TestAssembleBootRestartPreservesRules(t *testing.T) {
	yamlBody := "" +
		"rules:\n" +
		"  - id: r1\n" +
		"    kind: DOMAIN\n" +
		"    pattern: example.com\n" +
		"    action: direct\n" +
		"    priority: 1\n" +
		"    enabled: true\n" +
		"  - id: r2\n" +
		"    kind: IP-CIDR\n" +
		"    pattern: 10.0.0.0/8\n" +
		"    action: direct\n" +
		"    priority: 2\n" +
		"    enabled: true\n"

	store := &fakeStore{yaml: yamlBody, yamlOK: true}

	// Boot #1: cold start.
	boot1, err := Assemble(baseCfg(), store)
	if err != nil {
		t.Fatalf("Assemble boot1: %v", err)
	}

	// Boot #2: simulated restart with the same base + persistent store.
	boot2, err := Assemble(baseCfg(), store)
	if err != nil {
		t.Fatalf("Assemble boot2: %v", err)
	}

	if !reflect.DeepEqual(boot1.EffectiveRules, boot2.EffectiveRules) {
		t.Fatalf("restart parity violated:\n boot1=%+v\n boot2=%+v",
			boot1.EffectiveRules, boot2.EffectiveRules)
	}

	// And they must both carry the seeded rules — not a nil slice.
	if len(boot1.EffectiveRules) != 2 {
		t.Fatalf("expected 2 rules after boot, got %d", len(boot1.EffectiveRules))
	}
	if boot1.EffectiveRules[0].ID != "r1" || boot1.EffectiveRules[1].ID != "r2" {
		t.Fatalf("rule ordering not preserved: %+v", boot1.EffectiveRules)
	}
}

// TestAssembleDeepCopyNestedMap proves cloneStringAnyMap recursively
// deep-copies containers inside ExitConfig.Config values. Mutating the
// returned cfg's nested map/slice must never leak into base or into a
// subsequent Assemble call fed by the same store (F19 audit fix).
func TestAssembleDeepCopyNestedMap(t *testing.T) {
	base := baseCfg()
	store := &fakeStore{
		exits: []ExitRecord{{
			ID:       "wg1",
			Protocol: "wireguard",
			Config: map[string]any{
				"peers": []any{
					map[string]any{"pubkey": "A", "allowed_ips": []string{"0.0.0.0/0"}},
				},
			},
		}},
	}

	first, err := Assemble(base, store)
	if err != nil {
		t.Fatalf("first Assemble: %v", err)
	}
	peers := first.Exits[0].Config["peers"].([]any)
	peers[0].(map[string]any)["pubkey"] = "MUTATED"
	peers[0].(map[string]any)["allowed_ips"].([]string)[0] = "10.0.0.0/8"

	second, err := Assemble(base, store)
	if err != nil {
		t.Fatalf("second Assemble: %v", err)
	}
	got := second.Exits[0].Config["peers"].([]any)[0].(map[string]any)
	if got["pubkey"] != "A" {
		t.Fatalf("nested map value leaked between Assemble calls: pubkey=%v", got["pubkey"])
	}
	if got["allowed_ips"].([]string)[0] != "0.0.0.0/0" {
		t.Fatalf("nested slice value leaked between Assemble calls: allowed_ips=%v", got["allowed_ips"])
	}
}
