// Command 5gpn-installer is the Go replacement for the legacy 3.3k-line
// install.sh. All heavy lifting lives in internal/installer; this file
// dispatches subcommands and translates flags. Every mutating verb accepts
// --dry-run to preview the ops without touching the host.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Xiuyixx/5GPN-Go/internal/installer"
)

var version = "0.4.0"

func usage() {
	fmt.Fprint(os.Stderr, `5gpn-installer <subcommand> [flags]

subcommands:
  install               Provision user, dirs, config, systemd unit, and start the daemon.
  upgrade               Blue-green swap the daemon binary (keeps .prev fallback).
  uninstall             Stop unit, remove binary + unit; --purge also wipes state.
  status                Print unit health.
  doctor                Diagnose host prerequisites (read-only).
  migrate-from-legacy   Extract config from a legacy 5GPN-X install and render new config.yaml.
  version               Print version and exit.

common flags:
  --dry-run   Print planned operations without executing them.
  --root DIR  Re-root paths for controlled dry-runs/tests; not a rootless install.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	rest := os.Args[2:]

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch sub {
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	case "install":
		os.Exit(runInstall(ctx, rest))
	case "upgrade":
		os.Exit(runUpgrade(ctx, rest))
	case "uninstall":
		os.Exit(runUninstall(ctx, rest))
	case "status":
		os.Exit(runStatus(ctx, rest))
	case "doctor":
		os.Exit(runDoctor(ctx, rest))
	case "migrate-from-legacy":
		os.Exit(runMigrate(ctx, rest))
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

type commonFlags struct {
	dryRun    bool
	root      string
	osFixture string
}

func (c *commonFlags) bind(fs *flag.FlagSet) {
	fs.BoolVar(&c.dryRun, "dry-run", false, "print actions without executing")
	fs.StringVar(&c.root, "root", "", "re-root paths under DIR (controlled dry-runs/tests; not rootless)")
	fs.StringVar(&c.osFixture, "os-fixture", "",
		"path to an os-release fixture file (bypass live detection; used in CI + preview)")
}

func (c *commonFlags) distro() (installer.Distro, error) {
	if c.osFixture != "" {
		return installer.LoadOSFixture(c.osFixture)
	}
	d, err := installer.DetectDistro()
	if err != nil {
		// Non-linux dev machines: return zero value; callers decide whether to abort.
		return installer.Distro{}, nil
	}
	return d, nil
}

func (c *commonFlags) executor() installer.Executor {
	if c.dryRun {
		rec := installer.NewRecorder()
		rec.Out = os.Stdout
		return rec
	}
	return &installer.RealExecutor{Out: os.Stdout, Err: os.Stderr}
}

func (c *commonFlags) env() installer.Env {
	env := installer.Defaults()
	if c.root != "" {
		env = env.WithRoot(c.root)
	}
	return env
}

func runInstall(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	source := fs.String("source-binary", "", "path to the compiled 5gpn binary (empty = binary is already in place)")
	force := fs.Bool("force", false, "rewrite existing config.yaml")
	skipUnit := fs.Bool("skip-unit", false, "do not write the systemd unit")
	skipEnable := fs.Bool("skip-enable", false, "do not enable+start the unit")
	_ = fs.Parse(args)

	d, err := cf.distro()
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	if d.ID != "" {
		fmt.Printf("distro: %s\n", d)
	}
	if cf.dryRun {
		fmt.Println("[dry-run] install")
	}
	err = installer.Install(ctx, cf.env(), cf.executor(), installer.InstallOptions{
		Force:        *force,
		SkipUnit:     *skipUnit,
		SkipEnable:   *skipEnable,
		SourceBinary: *source,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	fmt.Println("install: ok")
	return 0
}

func runUpgrade(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	newBin := fs.String("new", "", "path to the new binary artefact (required)")
	skipRestart := fs.Bool("skip-restart", false, "do not restart the unit after swap")
	_ = fs.Parse(args)

	if *newBin == "" {
		fmt.Fprintln(os.Stderr, "upgrade: --new required")
		return 2
	}
	if cf.dryRun {
		fmt.Println("[dry-run] upgrade")
	}
	err := installer.Upgrade(ctx, cf.env(), cf.executor(), installer.UpgradeOptions{
		NewBinary:   *newBin,
		SkipRestart: *skipRestart,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "upgrade:", err)
		return 1
	}
	fmt.Println("upgrade: ok")
	return 0
}

func runUninstall(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	purge := fs.Bool("purge", false, "also delete config + state dirs")
	_ = fs.Parse(args)

	if cf.dryRun {
		fmt.Println("[dry-run] uninstall")
	}
	if err := installer.Uninstall(ctx, cf.env(), cf.executor(), *purge); err != nil {
		fmt.Fprintln(os.Stderr, "uninstall:", err)
		return 1
	}
	fmt.Println("uninstall: ok")
	return 0
}

func runStatus(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	_ = fs.Parse(args)
	if err := installer.Status(ctx, cf.env(), cf.executor(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 1
	}
	return 0
}

func runMigrate(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("migrate-from-legacy", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	legacyRoot := fs.String("legacy-root", "",
		"re-root legacy paths (/opt/proxy-gateway, /etc/proxy-gateway, /etc/dnsdist) under DIR")
	force := fs.Bool("force", false, "overwrite existing config.yaml")
	allowPartial := fs.Bool("allow-partial", false,
		"migrate only config-backed fields even when legacy rules/exits would be omitted")
	_ = fs.Parse(args)

	layout := installer.LegacyDefaults()
	if *legacyRoot != "" {
		layout = layout.WithRoot(*legacyRoot)
	}

	plan, err := installer.Plan(layout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}

	fmt.Println("--- extracted from legacy tree ---")
	fmt.Printf("  domain:          %s\n", or(plan.Extract.Domain, "(none)"))
	fmt.Printf("  remote dns:      %s\n", or(plan.Extract.RemoteDNS, "(none)"))
	fmt.Printf("  local dns:       %s\n", or(plan.Extract.LocalDNS, "(none)"))
	fmt.Printf("  current exit:    %s\n", or(plan.Extract.CurrentExit, "(none)"))
	fmt.Printf("  exits:           %d\n", len(plan.Extract.Exits))
	fmt.Printf("  rules:           %s\n", present(plan.Extract.Rules))
	fmt.Printf("  policy map:      %s\n", present(plan.Extract.PolicyMap))
	fmt.Printf("  tg token:        %s\n", redact(plan.Extract.TGToken))
	fmt.Printf("  tg admin ids:    %s\n", or(plan.Extract.TGAdminIDs, "(none)"))
	fmt.Printf("  ios profile uuid:%s\n", or(plan.Extract.IOSProfileUUID, "(none)"))
	fmt.Printf("  sources read:    %d\n", len(plan.Extract.SourcePaths))

	if len(plan.Warnings) > 0 {
		fmt.Println("--- warnings ---")
		for _, w := range plan.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
	}

	if cf.dryRun {
		fmt.Println("--- proposed config.yaml ---")
		fmt.Println(plan.NewConfigYAML)
		fmt.Println("[dry-run] not writing.")
		return 0
	}

	if err := installer.Migrate(ctx, cf.env(), cf.executor(), plan, installer.MigrateOptions{
		Force:        *force,
		AllowPartial: *allowPartial,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	fmt.Println("migrate: ok")
	return 0
}

func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func redact(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) <= 6 {
		return "***"
	}
	return s[:3] + "…" + s[len(s)-3:]
}

func present(s string) string {
	if s == "" {
		return "(none)"
	}
	return "present"
}

func runDoctor(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	var cf commonFlags
	cf.bind(fs)
	_ = fs.Parse(args)
	d, err := cf.distro()
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor:", err)
		return 1
	}
	report := installer.Doctor(ctx, cf.env(), cf.executor(), d)
	fails := 0
	for _, c := range report.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
			fails++
		}
		fmt.Printf("  [%s] %s — %s\n", mark, c.Name, c.Detail)
	}
	if fails > 0 {
		return 1
	}
	return 0
}
