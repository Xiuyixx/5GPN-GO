package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/updater"
)

// updateCheckResponse is what GET /api/v1/update/check returns. current
// is the running daemon's build tag; latest is the tag of the most recent
// non-draft GitHub release. When they match, has_update == false.
type updateCheckResponse struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	HasUpdate bool   `json:"has_update"`
	Asset     string `json:"asset,omitempty"`
	Checksum  string `json:"checksum,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.Updater.Client == nil {
		writeError(w, http.StatusServiceUnavailable, "updater_disabled", "updater is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rel, err := s.Updater.Client.Latest(ctx)
	if err != nil {
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor: actorFromCtx(r), Action: "update.check", Result: "error", After: err.Error(), IP: clientIP(r),
		})
		writeError(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}

	suffix := updater.ArtifactSuffix()
	asset, err := updater.FindAsset(rel, suffix)
	if err != nil {
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor: actorFromCtx(r), Action: "update.check", Result: "no_asset", After: suffix, IP: clientIP(r),
		})
		writeJSON(w, http.StatusOK, updateCheckResponse{
			Current:   s.Updater.Version,
			Latest:    rel.TagName,
			HasUpdate: rel.TagName != "" && rel.TagName != s.Updater.Version,
			Notes:     fmt.Sprintf("no asset for %s in release %s", suffix, rel.TagName),
		})
		return
	}

	// Try body first (older layout), then fall back to the SHA256SUMS asset.
	checksum, _ := updater.ExtractSha256(rel.Body, asset.Name)
	if checksum == "" {
		if sums := findSumsAsset(rel); sums != nil {
			if got, err := fetchSumForAsset(ctx, s.Updater.Client, sums, asset.Name); err == nil {
				checksum = got
			}
		}
	}

	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actorFromCtx(r),
		Action: "update.check",
		Target: rel.TagName,
		Result: "ok",
		After:  asset.Name,
		IP:     clientIP(r),
	})
	writeJSON(w, http.StatusOK, updateCheckResponse{
		Current:   s.Updater.Version,
		Latest:    rel.TagName,
		HasUpdate: rel.TagName != "" && rel.TagName != s.Updater.Version,
		Asset:     asset.Name,
		Checksum:  checksum,
		Size:      asset.Size,
		Notes:     rel.Name,
	})
}

type updateApplyResponse struct {
	Version         string `json:"version"`
	Checksum        string `json:"checksum"`
	Applied         bool   `json:"applied"`
	RestartRequired bool   `json:"restart_required"`
}

// handleUpdateApply downloads the latest release asset, verifies its
// sha256, and swaps it over the running binary via updater.Swap
// (synchronous — no progress polling). If systemd restart is configured
// the process may exit before the response reaches the client; the panel
// UI handles this by polling /api/v1/health.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.Updater.Client == nil {
		writeError(w, http.StatusServiceUnavailable, "updater_disabled", "updater is not configured")
		return
	}
	if s.Updater.BinaryPath == "" {
		writeError(w, http.StatusServiceUnavailable, "updater_disabled", "binary_path is not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	rel, err := s.Updater.Client.Latest(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}

	suffix := updater.ArtifactSuffix()
	asset, err := updater.FindAsset(rel, suffix)
	if err != nil {
		writeError(w, http.StatusNotFound, "no_asset", err.Error())
		return
	}

	checksum, _ := updater.ExtractSha256(rel.Body, asset.Name)
	if checksum == "" {
		if sums := findSumsAsset(rel); sums != nil {
			checksum, _ = fetchSumForAsset(ctx, s.Updater.Client, sums, asset.Name)
		}
	}
	if checksum == "" {
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor: actorFromCtx(r), Action: "update.apply", Target: rel.TagName,
			Result: "no_checksum", After: asset.Name, IP: clientIP(r),
		})
		writeError(w, http.StatusFailedDependency, "no_checksum", "release did not publish a sha256 for this asset")
		return
	}

	newPath := s.Updater.BinaryPath + ".new"
	if _, err := s.Updater.Client.Download(ctx, asset, newPath, checksum); err != nil {
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor: actorFromCtx(r), Action: "update.apply", Target: rel.TagName,
			Result: "download_error", After: err.Error(), IP: clientIP(r),
		})
		writeError(w, http.StatusBadGateway, "download_error", err.Error())
		return
	}

	err = updater.Swap(ctx, updater.SwapOptions{
		CurrentPath: s.Updater.BinaryPath,
		NewPath:     newPath,
		Unit:        s.Updater.Unit,
	})
	if err != nil {
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor: actorFromCtx(r), Action: "update.apply.rolled_back", Target: rel.TagName,
			Result: "swap_failed", After: err.Error(), IP: clientIP(r),
		})
		writeError(w, http.StatusInternalServerError, "swap_failed", err.Error())
		return
	}

	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actorFromCtx(r),
		Action: "update.apply",
		Target: rel.TagName,
		Before: s.Updater.Version,
		After:  asset.Name,
		Result: "ok",
		IP:     clientIP(r),
	})
	writeJSON(w, http.StatusOK, updateApplyResponse{
		Version:         rel.TagName,
		Checksum:        checksum,
		Applied:         true,
		RestartRequired: true,
	})
}

func actorFromCtx(r *http.Request) string {
	actor, _ := r.Context().Value(ctxUsername).(string)
	return actor
}

// findSumsAsset locates a SHA256SUMS-style asset in a release. Handles
// case variants and .txt suffixes since different release pipelines pick
// slightly different names.
func findSumsAsset(rel *updater.Release) *updater.Asset {
	for i := range rel.Assets {
		name := strings.ToLower(rel.Assets[i].Name)
		if strings.Contains(name, "sha256sum") || strings.Contains(name, "sha256sums") || strings.HasSuffix(name, ".sha256") {
			return &rel.Assets[i]
		}
	}
	return nil
}

// fetchSumForAsset downloads the SHA256SUMS asset into a temp file and
// parses out the hex digest for the given asset name. Format is the
// canonical `sha256sum` layout: "<hex>  <filename>" per line.
func fetchSumForAsset(ctx context.Context, c *updater.Client, sums *updater.Asset, assetName string) (string, error) {
	tmp, err := os.CreateTemp("", "5gpn-sums-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if _, err := c.Download(ctx, sums, tmpPath, ""); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	base := filepath.Base(assetName)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] == base || fields[len(fields)-1] == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", io.EOF
}
