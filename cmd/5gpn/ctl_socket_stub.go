//go:build !linux

package main

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/Xiuyixx/5GPN-Go/internal/core"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
)

// startCtlSocket is a no-op on non-linux hosts. SO_PEERCRED is a Linux
// syscall so we do not expose the ctl socket outside of production.
// Dev on macOS keeps compiling without stubbing anything else out.
func startCtlSocket(_ context.Context, _ *core.Applier, _ xexit.Store, _ *sql.DB, _ *slog.Logger) {
}
