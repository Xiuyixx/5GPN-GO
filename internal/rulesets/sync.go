// Ruleset syncing: HTTP GET with ETag / If-Modified-Since caching, parse
// the response body via internal/rules.Import to know the rule count,
// and cache the raw content in the DB so downstream Expand() does not
// need to hit the network.

package rulesets

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/netguard"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// SyncOptions tunes one sync round.
type SyncOptions struct {
	Timeout   time.Duration // GET timeout; default 10s
	MaxBytes  int64         // body cap; default 4 MiB
	UserAgent string        // default "5gpn-panel/rulesets"
}

// Syncer walks the rulesets table on a schedule (Run) or on demand
// (SyncOne). Never mutates the ruleset registration; only content /
// rule_count / etag / last_synced_at / last_error.
type Syncer struct {
	Store   *Store
	Logger  *slog.Logger
	Client  *http.Client
	Options SyncOptions
}

// NewSyncer returns a Syncer with sane defaults. Client is inherited
// from options if set; otherwise a default is built with the timeout.
func NewSyncer(store *Store, logger *slog.Logger, opts SyncOptions) *Syncer {
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 4 * 1024 * 1024
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "5gpn-panel/rulesets"
	}
	return &Syncer{
		Store:   store,
		Logger:  logger,
		Client:  netguard.NewHTTPClient(opts.Timeout),
		Options: opts,
	}
}

// SyncOne fetches one named ruleset, respecting the cached ETag /
// Last-Modified. Result is written to the store atomically:
//
//   - 304 Not Modified -> TouchSyncedAt (last_synced_at bumped, content
//     unchanged).
//   - 200 OK           -> UpdateContent (fresh body, fresh cache
//     headers, rule_count recomputed).
//   - Anything else    -> RecordError so the operator can see why the
//     card looks stale.
func (s *Syncer) SyncOne(ctx context.Context, name string) error {
	rs, err := s.Store.Get(ctx, name)
	if err != nil {
		return err
	}
	if _, err := netguard.ValidateHTTPURL(rs.SourceURL); err != nil {
		_ = s.Store.RecordError(ctx, name, err.Error())
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rs.SourceURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.Options.UserAgent)
	if rs.ETag != "" {
		req.Header.Set("If-None-Match", rs.ETag)
	}
	if rs.LastModified != "" {
		req.Header.Set("If-Modified-Since", rs.LastModified)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		_ = s.Store.RecordError(ctx, name, err.Error())
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return s.Store.TouchSyncedAt(ctx, name)
	case resp.StatusCode >= 400:
		msg := fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
		_ = s.Store.RecordError(ctx, name, msg)
		return fmt.Errorf("rulesets: %s", msg)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, s.Options.MaxBytes+1))
	if err != nil {
		_ = s.Store.RecordError(ctx, name, err.Error())
		return err
	}
	if int64(len(body)) > s.Options.MaxBytes {
		msg := fmt.Sprintf("ruleset exceeds %d bytes", s.Options.MaxBytes)
		_ = s.Store.RecordError(ctx, name, msg)
		return fmt.Errorf("rulesets: %s", msg)
	}

	// Count rules by running through the same parser core.Assemble will
	// use so the number on the card matches what actually lands in the
	// snapshot.
	converted, _ := rules.Import(string(body), rules.ImportLegacyOptions{})
	ruleCount := len(converted)

	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	lastModified := strings.TrimSpace(resp.Header.Get("Last-Modified"))
	return s.Store.UpdateContent(ctx, name, body, ruleCount, etag, lastModified)
}

// Run loops over every enabled ruleset every `interval` and calls
// SyncOne. Blocks until ctx is done. The first tick fires after
// `interval` so daemon boot is not delayed by a batch of ACME-like HTTP
// round-trips.
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			list, err := s.Store.List(ctx)
			if err != nil {
				if s.Logger != nil {
					s.Logger.Warn("rulesets.Run: list failed", "err", err)
				}
				continue
			}
			for _, rs := range list {
				if !rs.Enabled {
					continue
				}
				if err := s.SyncOne(ctx, rs.Name); err != nil && s.Logger != nil {
					s.Logger.Warn("rulesets.Run: sync failed", "name", rs.Name, "err", err)
				}
			}
		}
	}
}
