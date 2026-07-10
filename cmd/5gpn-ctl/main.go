// Command 5gpn-ctl is a small local CLI for admin tasks that do not require the daemon.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0-m0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: 5gpn-ctl <version|config-validate|snapshot-list>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "config-validate", "snapshot-list":
		fmt.Printf("TODO: %s — M1/M2 will implement\n", os.Args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
