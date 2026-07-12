//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// socket paths — prefer /run/5gpn/ (systemd RuntimeDirectory), fall back
// to /tmp/5gpn.sock which the server side uses when /run/5gpn is
// unavailable.
const (
	primarySocket  = "/run/5gpn/ctl.sock"
	fallbackSocket = "/tmp/5gpn.sock"
	dialTimeout    = 5 * time.Second
	rwTimeout      = 30 * time.Second
)

// run dials the daemon socket, writes one JSON-line request, reads one
// JSON-line response, and prints a human-readable rendering.
func run(req request) (int, error) {
	conn, err := dial()
	if err != nil {
		return 1, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(rwTimeout)); err != nil {
		return 1, err
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return 1, fmt.Errorf("write request: %w", err)
	}

	dec := json.NewDecoder(bufio.NewReader(conn))
	var resp response
	if err := dec.Decode(&resp); err != nil {
		return 1, fmt.Errorf("read response: %w", err)
	}
	if !resp.OK {
		return 1, fmt.Errorf("daemon: %s", resp.Error)
	}
	render(req.Cmd, resp.Data)
	return 0, nil
}

func dial() (net.Conn, error) {
	for _, path := range []string{primarySocket, fallbackSocket} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		conn, err := net.DialTimeout("unix", path, dialTimeout)
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("cannot reach 5gpn daemon (tried %s and %s) — is 5gpn running?", primarySocket, fallbackSocket)
}

// render prints the response payload in a shape that fits each cmd.
// Falls back to pretty-JSON for anything not specifically formatted.
func render(cmd string, data map[string]any) {
	switch cmd {
	case "version":
		if v, ok := data["version"].(string); ok {
			fmt.Println(v)
			return
		}
	case "status":
		fmt.Printf("version:      %v\n", data["version"])
		fmt.Printf("uptime:       %v\n", data["uptime"])
		fmt.Printf("active_exit:  %v\n", data["active_exit"])
		fmt.Printf("rule_count:   %v\n", data["rule_count"])
		return
	case "exits.list":
		items, _ := data["exits"].([]any)
		if len(items) == 0 {
			fmt.Println("(no exits)")
			return
		}
		fmt.Printf("%-20s %-10s %-6s %s\n", "ID", "PROTOCOL", "ACTIVE", "URI")
		for _, raw := range items {
			e, _ := raw.(map[string]any)
			active := ""
			if b, _ := e["active"].(bool); b {
				active = "*"
			}
			fmt.Printf("%-20v %-10v %-6s %v\n",
				e["exit_id"], e["protocol"], active, e["uri"])
		}
		return
	case "exits.switch":
		fmt.Printf("switched to exit %v (snapshot=%v, health=%v)\n",
			data["exit_id"], data["snapshot_id"], data["health"])
		return
	case "rules.rollback":
		fmt.Printf("rolled back to snapshot %v (rule_version=%v)\n",
			data["snapshot_id"], data["rule_version_id"])
		return
	case "chinalist.sync":
		fmt.Printf("chinalist synced (source=%v, path=%v)\n",
			data["source"], data["path"])
		return
	}
	// Generic fallback.
	body, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(body))
}
