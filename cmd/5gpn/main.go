// Command 5gpn is the personal-gateway daemon entrypoint.
//
// M0 skeleton: parses flags and prints a startup banner.
// M1 will wire the API server, orchestrator, and TG bot.
package main

import (
	"flag"
	"fmt"

	"github.com/Xiuyixx/5GPN-Go/internal/web"
)

var version = "0.0.0-m0"

func main() {
	configPath := flag.String("config", "/etc/5gpn/config.yaml", "path to config.yaml")
	dataDir := flag.String("data", "/var/lib/5gpn", "state directory (SQLite, snapshots, keys)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	fmt.Printf("5gpn daemon starting version=%s config=%s data=%s\n",
		version, *configPath, *dataDir)

	if web.FS == nil {
		fmt.Println("(embed): panel bundle not embedded; build with -tags embed to include internal/web/dist")
	} else {
		fmt.Println("(embed): panel bundle embedded")
	}
}
