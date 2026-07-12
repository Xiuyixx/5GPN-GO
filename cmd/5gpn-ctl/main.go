// Command 5gpn-ctl is a local admin CLI that talks to the running 5gpn
// daemon over a Unix socket. Authentication is performed via SO_PEERCRED
// on the server side — only the daemon's uid (or root) may issue
// commands. There is no token, no config file, no environment variable.
//
// Wire protocol (JSON-line, one request/response per connection):
//
//	>>> {"cmd": "<name>", "args": { ... }}
//	<<< {"ok": true|false, "error": "...", "data": { ... }}
//
// Subcommands:
//
//	status                   daemon uptime + version + active exit + rule count
//	version                  daemon version string
//	exits list               JSON exit list
//	exits switch <exit-id>   flip the active exit
//	rules rollback           rollback to the previous snapshot
//	chinalist sync           refresh the chinalist file
//
// The client is Linux-only. Building on darwin produces a stub binary
// that prints a clear message and exits non-zero (see ctl_darwin.go).
package main

import (
	"fmt"
	"os"
)

// version is stamped at build time. It has no coupling to the
// daemon version — the daemon reports its own via the "version" cmd.
var version = "0.4.0"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	code, err := dispatch(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func usage(w *os.File) {
	_, _ = fmt.Fprintln(w, "usage: 5gpn-ctl <subcommand> [args]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "subcommands:")
	_, _ = fmt.Fprintln(w, "  status                   daemon status summary")
	_, _ = fmt.Fprintln(w, "  version                  daemon version")
	_, _ = fmt.Fprintln(w, "  exits list               list exits")
	_, _ = fmt.Fprintln(w, "  exits switch <id>        switch active exit")
	_, _ = fmt.Fprintln(w, "  rules rollback           rollback to prior snapshot")
	_, _ = fmt.Fprintln(w, "  chinalist sync           refresh chinalist")
	_, _ = fmt.Fprintln(w, "  --version                print client version")
}

// dispatch parses argv (without argv[0]) into a wire request and hands
// it to run(). Returns process exit code + optional error to print.
// run() is defined per-OS in ctl_linux.go / ctl_darwin.go.
func dispatch(args []string) (int, error) {
	switch args[0] {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0, nil
	case "--version":
		_, _ = fmt.Println(version)
		return 0, nil
	case "status":
		return run(request{Cmd: "status"})
	case "version":
		return run(request{Cmd: "version"})
	case "exits":
		if len(args) < 2 {
			return 2, fmt.Errorf("exits: missing subcommand (list|switch)")
		}
		switch args[1] {
		case "list":
			return run(request{Cmd: "exits.list"})
		case "switch":
			if len(args) < 3 {
				return 2, fmt.Errorf("exits switch: missing <exit-id>")
			}
			return run(request{Cmd: "exits.switch", Args: map[string]any{"exit_id": args[2]}})
		default:
			return 2, fmt.Errorf("exits: unknown subcommand %q", args[1])
		}
	case "rules":
		if len(args) < 2 {
			return 2, fmt.Errorf("rules: missing subcommand (rollback)")
		}
		switch args[1] {
		case "rollback":
			return run(request{Cmd: "rules.rollback"})
		default:
			return 2, fmt.Errorf("rules: unknown subcommand %q", args[1])
		}
	case "chinalist":
		if len(args) < 2 {
			return 2, fmt.Errorf("chinalist: missing subcommand (sync)")
		}
		switch args[1] {
		case "sync":
			return run(request{Cmd: "chinalist.sync"})
		default:
			return 2, fmt.Errorf("chinalist: unknown subcommand %q", args[1])
		}
	default:
		return 2, fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// request is the wire type. Kept in main.go so both the
// linux client and the darwin stub (which never actually sends one)
// compile against the same shape.
type request struct {
	Cmd  string         `json:"cmd"`
	Args map[string]any `json:"args,omitempty"`
}

// response mirrors cmd/5gpn/ctl_socket_linux.go's daemon-side type.
// Kept in main.go for the same reason as request: the darwin stub
// references it too, so it must live outside the linux-only file.
type response struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}
