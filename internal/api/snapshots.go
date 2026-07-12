package api

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

const (
	backupMaxCompressedBytes = 32 << 20
	backupMaxEntries         = 256
	backupMaxTotalBytes      = 128 << 20
	backupMaxMemberBytes     = 128 << 20
	backupMaxRulesBytes      = 8 << 20
)

type snapshotEntry struct {
	ID           int64  `json:"id"`
	CreatedAt    string `json:"created_at"`
	ConfigHash   string `json:"config_hash"`
	Note         string `json:"note,omitempty"`
	Active       bool   `json:"active"`
	Rollbackable bool   `json:"rollbackable"`
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
	rollbackable, err := rollbackableSnapshotIDs(s.DB, rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]snapshotEntry, len(rows))
	activeSnapshotID := int64(0)
	if active, activeErr := db.GetActiveRuleVersion(s.DB); activeErr == nil {
		activeSnapshotID = active.SnapshotID
	} else if !errors.Is(activeErr, db.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "db_error", activeErr.Error())
		return
	}
	for i, r := range rows {
		_, canRollback := rollbackable[r.ID]
		out[i] = snapshotEntry{
			ID:           r.ID,
			CreatedAt:    r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			ConfigHash:   r.ConfigHash,
			Note:         r.Note,
			Active:       r.ID == activeSnapshotID,
			Rollbackable: canRollback,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func rollbackableSnapshotIDs(handle *sql.DB, snapshots []db.Snapshot) (map[int64]struct{}, error) {
	out := make(map[int64]struct{}, len(snapshots))
	if len(snapshots) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(snapshots))
	args := make([]any, len(snapshots))
	for i, snapshot := range snapshots {
		placeholders[i] = "?"
		args[i] = snapshot.ID
	}
	rows, err := handle.Query(
		`SELECT DISTINCT snapshot_id FROM rule_versions WHERE snapshot_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
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
	target, err := db.GetRuleVersionBySnapshot(s.DB, snap.ID)
	if errors.Is(err, db.ErrNoRows) {
		writeError(w, http.StatusConflict, "no_rules", "snapshot has no rule_version to reactivate")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	targetID := target.ID
	targetSet, err := rules.ParseYAML([]byte(target.RulesYAML))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rules_corrupt", err.Error())
		return
	}
	if err := targetSet.Validate(); err != nil {
		writeError(w, http.StatusInternalServerError, "rules_corrupt", err.Error())
		return
	}
	var prevSnapshot int64
	if active, activeErr := db.GetActiveRuleVersion(s.DB); activeErr == nil {
		prevSnapshot = active.SnapshotID
	} else if !errors.Is(activeErr, db.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "db_error", activeErr.Error())
		return
	}
	appRes, appErr := s.Applier.ApplyRules(r.Context(), snap.ID, targetID, prevSnapshot)
	if appErr != nil {
		if errors.Is(appErr, orchestrator.ErrApplyInFlight) {
			writeError(w, http.StatusConflict, "apply_in_flight", appErr.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "rollback_failed", appErr.Error())
		}
		return
	}
	entry := s.trackRuleApply(targetSet, "rollback", appRes)
	actor, _ := r.Context().Value(ctxUsername).(string)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "rules.rollback",
		Target: snap.ConfigHash,
		Result: map[bool]string{true: "observing", false: "ok"}[appRes.Health == "observing"],
		IP:     clientIP(r),
	})

	response := map[string]any{
		"snapshot_id":     snap.ID,
		"rule_version_id": targetID,
		"apply_id":        entry.ID,
		"status":          entry.Status,
		"hash":            entry.Hash,
		"health":          appRes.Health,
		"rolled_back":     appRes.RolledBack,
		"reason":          appRes.Reason,
	}
	if entry.Status == "pending" {
		w.Header().Set("Location", "/api/v1/applies/"+entry.ID)
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleExportBackup streams a tar.gz containing:
// - rules/active.yaml (current active rule_version)
// - snapshots/manifest.json (last 200 snapshot rows)
// - db/5gpn.db (SQLite hot-copy via VACUUM INTO — atomic + WAL-safe)
// - README.txt
func (s *Server) handleExportBackup(w http.ResponseWriter, r *http.Request) {
	path, err := buildBackupArchive(r.Context(), s.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	defer func() { _ = os.Remove(path) }()
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="5gpn-backup.tar.gz"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "5gpn-backup.tar.gz", info.ModTime(), f)
}

func buildBackupArchive(ctx context.Context, handle *sql.DB) (path string, retErr error) {
	if err := checkDBIntegrity(ctx, handle); err != nil {
		return "", err
	}
	active, err := db.GetActiveRuleVersion(handle)
	if err != nil && !errors.Is(err, db.ErrNoRows) {
		return "", fmt.Errorf("read active rules: %w", err)
	}
	snaps, err := db.ListSnapshots(handle, 200)
	if err != nil {
		return "", fmt.Errorf("list snapshots: %w", err)
	}
	manifest, err := json.MarshalIndent(snaps, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal snapshot manifest: %w", err)
	}
	dbCopy, err := hotCopyDB(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("SQLite hot copy: %w", err)
	}

	tmp, err := os.CreateTemp("", "5gpn-backup-*.tar.gz")
	if err != nil {
		return "", err
	}
	path = tmp.Name()
	tmpPath := path
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	writeMember := func(name string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now()}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	activeRules := []byte("rules: []\n")
	if active != nil {
		activeRules = []byte(active.RulesYAML)
	}
	if err := writeMember("rules/active.yaml", activeRules); err != nil {
		retErr = err
	}
	if retErr == nil {
		retErr = writeMember("snapshots/manifest.json", manifest)
	}
	if retErr == nil {
		retErr = writeMember("db/5gpn.db", dbCopy)
	}
	if retErr == nil {
		retErr = writeMember("README.txt", []byte(
			"5GPN disaster-recovery export\n"+
				"The panel import endpoint applies rules/active.yaml only.\n"+
				"db/5gpn.db and snapshots/manifest.json are for offline recovery by an administrator.\n"+
				"This archive is plaintext and contains secrets. Store it securely.\n"))
	}
	if err := tw.Close(); retErr == nil && err != nil {
		retErr = err
	}
	if err := gz.Close(); retErr == nil && err != nil {
		retErr = err
	}
	if err := tmp.Sync(); retErr == nil && err != nil {
		retErr = err
	}
	if err := tmp.Close(); retErr == nil && err != nil {
		retErr = err
	}
	if retErr != nil {
		return "", fmt.Errorf("build backup archive: %w", retErr)
	}
	return path, nil
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
	if r.ContentLength > backupMaxCompressedBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "backup_too_large", "compressed backup exceeds 32 MiB")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, backupMaxCompressedBytes)
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_gzip", err.Error())
		return
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	entries := 0
	var totalSize int64
	seen := make(map[string]struct{})
	var rulesYAML []byte
	ignored := make([]string, 0, 3)

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
		if entries > backupMaxEntries {
			writeError(w, http.StatusBadRequest, "backup_limits", "backup has too many members")
			return
		}
		if !hdr.FileInfo().Mode().IsRegular() {
			writeError(w, http.StatusBadRequest, "bad_tar", "backup members must be regular files")
			return
		}
		if hdr.Size < 0 || hdr.Size > backupMaxMemberBytes {
			writeError(w, http.StatusBadRequest, "backup_limits", "backup member exceeds size limit")
			return
		}
		totalSize += hdr.Size
		if totalSize > backupMaxTotalBytes {
			writeError(w, http.StatusBadRequest, "backup_limits", "expanded backup exceeds 128 MiB")
			return
		}
		if _, duplicate := seen[hdr.Name]; duplicate {
			writeError(w, http.StatusBadRequest, "duplicate_member", "duplicate backup member: "+hdr.Name)
			return
		}
		seen[hdr.Name] = struct{}{}

		switch hdr.Name {
		case "rules/active.yaml":
			if hdr.Size > backupMaxRulesBytes {
				writeError(w, http.StatusBadRequest, "backup_limits", "rules/active.yaml exceeds 8 MiB")
				return
			}
			body, err := io.ReadAll(io.LimitReader(tr, backupMaxRulesBytes+1))
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_tar", err.Error())
				return
			}
			if len(body) == 0 || len(body) > backupMaxRulesBytes {
				writeError(w, http.StatusBadRequest, "bad_rules", "rules/active.yaml is empty or too large")
				return
			}
			rulesYAML = body
		case "snapshots/manifest.json", "db/5gpn.db", "README.txt":
			ignored = append(ignored, hdr.Name)
			if _, err := io.Copy(io.Discard, tr); err != nil {
				writeError(w, http.StatusBadRequest, "bad_tar", err.Error())
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "unknown_member", "unknown backup member: "+hdr.Name)
			return
		}
	}
	if len(rulesYAML) == 0 {
		writeError(w, http.StatusBadRequest, "rules_missing", "backup has no rules/active.yaml member")
		return
	}
	set, err := rules.ParseYAML(rulesYAML)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_rules", err.Error())
		return
	}
	if err := set.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_rules", err.Error())
		return
	}

	actor, _ := r.Context().Value(ctxUsername).(string)
	ip := clientIP(r)
	res, appErr := s.Applier.ImportRules(r.Context(), string(rulesYAML), actor, ip)
	if appErr != nil || res.RolledBack {
		reason := res.Reason
		if appErr != nil {
			reason = appErr.Error()
		}
		_ = db.AppendAudit(s.DB, db.AuditEntry{Actor: actor, Action: "backup.restore.rolled_back", Target: "rules/active.yaml", Result: reason, IP: ip})
		writeError(w, http.StatusInternalServerError, "import_failed", reason)
		return
	}
	entry := s.trackRuleApply(set, "import", res)
	health := "confirmed"
	pending := entry.Status == "pending"
	if pending {
		health = "observing"
	}
	applyPayload := &applyResultPayload{Health: health, RolledBack: false, Reason: res.Reason}
	_ = db.AppendAudit(s.DB, db.AuditEntry{Actor: actor, Action: "backup.restore", Target: "rules/active.yaml", Result: health, IP: ip})
	resp := map[string]any{
		"entries":         entries,
		"total_bytes":     totalSize,
		"applied":         !pending,
		"pending":         pending,
		"ignored_entries": ignored,
		"note":            "panel import restores rules/active.yaml only; database and manifest members are offline recovery artifacts",
		"apply_result":    applyPayload,
		"apply_id":        entry.ID,
		"status":          entry.Status,
	}
	resp["applied_snapshot_id"] = res.SnapshotID
	if pending {
		w.Header().Set("Location", "/api/v1/applies/"+entry.ID)
		writeJSON(w, http.StatusAccepted, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func checkDBIntegrity(ctx context.Context, handle *sql.DB) error {
	var result string
	if err := handle.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("PRAGMA integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("PRAGMA integrity_check: %s", result)
	}
	return nil
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
	defer func() { _ = os.Remove(path) }()

	if _, err := handle.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
