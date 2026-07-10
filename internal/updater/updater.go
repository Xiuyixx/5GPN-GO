// Package updater checks GitHub Releases for a newer 5gpn binary,
// downloads it with sha256 verification, and swaps it in blue-green
// (write ".new", os.Rename over the running binary, systemctl restart).
//
// Runtime safety: Linux allows deleting an open executable; the running
// daemon keeps the old file mapped until systemd restarts it. If the
// new binary fails its 3-second health check the previous file is
// restored from the ".prev" backup.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Release captures the subset of the GitHub Releases API we consume.
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	Body        string  `json:"body"`
	Assets      []Asset `json:"assets"`
	PublishedAt string  `json:"published_at"`
}

// Asset is one binary attached to a release.
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"browser_download_url"`
}

// Config knobs.
type Config struct {
	Owner      string        // e.g. "Xiuyixx"
	Repo       string        // e.g. "5GPN-GO"
	HTTPClient *http.Client  // defaults to http.DefaultClient
	Timeout    time.Duration // GitHub API + download timeout, default 60s
}

// Client is the HTTP + FS shim.
type Client struct {
	cfg Config
	hc  *http.Client
}

// New builds a Client.
func New(cfg Config) *Client {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Client{cfg: cfg, hc: cfg.HTTPClient}
}

// Latest returns the most recent non-draft release.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", c.cfg.Owner, c.cfg.Repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("updater: latest release: %s: %s", resp.Status, string(body))
	}
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// FindAsset picks the asset matching a suffix (e.g. `linux-amd64`).
// Panics never; returns nil + error if no match.
func FindAsset(r *Release, needle string) (*Asset, error) {
	needle = strings.ToLower(needle)
	for i := range r.Assets {
		if strings.Contains(strings.ToLower(r.Assets[i].Name), needle) {
			return &r.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("updater: no asset matching %q in release %s", needle, r.TagName)
}

// ExtractSha256 finds a hex sha256 for the asset in the release body.
// Matches lines like `assetname  <sha256>`.
func ExtractSha256(body, assetName string) (string, error) {
	re := regexp.MustCompile(`([a-fA-F0-9]{64})`)
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, assetName) {
			continue
		}
		if m := re.FindString(line); m != "" {
			return strings.ToLower(m), nil
		}
	}
	if m := re.FindString(body); m != "" {
		return strings.ToLower(m), nil
	}
	return "", fmt.Errorf("updater: no sha256 for %q in release body", assetName)
}

// Download saves asset to dest, verifies sha256 (empty wantHex skips
// verification for tests), and returns the number of bytes written.
func (c *Client) Download(ctx context.Context, asset *Asset, dest, wantHex string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", asset.DownloadURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("updater: download %s: %s", asset.Name, resp.Status)
	}
	tmp := dest + ".dl"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if wantHex != "" && got != strings.ToLower(wantHex) {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("updater: sha256 mismatch: got %s want %s", got, wantHex)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return 0, err
	}
	return n, nil
}

// Swap performs a blue-green replacement: back up current, install new,
// restart systemd unit, health-check, roll back on failure.
type SwapOptions struct {
	CurrentPath   string        // path to the running binary
	NewPath       string        // path to the freshly downloaded binary
	Unit          string        // systemd unit to restart (empty skips)
	HealthCheck   func(context.Context) error
	HealthDelay   time.Duration // default 3s
	HealthTimeout time.Duration // default 5s
}

// Swap replaces CurrentPath with NewPath, keeping a .prev backup.
func Swap(ctx context.Context, opts SwapOptions) error {
	if opts.CurrentPath == "" || opts.NewPath == "" {
		return fmt.Errorf("updater: paths required")
	}
	if opts.HealthDelay == 0 {
		opts.HealthDelay = 3 * time.Second
	}
	if opts.HealthTimeout == 0 {
		opts.HealthTimeout = 5 * time.Second
	}

	backup := opts.CurrentPath + ".prev"
	if _, err := os.Stat(opts.CurrentPath); err == nil {
		if err := copyFile(opts.CurrentPath, backup); err != nil {
			return fmt.Errorf("updater: backup: %w", err)
		}
	}
	if err := os.Rename(opts.NewPath, opts.CurrentPath); err != nil {
		return fmt.Errorf("updater: rename: %w", err)
	}
	if opts.Unit != "" && runtime.GOOS == "linux" {
		if err := exec.CommandContext(ctx, "systemctl", "restart", opts.Unit).Run(); err != nil {
			_ = restore(backup, opts.CurrentPath)
			return fmt.Errorf("updater: systemctl restart: %w", err)
		}
	}
	if opts.HealthCheck != nil {
		time.Sleep(opts.HealthDelay)
		probeCtx, cancel := context.WithTimeout(ctx, opts.HealthTimeout)
		defer cancel()
		if err := opts.HealthCheck(probeCtx); err != nil {
			_ = restore(backup, opts.CurrentPath)
			if opts.Unit != "" && runtime.GOOS == "linux" {
				_ = exec.CommandContext(ctx, "systemctl", "restart", opts.Unit).Run()
			}
			return fmt.Errorf("updater: health check failed: %w", err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func restore(backup, current string) error {
	if _, err := os.Stat(backup); err != nil {
		return err
	}
	return os.Rename(backup, current)
}

// ArtifactSuffix builds the conventional linux-amd64 / linux-arm64 tag.
func ArtifactSuffix() string {
	return filepath.Base(runtime.GOOS + "-" + runtime.GOARCH)
}
