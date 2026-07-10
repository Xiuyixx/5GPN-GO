package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedLegacyTree writes the minimal set of files an operator would have
// on a real 5GPN-X host, under a temp root.
func seedLegacyTree(t *testing.T) (string, LegacyLayout) {
	t.Helper()
	root := t.TempDir()
	layout := LegacyDefaults().WithRoot(root)

	must := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	must(filepath.Join(layout.DnsdistEtc, ".domain"), "gate.example.com\n")
	must(filepath.Join(layout.DnsdistEtc, ".remote_dns"), "1.1.1.1 8.8.8.8\n")
	must(filepath.Join(layout.DnsdistEtc, ".local_dns"), "223.5.5.5\n")
	must(filepath.Join(layout.Root, "etc", "current-exit"), "wg1\n")
	must(filepath.Join(layout.EtcRoot, "rules.conf"), "DOMAIN-SUFFIX,google.com,wg1\nFINAL,direct\n")
	must(filepath.Join(layout.EtcRoot, "policy-map.conf"), "netflix=wg1\n")
	must(filepath.Join(layout.EtcRoot, "exits", "wg1.type"), "wireguard\n")
	must(filepath.Join(layout.EtcRoot, "exits", "trojan-jp.type"), "trojan\n")
	must(filepath.Join(layout.EtcRoot, "tgbot.env"),
		"TG_TOKEN=\"111:secret\"\nTG_ADMIN_IDS=42 43\n")
	must(filepath.Join(layout.Root, "etc", ".ios_profile_uuid"), "abcd-uuid\n")

	return root, layout
}

func TestExtract_ReadsEveryLegacyPath(t *testing.T) {
	_, layout := seedLegacyTree(t)
	e, err := Extract(layout)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if e.Domain != "gate.example.com" {
		t.Errorf("domain: %q", e.Domain)
	}
	if e.RemoteDNS != "1.1.1.1 8.8.8.8" {
		t.Errorf("remote dns: %q", e.RemoteDNS)
	}
	if e.LocalDNS != "223.5.5.5" {
		t.Errorf("local dns: %q", e.LocalDNS)
	}
	if e.CurrentExit != "wg1" {
		t.Errorf("current exit: %q", e.CurrentExit)
	}
	if !strings.Contains(e.Rules, "DOMAIN-SUFFIX,google.com,wg1") {
		t.Errorf("rules: %q", e.Rules)
	}
	if e.TGToken != "111:secret" {
		t.Errorf("token: %q", e.TGToken)
	}
	if e.TGAdminIDs != "42 43" {
		t.Errorf("admin ids: %q", e.TGAdminIDs)
	}
	if e.IOSProfileUUID != "abcd-uuid" {
		t.Errorf("ios uuid: %q", e.IOSProfileUUID)
	}
	if len(e.Exits) != 2 || e.Exits[0] != "trojan-jp" || e.Exits[1] != "wg1" {
		t.Errorf("exits: %v", e.Exits)
	}
	if len(e.SourcePaths) < 7 {
		t.Errorf("expected >=7 source paths, got %d", len(e.SourcePaths))
	}
}

func TestExtract_EmptyTreeReturnsSentinel(t *testing.T) {
	empty := LegacyDefaults().WithRoot(t.TempDir())
	_, err := Extract(empty)
	if !errors.Is(err, ErrNoLegacyFound) {
		t.Fatalf("want ErrNoLegacyFound, got %v", err)
	}
}

func TestRenderNewConfig_ProducesValidYAMLSkeleton(t *testing.T) {
	_, layout := seedLegacyTree(t)
	e, err := Extract(layout)
	if err != nil {
		t.Fatal(err)
	}
	yaml, warnings := RenderNewConfig(e)
	for _, want := range []string{
		`domain: "gate.example.com"`,
		`token: "111:secret"`,
		`admin_chat_ids: [42, 43]`,
		`profile_uuid: "abcd-uuid"`,
		"remote: \"1.1.1.1 8.8.8.8\"",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("yaml missing %q\n---\n%s", want, yaml)
		}
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings on complete tree: %v", warnings)
	}
}

func TestRenderNewConfig_EmitsWarningsForMissingFields(t *testing.T) {
	yaml, warnings := RenderNewConfig(LegacyExtract{})
	if !strings.Contains(yaml, `domain: "panel.local"`) {
		t.Errorf("expected panel.local placeholder in yaml:\n%s", yaml)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"domain", "TG_TOKEN", "exits"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning for %q: %v", want, warnings)
		}
	}
}

func TestMigrate_WritesConfigAndRespectsForce(t *testing.T) {
	root, layout := seedLegacyTree(t)
	env := Defaults().WithRoot(root)
	rec := NewRecorder()
	rec.Apply = true

	plan, err := Plan(layout)
	if err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), env, rec, plan, MigrateOptions{}); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if !strings.Contains(string(rec.Files[env.ConfigPath()]), "gate.example.com") {
		t.Errorf("migrated config missing domain: %s", rec.Files[env.ConfigPath()])
	}

	// Second call without --force must refuse.
	err = Migrate(context.Background(), env, rec, plan, MigrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("expected refusal on existing config, got %v", err)
	}

	// With --force it goes through.
	if err := Migrate(context.Background(), env, rec, plan, MigrateOptions{Force: true}); err != nil {
		t.Errorf("force migrate: %v", err)
	}
}

func TestPlan_SurfacesWarningsWithoutWriting(t *testing.T) {
	// Seed a partial tree: domain only, no TG creds.
	root := t.TempDir()
	layout := LegacyDefaults().WithRoot(root)
	if err := os.MkdirAll(layout.DnsdistEtc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.DnsdistEtc, ".domain"),
		[]byte("half.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(layout)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Extract.Domain != "half.example.com" {
		t.Errorf("domain: %q", plan.Extract.Domain)
	}
	if len(plan.Warnings) == 0 {
		t.Error("expected warnings for missing TG creds")
	}
}
