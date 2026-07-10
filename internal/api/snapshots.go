package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

type snapshotEntry struct {
	ID         int64  `json:"id"`
	CreatedAt  string `json:"created_at"`
	ConfigHash string `json:"config_hash"`
	Note       string `json:"note,omitempty"`
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	rows, err := db.ListSnapshots(s.DB, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]snapshotEntry, len(rows))
	for i, r := range rows {
		out[i] = snapshotEntry{
			ID:         r.ID,
			CreatedAt:  r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			ConfigHash: r.ConfigHash,
			Note:       r.Note,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (s *Server) handleRollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "id must be integer")
		return
	}
	snap, err := db.GetSnapshotByID(s.DB, id)
	if err == db.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "no snapshot with id "+idStr)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// Find the rule_version tied to this snapshot and reactivate it.
	versions, err := db.ListRuleVersions(s.DB, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	var targetID int64
	for _, v := range versions {
		if v.SnapshotID == snap.ID {
			targetID = v.ID
			break
		}
	}
	if targetID == 0 {
		writeError(w, http.StatusConflict, "no_rules", "snapshot has no rule_version to reactivate")
		return
	}
	if err := db.SetActiveRuleVersion(s.DB, targetID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	actor, _ := r.Context().Value(ctxUsername).(string)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "rules.rollback",
		Target: snap.ConfigHash,
		Result: "ok",
		IP:     clientIP(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot_id":     snap.ID,
		"rule_version_id": targetID,
	})
}

// handleExportBackup streams a tar.gz containing:
// - rules/active.yaml (current active rule_version)
// - snapshots/manifest.json (last 200 snapshot rows)
// - db/5gpn.db (SQLite hot-copy via VACUUM INTO — atomic + WAL-safe)
// - README.txt
func (s *Server) handleExportBackup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="5gpn-backup.tar.gz"`)

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	writeMember := func(name string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(body)),
		}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}

	// Active rules
	if active, err := db.GetActiveRuleVersion(s.DB); err == nil {
		_ = writeMember("rules/active.yaml", []byte(active.RulesYAML))
	}

	// Snapshot manifest
	snaps, err := db.ListSnapshots(s.DB, 200)
	if err == nil {
		body, _ := json.MarshalIndent(snaps, "", "  ")
		_ = writeMember("snapshots/manifest.json", body)
	}

	// SQLite hot copy via VACUUM INTO — atomic, does not require
	// stopping writes, and gives us a WAL-consistent file we can put
	// directly into the tar member.
	if body, err := hotCopyDB(r.Context(), s.DB); err == nil {
		_ = writeMember("db/5gpn.db", body)
	} else {
		s.Logger.Warn("backup: hotcopy failed", "err", err)
	}

	_ = writeMember("README.txt", []byte(fmt.Sprintf(
		"5GPN backup export\ngenerated: %s\ncontains: rules/active.yaml, snapshots/manifest.json, db/5gpn.db\n",
		r.Header.Get("Date"),
	)))
}

// applyResultPayload is the machine-readable outcome of the Applier.ImportRules
// call embedded in the import response. Callers use it to distinguish
// confirmed / observing / rolled_back without polling apply_status.
type applyResultPayload struct {
	Health     string `json:"health"`
	RolledBack bool   `json:"rolled_back"`
	Reason     string `json:"reason,omitempty"`
}

// handleImportBackup accepts a tar.gz backup and applies its rules/active.yaml
// member through core.Applier.ImportRules — the same pipeline used by the
// panel's rules apply endpoint. On any failure the active rule_version
// pointer is restored to whatever was active before the import, a
// backup.restore.rolled_back audit entry is written, and the caller gets
// a non-2xx response so "applied" can never mean "DB advanced but data
// plane unchanged".
func (s *Server) handleImportBackup(w http.ResponseWriter, r *http.Request) {
	if s.Applier == nil {
		writeError(w, http.StatusInternalServerError, "applier_missing", "applier not wired")
		return
	}
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_gzip", err.Error())
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := 0
	var totalSize int64
	var applied bool
	var appliedSnapshotID int64
	var applyPayload *applyResultPayload

	actor, _ := r.Context().Value(ctxUsername).(string)
	ip := clientIP(r)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_tar", err.Error())
			return
		}
		entries++
		totalSize += hdr.Size

		switch hdr.Name {
		case "rules/active.yaml":
			body, err := io.ReadAll(tr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_tar", err.Error())
				return
			}
			if len(body) == 0 {
				continue
			}

			// Capture the prior active rule_version so we can restore
			// the pointer if the import's apply pipeline fails.
			var priorRVID int64
			if prior, perr := db.GetActiveRuleVersion(s.DB); perr == nil {
				priorRVID = prior.ID
			}

			res, appErr := s.Applier.ImportRules(r.Context(), string(body), actor, ip)
			if appErr != nil || res.RolledBack {
				reason := ""
				if appErr != nil {
					reason = appErr.Error()
				} else {
					reason = res.Reason
				}
				// Best-effort: reactivate the prior active version so the
				// DB matches the pre-import state. If there was no prior
				// active version, leave whatever the applier ended up in
				// place — SetActiveRuleVersion(0) is a no-op.
				if priorRVID != 0 {
					_ = db.SetActiveRuleVersion(s.DB, priorRVID)
				}
				_ = db.AppendAudit(s.DB, db.AuditEntry{
					Actor:  actor,
					Action: "backup.restore.rolled_back",
					Target: "rules/active.yaml",
					Result: reason,
					IP:     ip,
				})
				writeError(w, http.StatusInternalServerError, "import_failed", reason)
				return
			}

			applied = true
			appliedSnapshotID = res.SnapshotID
			applyPayload = &applyResultPayload{
				Health:     res.Health,
				RolledBack: res.RolledBack,
				Reason:     res.Reason,
			}
			_ = db.AppendAudit(s.DB, db.AuditEntry{
				Actor: actor, Action: "backup.restore",
				Target: "rules/active.yaml", Result: "ok", IP: ip,
			})
		default:
			// Drain unknown members; we accept them structurally but
			// don't apply them.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				writeError(w, http.StatusBadRequest, "bad_tar", err.Error())
				return
			}
		}
	}
	resp := map[string]any{
		"entries":     entries,
		"total_bytes": totalSize,
		"applied":     applied,
	}
	if appliedSnapshotID != 0 {
		resp["applied_snapshot_id"] = appliedSnapshotID
	}
	if applyPayload != nil {
		resp["apply_result"] = applyPayload
	}
	writeJSON(w, http.StatusOK, resp)
}

// hotCopyDB creates a WAL-safe snapshot of the panel SQLite file via
// VACUUM INTO into a temp path, reads the bytes, and cleans up.
func hotCopyDB(ctx context.Context, handle *sql.DB) ([]byte, error) {
	tmp, err := os.CreateTemp("", "5gpn-backup-*.db")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(path)
	defer os.Remove(path)

	if _, err := handle.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
