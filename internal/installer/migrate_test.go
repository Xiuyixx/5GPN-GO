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
	must(filepath.Join(layout.Root, "etc", "tgbot.env"),
		"TG_BOT_TOKEN=\"111:secret\"\nTG_ADMIN_IDS=42 43\n")
	must(filepath.Join(layout.Root, "www", "ios-dot.mobileconfig"), `<?xml version="1.0"?>
<plist><dict>
<key>PayloadUUID</key><string>11111111-1111-1111-1111-111111111111</string>
<key>PayloadUUID</key><string>22222222-2222-2222-2222-222222222222</string>
</dict></plist>`)

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
	if e.IOSProfileUUID != "22222222-2222-2222-2222-222222222222" {
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
		`profile_uuid: "22222222-2222-2222-2222-222222222222"`,
		`http_port: 0`,
		`- "1.1.1.1"`,
		`- "8.8.8.8"`,
		`- "223.5.5.5"`,
		`listen: "127.0.0.1"`,
		`allow_cidr:`,
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("yaml missing %q\n---\n%s", want, yaml)
		}
	}
	joinedWarnings := strings.Join(warnings, "\n")
	for _, want := range []string{
		"legacy local/remote DNS roles",
		"legacy rules",
		"legacy policy map",
		"legacy exits",
		"legacy active exit",
	} {
		if !strings.Contains(joinedWarnings, want) {
			t.Errorf("missing warning %q: %v", want, warnings)
		}
	}
}

func TestLegacyDNSUpstreams_SplitsAndDeduplicates(t *testing.T) {
	got := legacyDNSUpstreams("1.1.1.1, 8.8.8.8", "8.8.8.8;223.5.5.5")
	want := []string{"1.1.1.1", "8.8.8.8", "223.5.5.5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("upstreams=%v want=%v", got, want)
	}
}

func TestNormalizeAdminIDs_DropsInvalidAndDuplicateValues(t *testing.T) {
	got, rejected := normalizeAdminIDs("42, nope 0 42 -7")
	if got != "42, -7" || rejected != 3 {
		t.Fatalf("normalizeAdminIDs=(%q, %d)", got, rejected)
	}
}

func TestRenderNewConfig_DisablesIncompleteTelegramConfig(t *testing.T) {
	body, warnings := RenderNewConfig(LegacyExtract{
		Domain:  "gateway.example.com",
		TGToken: "111:secret",
	})
	if !strings.Contains(body, `token: ""`) {
		t.Fatalf("incomplete Telegram config must not make the daemon unbootable:\n%s", body)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "token omitted") {
		t.Fatalf("missing omission warning: %v", warnings)
	}
	if fields := unsupportedMigrationFields(LegacyExtract{TGToken: "111:secret"}); len(fields) != 1 {
		t.Fatalf("incomplete Telegram config must be fail-closed: %v", fields)
	}
}

func TestExtract_FallsBackToInstallRootDNS(t *testing.T) {
	root := t.TempDir()
	layout := LegacyDefaults().WithRoot(root)
	if err := os.MkdirAll(filepath.Join(layout.Root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Root, "etc", ".remote_dns"), []byte("9.9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := Extract(layout)
	if err != nil {
		t.Fatal(err)
	}
	if e.RemoteDNS != "9.9.9.9" {
		t.Fatalf("remote DNS=%q", e.RemoteDNS)
	}
}

func TestRenderNewConfig_EmitsWarningsForMissingFields(t *testing.T) {
	yaml, warnings := RenderNewConfig(LegacyExtract{})
	if !strings.Contains(yaml, `domain: "panel.local"`) {
		t.Errorf("expected panel.local placeholder in yaml:\n%s", yaml)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"domain", "TG_BOT_TOKEN", "exits"} {
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

	if err := Migrate(context.Background(), env, rec, plan, MigrateOptions{AllowPartial: true}); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if !strings.Contains(string(rec.Files[env.ConfigPath()]), "gate.example.com") {
		t.Errorf("migrated config missing domain: %s", rec.Files[env.ConfigPath()])
	}

	// Second call without --force must refuse.
	err = Migrate(context.Background(), env, rec, plan, MigrateOptions{AllowPartial: true})
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("expected refusal on existing config, got %v", err)
	}

	// With --force it goes through.
	if err := Migrate(context.Background(), env, rec, plan, MigrateOptions{Force: true, AllowPartial: true}); err != nil {
		t.Errorf("force migrate: %v", err)
	}
}

func TestMigrate_RefusesToDropSQLiteBackedState(t *testing.T) {
	_, layout := seedLegacyTree(t)
	plan, err := Plan(layout)
	if err != nil {
		t.Fatal(err)
	}
	env := Defaults().WithRoot(t.TempDir())
	rec := NewRecorder()
	rec.Apply = true

	err = Migrate(context.Background(), env, rec, plan, MigrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "refusing partial legacy migration") {
		t.Fatalf("expected fail-closed migration, got %v", err)
	}
	if len(rec.Ops) != 0 {
		t.Fatalf("refused migration must not mutate anything, ops=%v", rec.Ops)
	}
}

func TestExtract_AcceptsHistoricalTGTokenFallback(t *testing.T) {
	root := t.TempDir()
	layout := LegacyDefaults().WithRoot(root)
	if err := os.MkdirAll(layout.EtcRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.EtcRoot, "tgbot.env"),
		[]byte("TG_TOKEN=legacy-fixture-token\nTG_ADMIN_IDS=42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := Extract(layout)
	if err != nil {
		t.Fatal(err)
	}
	if e.TGToken != "legacy-fixture-token" {
		t.Fatalf("fallback token=%q", e.TGToken)
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
