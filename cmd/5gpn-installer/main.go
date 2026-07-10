// Command 5gpn-installer is the Go replacement for the legacy 130KB install.sh.
//
// M0 skeleton: dispatches subcommands and prints TODO; M3 lands real logic.
package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "0.0.0-m0"

func usage() {
	fmt.Fprint(os.Stderr, `5gpn-installer <subcommand> [flags]

subcommands:
  install               Provision the daemon, three-party components, and initial config.
  upgrade               Fetch a newer 5gpn binary and swap it in blue-green.
  rollback              Restore a prior snapshot or a checkpointed install step.
  rollback-to-legacy    Stop the new daemon and restore the legacy 5GPN-X install.
  uninstall             Remove the daemon, systemd units, and (optionally) data.
  status                Show unit health and health-check output.
  doctor                Diagnose common misconfigurations.
  migrate-from-legacy   Import DOMAIN, TG token, rules, iOS profile UUID from /opt/5gpn.
  version               Print version and exit.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print actions without executing")
	_ = fs.Parse(os.Args[1:])

	switch sub {
	case "version":
		fmt.Println(version)
	case "install", "upgrade", "rollback", "rollback-to-legacy",
		"uninstall", "status", "doctor", "migrate-from-legacy":
		fmt.Printf("TODO: %s (dry-run=%v) — M3 will implement\n", sub, *dryRun)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}
