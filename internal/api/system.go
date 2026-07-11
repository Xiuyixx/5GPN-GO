// System-level endpoints: currently just the panel-driven daemon
// restart used by the wizard's "restart to apply" flow.

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/updater"
)

// handleSystemRestart triggers a non-blocking systemd restart of the
// 5gpn unit. Structurally identical to the one-click upgrade path
// (write the response, flush it, then fire the restart in a detached
// goroutine so the response reaches the client before systemd yanks
// the process). Requires the daemon to run under systemd — otherwise
// the systemctl call is a no-op and the caller sees no restart.
func (s *Server) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	unit := s.Updater.Unit
	if unit == "" {
		writeError(w, http.StatusServiceUnavailable, "no_unit",
			"restart is not configured (Updater.Unit is empty)")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"restarting":       true,
		"unit":             unit,
		"drain_ms":         300,
		"restart_deadline": 10,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actorFromCtx(r), Action: "system.restart", Target: unit,
		Result: "queued", IP: clientIP(r),
	})

	logger := s.Logger
	go func() {
		time.Sleep(300 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := updater.RestartService(ctx, unit); err != nil && logger != nil {
			logger.Warn("system.restart: RestartService failed", "unit", unit, "err", err)
		}
	}()
}
