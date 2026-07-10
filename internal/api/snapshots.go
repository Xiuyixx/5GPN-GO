package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// handleExportBackup streams a tar.gz containing configs + active rules +
// snapshots metadata JSON. M2 S4 will add SQLite hot-copy via VACUUM INTO.
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

	// A small README with regenerable instructions.
	_ = writeMember("README.txt", []byte(fmt.Sprintf(
		"5GPN backup export\ngenerated: %s\ncontains: active rules yaml + snapshot manifest\nM2 S4 will add SQLite hot-copy + config.yaml.\n",
		r.Header.Get("Date"),
	)))
}

// handleImportBackup is a stub that just accepts + counts the tarball
// entries so the UI can validate a round-trip. M2 S4 does the real
// restore + orchestrator reload.
func (s *Server) handleImportBackup(w http.ResponseWriter, r *http.Request) {
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_gzip", err.Error())
		return
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := 0
	var totalSize int64
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
		if _, err := io.Copy(io.Discard, tr); err != nil {
			writeError(w, http.StatusBadRequest, "bad_tar", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":     entries,
		"total_bytes": totalSize,
		"applied":     false,
		"note":        "M2 S2 stub — S4 wires the real restore",
	})
}
