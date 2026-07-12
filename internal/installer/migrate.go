package installer

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// LegacyLayout points at a 5GPN-X install tree. Any field may be empty
// meaning "not present on this host". Defaults() returns the paths
// documented in docs/tgbot-legacy-commands.md.
type LegacyLayout struct {
	Root       string // e.g. /opt/proxy-gateway
	EtcRoot    string // e.g. /etc/proxy-gateway
	DnsdistEtc string // e.g. /etc/dnsdist
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
	TGToken        string
	TGAdminIDs     string
	IOSProfileUUID string
	SourcePaths    []string // absolute paths that were read (for the migration log)
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

	// DNS upstreams are normally mirrored into /etc/dnsdist and the legacy
	// install root. Prefer dnsdist's live copy, then fall back to the durable
	// /opt/proxy-gateway copy if dnsdist was already removed.
	for _, dir := range []string{layout.DnsdistEtc, filepath.Join(layout.Root, "etc")} {
		for _, name := range []string{".remote_dns", ".overseas_dns"} {
			path := filepath.Join(dir, name)
			if v := readTrim(path); v != "" {
				e.RemoteDNS = v
				touch(path, v)
				break
			}
		}
		if e.RemoteDNS != "" {
			break
		}
	}
	for _, dir := range []string{layout.DnsdistEtc, filepath.Join(layout.Root, "etc")} {
		path := filepath.Join(dir, ".local_dns")
		if v := readTrim(path); v != "" {
			e.LocalDNS = v
			touch(path, v)
			break
		}
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
	envPath := filepath.Join(layout.EtcRoot, "tgbot.env")
	env := readEnvFile(envPath)
	if env == nil {
		envPath = filepath.Join(layout.Root, "etc", "tgbot.env")
		env = readEnvFile(envPath)
	}
	if env != nil {
		// 5GPN-X writes TG_BOT_TOKEN. TG_TOKEN was used by an early
		// migration fixture, so retain it as a compatibility fallback.
		e.TGToken = env["TG_BOT_TOKEN"]
		if e.TGToken == "" {
			e.TGToken = env["TG_TOKEN"]
		}
		e.TGAdminIDs = env["TG_ADMIN_IDS"]
		if e.TGToken != "" {
			e.SourcePaths = append(e.SourcePaths, envPath)
		}
	}

	// Some development versions stashed the UUID separately. Released
	// 5GPN-X only stored it in the generated plist, where the final
	// PayloadUUID is the outer profile UUID.
	uuidPath := filepath.Join(layout.Root, "etc", ".ios_profile_uuid")
	if v := readTrim(uuidPath); v != "" {
		e.IOSProfileUUID = v
		touch(uuidPath, v)
	} else {
		profilePath := filepath.Join(layout.Root, "www", "ios-dot.mobileconfig")
		if v := readLastPlistString(profilePath, "PayloadUUID"); v != "" {
			e.IOSProfileUUID = v
			touch(profilePath, v)
		}
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
	adminIDs, rejectedAdminIDs := normalizeAdminIDs(e.TGAdminIDs)
	tgToken := e.TGToken
	if e.Domain == "" {
		warnings = append(warnings, "domain not found in legacy tree — new config uses placeholder panel.local")
	}
	if e.TGToken == "" {
		warnings = append(warnings, "TG_BOT_TOKEN not found — Bot will refuse to start until you fill it in")
	}
	if rejectedAdminIDs > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d invalid or duplicate TG_ADMIN_IDS value(s)", rejectedAdminIDs))
	}
	if adminIDs == "" && e.TGToken != "" {
		warnings = append(warnings, "no valid TG_ADMIN_IDS found — token omitted so the migrated daemon remains bootable")
		tgToken = ""
	}
	if len(e.Exits) == 0 {
		warnings = append(warnings, "no exits found under /etc/proxy-gateway/exits — Exits page will start empty")
	}
	if e.RemoteDNS != "" && e.LocalDNS != "" {
		warnings = append(warnings, "legacy local/remote DNS roles are combined into dns.upstreams — verify routing after migration")
	}
	for _, field := range unsupportedMigrationFields(e) {
		warnings = append(warnings, "legacy "+field+" found — it cannot be imported losslessly by the current config-only migrator")
	}

	domain := e.Domain
	if domain == "" {
		domain = "panel.local"
	}
	var b strings.Builder
	fmt.Fprintln(&b, "# migrated from legacy 5GPN-X; review installer warnings before use")
	fmt.Fprintf(&b, "server:\n  domain: %q\n  panel_bind: \"127.0.0.1\"\n  panel_port: 8443\n  tls: { cert: \"\", key: \"\" }\n\n", domain)
	fmt.Fprintf(&b, "panel:\n  session_ttl: 8h\n  rate_limit:\n    login_per_minute: 5\n    lockout_minutes: 15\n\n")
	if upstreams := legacyDNSUpstreams(e.RemoteDNS, e.LocalDNS); len(upstreams) > 0 {
		fmt.Fprintln(&b, "dns:\n  upstreams:")
		for _, upstream := range upstreams {
			fmt.Fprintf(&b, "    - %q\n", upstream)
		}
		fmt.Fprintln(&b)
	}
	// These localhost-only values are the same safe, schema-valid defaults as
	// a fresh install. Legacy exit details cannot be represented here and are
	// handled by the fail-closed partial-migration check below.
	fmt.Fprint(&b, "proxy:\n  wa_shim:\n    listen: \"127.0.0.1\"\n    port: 8447\n    backend: \"127.0.0.1:1080\"\n    wa_host: \"www.apple.com\"\n    allow_cidr:\n      - \"127.0.0.1/32\"\n    max_conn: 100\n\n")
	fmt.Fprintf(&b, "tgbot:\n  token: %q\n  admin_chat_ids: [%s]\n\n", tgToken, adminIDs)
	profileUUID := e.IOSProfileUUID
	if profileUUID == "" {
		profileUUID = "auto"
	}
	fmt.Fprintf(&b, "ios:\n  dot_domain: %q\n  http_port: 0\n  profile_uuid: %q\n", domain, profileUUID)
	return b.String(), warnings
}

// readLastPlistString reads a plist as XML and returns the final string value
// following the requested key. The released legacy profile contains nested
// and outer PayloadUUID keys; the outer profile UUID is emitted last.
func readLastPlistString(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	decoder := xml.NewDecoder(f)
	wantString := false
	last := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			return last
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return last
			}
			wantString = strings.TrimSpace(value) == key
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return last
			}
			if wantString {
				last = strings.TrimSpace(value)
			}
			wantString = false
		default:
			if wantString {
				wantString = false
			}
		}
	}
}

func legacyDNSUpstreams(values ...string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, value := range values {
		for _, upstream := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			if _, ok := seen[upstream]; ok {
				continue
			}
			seen[upstream] = struct{}{}
			out = append(out, upstream)
		}
	}
	return out
}

// unsupportedMigrationFields returns legacy state that has no lossless
// representation in config.yaml. Exits and active rules become SQLite-backed
// state in the new daemon; silently omitting them would make a successful
// migration report materially false.
func unsupportedMigrationFields(e LegacyExtract) []string {
	var fields []string
	if strings.TrimSpace(e.Rules) != "" {
		fields = append(fields, "rules")
	}
	if strings.TrimSpace(e.PolicyMap) != "" {
		fields = append(fields, "policy map")
	}
	if len(e.Exits) > 0 {
		fields = append(fields, "exits")
	}
	current := strings.TrimSpace(strings.ToLower(e.CurrentExit))
	if current != "" && current != "local" && current != "direct" {
		fields = append(fields, "active exit")
	}
	if adminIDs, _ := normalizeAdminIDs(e.TGAdminIDs); e.TGToken != "" && adminIDs == "" {
		fields = append(fields, "Telegram token without a valid admin ID")
	}
	return fields
}

// MigrateOptions controls the write path.
type MigrateOptions struct {
	Force        bool // rewrite config.yaml even if one exists
	AllowPartial bool // explicitly accept omitting SQLite-backed legacy state
}

// Migrate writes the new config.yaml from a plan. It does NOT touch the
// legacy tree; teardown of the old systemd units is the operator's call
// after they confirm the new daemon is healthy.
func Migrate(ctx context.Context, env Env, ex Executor, plan MigratePlan, opts MigrateOptions) error {
	if fields := unsupportedMigrationFields(plan.Extract); len(fields) > 0 && !opts.AllowPartial {
		return fmt.Errorf(
			"installer: refusing partial legacy migration; unsupported state present: %s (pass --allow-partial to migrate only domain, DNS, Telegram and iOS settings)",
			strings.Join(fields, ", "),
		)
	}
	if err := ensureUser(ctx, env, ex); err != nil {
		return err
	}
	if err := ensureDirs(env, ex); err != nil {
		return err
	}
	target := env.ConfigPath()
	if ex.Exists(target) && !opts.Force {
		return fmt.Errorf("installer: %s exists (pass --force to overwrite)", target)
	}
	if err := ex.WriteFile(target, []byte(plan.NewConfigYAML), 0o640); err != nil {
		return err
	}
	if err := finalizeConfigOwnership(env, ex); err != nil {
		return err
	}
	return finalizeStatePermissions(env, ex)
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

// normalizeAdminIDs returns a YAML-flow-list body containing only unique,
// non-zero signed 64-bit IDs. The second return value counts rejected or
// duplicate fields so the migration plan can surface malformed legacy data.
func normalizeAdminIDs(raw string) (string, int) {
	seen := make(map[int64]struct{})
	var valid []string
	rejected := 0
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil || id == 0 {
			rejected++
			continue
		}
		if _, ok := seen[id]; ok {
			rejected++
			continue
		}
		seen[id] = struct{}{}
		valid = append(valid, strconv.FormatInt(id, 10))
	}
	return strings.Join(valid, ", "), rejected
}
