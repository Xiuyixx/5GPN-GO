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

// handleSystemRestart queues a non-blocking systemd restart. It returns 202
// only after systemctl has accepted the job; permission/host errors are
// returned to the caller instead of being hidden in a detached goroutine.
func (s *Server) handleSystemRestart(w http.ResponseWriter, r *http.Request) {
	unit := s.Updater.Unit
	if unit == "" {
		writeError(w, http.StatusServiceUnavailable, "no_unit",
			"restart is not configured (Updater.Unit is empty)")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := updater.RestartService(ctx, unit); err != nil {
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor: actorFromCtx(r), Action: "system.restart", Target: unit,
			Result: "failed", After: err.Error(), IP: clientIP(r),
		})
		writeError(w, http.StatusInternalServerError, "restart_failed", err.Error())
		return
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actorFromCtx(r), Action: "system.restart", Target: unit,
		Result: "queued", IP: clientIP(r),
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "unit": unit})
}
