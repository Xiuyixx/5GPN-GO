package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LegacyLayout points at a 5GPN-X install tree. Any field may be empty
// meaning "not present on this host". Defaults() returns the paths
// documented in docs/tgbot-legacy-commands.md.
type LegacyLayout struct {
	Root         string // e.g. /opt/proxy-gateway
	EtcRoot      string // e.g. /etc/proxy-gateway
	DnsdistEtc   string // e.g. /etc/dnsdist
}

// LegacyDefaults returns the canonical 5GPN-X paths.
func LegacyDefaults() LegacyLayout {
	return LegacyLayout{
		Root:       "/opt/proxy-gateway",
		EtcRoot:    "/etc/proxy-gateway",
		DnsdistEtc: "/etc/dnsdist",
	}
}

// WithRoot re-roots every legacy path under a directory (used in tests).
func (l LegacyLayout) WithRoot(root string) LegacyLayout {
	return LegacyLayout{
		Root:       filepath.Join(root, l.Root),
		EtcRoot:    filepath.Join(root, l.EtcRoot),
		DnsdistEtc: filepath.Join(root, l.DnsdistEtc),
	}
}

// LegacyExtract holds everything we could recover from a legacy install.
// Zero fields mean "not present" — the caller decides whether that
// requires user confirmation before writing the new config.
type LegacyExtract struct {
	Domain      string
	RemoteDNS   string
	LocalDNS    string
	CurrentExit string
	Rules       string   // raw rules.conf contents
	Exits       []string // exit names (from /etc/proxy-gateway/exits/*.type)
	PolicyMap   string   // raw policy-map.conf
	// TG bot creds live in the systemd unit env drop-in on legacy hosts;
	// captured as key/value best-effort.
	TGToken       string
	TGAdminIDs    string
	IOSProfileUUID string
	SourcePaths   []string // absolute paths that were read (for the migration log)
}

// Extract reads a legacy tree and returns whatever it finds. It never
// mutates the filesystem. Unreadable files are skipped, not fatal — the
// caller inspects the returned Extract and decides.
func Extract(layout LegacyLayout) (LegacyExtract, error) {
	e := LegacyExtract{}
	touch := func(path, value string) {
		if value != "" {
			e.SourcePaths = append(e.SourcePaths, path)
		}
	}

	// Domain lives in either /etc/dnsdist/.domain or /opt/.../.domain.
	if v := readTrim(filepath.Join(layout.DnsdistEtc, ".domain")); v != "" {
		e.Domain = v
		touch(filepath.Join(layout.DnsdistEtc, ".domain"), v)
	} else if v := readTrim(filepath.Join(layout.Root, "etc", ".domain")); v != "" {
		e.Domain = v
		touch(filepath.Join(layout.Root, "etc", ".domain"), v)
	}

	// DNS upstreams — legacy shipped either .remote_dns or .overseas_dns
	// as a synonym; try both.
	for _, name := range []string{".remote_dns", ".overseas_dns"} {
		if v := readTrim(filepath.Join(layout.DnsdistEtc, name)); v != "" {
			e.RemoteDNS = v
			touch(filepath.Join(layout.DnsdistEtc, name), v)
			break
		}
	}
	if v := readTrim(filepath.Join(layout.DnsdistEtc, ".local_dns")); v != "" {
		e.LocalDNS = v
		touch(filepath.Join(layout.DnsdistEtc, ".local_dns"), v)
	}

	// Current exit + rules + policy map.
	if v := readTrim(filepath.Join(layout.Root, "etc", "current-exit")); v != "" {
		e.CurrentExit = v
		touch(filepath.Join(layout.Root, "etc", "current-exit"), v)
	}
	if v := readTrim(filepath.Join(layout.EtcRoot, "rules.conf")); v != "" {
		e.Rules = v
		touch(filepath.Join(layout.EtcRoot, "rules.conf"), v)
	}
	if v := readTrim(filepath.Join(layout.EtcRoot, "policy-map.conf")); v != "" {
		e.PolicyMap = v
		touch(filepath.Join(layout.EtcRoot, "policy-map.conf"), v)
	}

	// Exits: /etc/proxy-gateway/exits/<name>.type
	if entries, err := os.ReadDir(filepath.Join(layout.EtcRoot, "exits")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if name, ok := strings.CutSuffix(entry.Name(), ".type"); ok {
				e.Exits = append(e.Exits, name)
			}
		}
		sort.Strings(e.Exits)
	}

	// TG bot env sits in systemd drop-ins; readEnvFile takes the first
	// path that parses.
	env := readEnvFile(filepath.Join(layout.EtcRoot, "tgbot.env"))
	if env == nil {
		env = readEnvFile(filepath.Join(layout.Root, "etc", "tgbot.env"))
	}
	if env != nil {
		e.TGToken = env["TG_TOKEN"]
		e.TGAdminIDs = env["TG_ADMIN_IDS"]
		if e.TGToken != "" {
			e.SourcePaths = append(e.SourcePaths, "tgbot.env")
		}
	}

	// iOS profile UUID lives in the mobileconfig; the installer stashes
	// the UUID separately at etc/.ios_profile_uuid to survive re-issues.
	if v := readTrim(filepath.Join(layout.Root, "etc", ".ios_profile_uuid")); v != "" {
		e.IOSProfileUUID = v
		touch(filepath.Join(layout.Root, "etc", ".ios_profile_uuid"), v)
	}

	if len(e.SourcePaths) == 0 {
		return e, ErrNoLegacyFound
	}
	return e, nil
}

// ErrNoLegacyFound is returned by Extract when nothing recognizable was
// present under the layout.
var ErrNoLegacyFound = errors.New("installer: no legacy 5GPN-X data found")

// MigratePlan describes what Migrate would write. Used as the dry-run
// output the operator confirms before actually applying.
type MigratePlan struct {
	Extract       LegacyExtract
	NewConfigYAML string
	Warnings      []string
}

// Plan returns a MigratePlan without touching the filesystem. Callers
// (--dry-run) can print the plan and stop.
func Plan(layout LegacyLayout) (MigratePlan, error) {
	e, err := Extract(layout)
	if err != nil {
		return MigratePlan{}, err
	}
	yaml, warnings := RenderNewConfig(e)
	return MigratePlan{Extract: e, NewConfigYAML: yaml, Warnings: warnings}, nil
}

// RenderNewConfig renders a config.yaml from a LegacyExtract. Empty
// fields fall back to the schema defaults. Non-fatal issues (missing
// domain, missing DNS) are returned as warnings so the operator can
// decide whether to fill them in before Migrate writes anything.
func RenderNewConfig(e LegacyExtract) (string, []string) {
	var warnings []string
	if e.Domain == "" {
		warnings = append(warnings, "domain not found in legacy tree — new config uses placeholder panel.local")
	}
	if e.TGToken == "" {
		warnings = append(warnings, "TG_TOKEN not found — Bot will refuse to start until you fill it in")
	}
	if e.TGAdminIDs == "" && e.TGToken != "" {
		warnings = append(warnings, "TG_ADMIN_IDS empty — Bot will refuse to start with an empty whitelist")
	}
	if len(e.Exits) == 0 {
		warnings = append(warnings, "no exits found under /etc/proxy-gateway/exits — Exits page will start empty")
	}

	domain := e.Domain
	if domain == "" {
		domain = "panel.local"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# migrated from legacy 5GPN-X (see /var/log/5gpn/migration.log)\n")
	fmt.Fprintf(&b, "server:\n  domain: %q\n  panel_bind: \"127.0.0.1\"\n  panel_port: 8443\n  tls: { cert: \"\", key: \"\" }\n\n", domain)
	fmt.Fprintf(&b, "panel:\n  session_ttl: 8h\n  rate_limit:\n    login_per_minute: 5\n    lockout_minutes: 15\n\n")
	if e.RemoteDNS != "" || e.LocalDNS != "" {
		fmt.Fprintf(&b, "dns:\n")
		if e.RemoteDNS != "" {
			fmt.Fprintf(&b, "  remote: %q\n", e.RemoteDNS)
		}
		if e.LocalDNS != "" {
			fmt.Fprintf(&b, "  local: %q\n", e.LocalDNS)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "tgbot:\n  token: %q\n  admin_chat_ids: [%s]\n\n", e.TGToken, joinInts(e.TGAdminIDs))
	fmt.Fprintf(&b, "ios:\n  profile_port: 8444\n  profile_uuid: %q\n", e.IOSProfileUUID)
	return b.String(), warnings
}

// MigrateOptions controls the write path.
type MigrateOptions struct {
	Force bool // rewrite config.yaml even if one exists
}

// Migrate writes the new config.yaml from a plan. It does NOT touch the
// legacy tree; teardown of the old systemd units is the operator's call
// after they confirm the new daemon is healthy.
func Migrate(_ context.Context, env Env, ex Executor, plan MigratePlan, opts MigrateOptions) error {
	if err := ensureDirs(env, ex); err != nil {
		return err
	}
	target := env.ConfigPath()
	if ex.Exists(target) && !opts.Force {
		return fmt.Errorf("installer: %s exists (pass --force to overwrite)", target)
	}
	return ex.WriteFile(target, []byte(plan.NewConfigYAML), 0o640)
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readEnvFile parses a systemd Environment=/EnvironmentFile= style file:
// lines like `KEY=value` or `KEY="value with spaces"`, ignoring blanks
// and #-comments.
func readEnvFile(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		val = strings.TrimPrefix(val, "\"")
		val = strings.TrimSuffix(val, "\"")
		out[key] = val
	}
	return out
}

// joinInts turns "111,222" or "111 222" into `111, 222` for YAML.
func joinInts(raw string) string {
	if raw == "" {
		return ""
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	return strings.Join(fields, ", ")
}
