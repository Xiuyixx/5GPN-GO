package api

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/Xiuyixx/5GPN-Go/internal/core"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

const (
	// applyRingCap is the count-cap on the number of tracked apply entries.
	// Enforced on write: the oldest entry is evicted once the ring exceeds
	// this size, regardless of its age (AC-O6).
	applyRingCap = 128
	// applyRingTTL is the minimum retention floor for an entry: it is kept
	// readable for at least this long even if the ring is nowhere near the
	// count-cap. Enforced on read — Get/List simply stop returning an
	// entry once it ages past this, they don't proactively delete it (the
	// count-cap already bounds total ring size).
	applyRingTTL = 24 * time.Hour
	// applySyncWindow is how long handleApply / handleRollbackSnapshot
	// block waiting for a resolver.BuildTable + Store.Publish to finish
	// before degrading to 202 Accepted + poll (AC-O1).
	applySyncWindow = 100 * time.Millisecond
)

// buildTableFn indirects resolver.BuildTable through a package var so
// tests can inject an artificial delay and exercise the async (202) path
// deterministically, without needing a real 100k-rule fixture to blow the
// 100ms sync window on every machine.
var buildTableFn = resolver.BuildTable

// applyEntry is one row of apply-lifecycle bookkeeping tracked by
// applyStore. Status transitions pending -> succeeded|failed exactly once.
// Kind distinguishes a forward rules-apply from a snapshot rollback so the
// /api/v1/applies feed can label both without callers having to guess from
// context.
type applyEntry struct {
	ID         string    `json:"id"`
	Hash       string    `json:"hash"`
	Status     string    `json:"status"` // pending | succeeded | failed
	Kind       string    `json:"kind"`   // apply | rollback | import | exit_switch
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`
	RuleCount  int       `json:"rule_count"`
	SnapshotID int64     `json:"snapshot_id,omitempty"`
}

// applyStore is an in-memory ring buffer of the most recent apply-lifecycle
// entries, keyed by apply ID for O(1) polling lookups (GET
// /api/v1/applies/{id}) plus insertion-ordered listing (GET
// /api/v1/applies). It also owns the singleflight group that collapses
// concurrent same-hash builds and the mutex that serializes
// resolver.Store.Publish calls across DIFFERENT hashes so two racing
// builds can never commit out of generation order (plan §4 Phase 5,
// Critic R3 "concurrent apply + rollback race").
type applyStore struct {
	mu    sync.Mutex
	order []string // entry IDs, oldest first; bounded to applyRingCap
	byID  map[string]*applyEntry

	now func() time.Time // overridable in tests for TTL assertions

	sf        singleflight.Group
	publishMu sync.Mutex
}

func newApplyStore() *applyStore {
	return &applyStore{
		byID: make(map[string]*applyEntry),
		now:  time.Now,
	}
}

// create allocates a new pending entry and inserts it into the ring,
// evicting the oldest entry once the applyRingCap count-cap is exceeded.
func (as *applyStore) create(hash, kind string, ruleCount int, snapshotID ...int64) *applyEntry {
	var snapID int64
	if len(snapshotID) > 0 {
		snapID = snapshotID[0]
	}
	e := &applyEntry{
		ID:         uuid.NewString(),
		Hash:       hash,
		Status:     "pending",
		Kind:       kind,
		StartedAt:  as.now(),
		RuleCount:  ruleCount,
		SnapshotID: snapID,
	}
	as.mu.Lock()
	as.byID[e.ID] = e
	as.order = append(as.order, e.ID)
	for len(as.order) > applyRingCap {
		oldest := as.order[0]
		as.order = as.order[1:]
		delete(as.byID, oldest)
	}
	as.mu.Unlock()
	return e
}

func (s *Server) trackRuleApply(set *rules.RuleSet, kind string, result core.ApplyResult) *applyEntry {
	return s.trackApplyResult(rules.HashRules(set), kind, len(set.Rules), result)
}

func (s *Server) trackApplyResult(hash, kind string, ruleCount int, result core.ApplyResult) *applyEntry {
	entry := s.applyStore.create(hash, kind, ruleCount, result.SnapshotID)
	if result.Health == "observing" {
		return entry
	}
	if result.RolledBack {
		s.applyStore.finish(entry.ID, "failed", result.Reason)
	} else {
		s.applyStore.finish(entry.ID, "succeeded", "")
	}
	entry, _ = s.applyStore.Get(entry.ID)
	return entry
}

func (s *Server) refreshApplyEntry(entry *applyEntry) *applyEntry {
	if entry == nil || entry.Status != "pending" || entry.SnapshotID == 0 {
		return entry
	}
	if s.Applier != nil {
		if terminal, ok := s.Applier.TerminalStatus(entry.SnapshotID); ok {
			if terminal.State == "confirmed" {
				s.applyStore.finish(entry.ID, "succeeded", "")
			} else {
				s.applyStore.finish(entry.ID, "failed", terminal.Reason)
			}
			refreshed, _ := s.applyStore.Get(entry.ID)
			return refreshed
		}
	}
	status, err := db.ApplyStatusBySnapshot(s.DB, entry.SnapshotID)
	if err != nil || status == nil || status.State == "submitted" {
		return entry
	}
	if status.State == "confirmed" {
		s.applyStore.finish(entry.ID, "succeeded", "")
	} else {
		s.applyStore.finish(entry.ID, "failed", status.Reason)
	}
	refreshed, ok := s.applyStore.Get(entry.ID)
	if !ok {
		return entry
	}
	return refreshed
}

// finish marks entry id terminal (succeeded|failed) with an optional error
// message. No-op if the id has since been evicted by the count-cap.
func (as *applyStore) finish(id, status, errMsg string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	e, ok := as.byID[id]
	if !ok {
		return
	}
	if e.Status != "pending" {
		return
	}
	e.Status = status
	e.Error = errMsg
	e.FinishedAt = as.now()
}

// Get returns a copy of the entry for id, or (nil, false) if the id was
// never tracked, has been evicted by the count-cap, or has aged past the
// 24h TTL floor (AC-O6). TTL is enforced lazily on read; the entry is not
// physically removed (the count-cap already bounds ring size).
func (as *applyStore) Get(id string) (*applyEntry, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	e, ok := as.byID[id]
	if !ok {
		return nil, false
	}
	if as.now().Sub(e.StartedAt) > applyRingTTL {
		return nil, false
	}
	cp := *e
	return &cp, true
}

// List returns copies of all live (non-TTL-expired) entries sorted by
// StartedAt descending.
func (as *applyStore) List() []applyEntry {
	as.mu.Lock()
	defer as.mu.Unlock()
	cutoff := as.now().Add(-applyRingTTL)
	out := make([]applyEntry, 0, len(as.order))
	for _, id := range as.order {
		e, ok := as.byID[id]
		if !ok || e.StartedAt.Before(cutoff) {
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// rebuildAndPublish computes the effective RuleTable for rs and publishes
// it into s.Resolver — a no-op when s.Resolver is nil (a test-mode Server
// built without the DNS plane wired in). The operation is recorded as a
// new applyStore entry of the given kind ("apply" or "rollback") and that
// entry is returned either once the build+publish has completed, or after
// applySyncWindow has elapsed with the build still running in the
// background — callers distinguish the two cases via entry.Status
// ("succeeded"/"failed" vs "pending").
//
// Concurrent calls sharing the same rule-set hash collapse into a single
// resolver.BuildTable invocation via singleflight (same-hash dedup).
// Calls to resolver.Store.Publish are additionally serialized across
// DIFFERENT hashes by applyStore.publishMu so two racing builds can never
// interleave commits — the last Publish() to run wins, but the commit
// order is deterministic rather than racing on the atomic pointer swap
// (Critic R3 refinement).
func (s *Server) rebuildAndPublish(ctx context.Context, rs []rules.Rule, kind string) *applyEntry {
	set := &rules.RuleSet{Rules: rs}
	hash := rules.HashRules(set)
	entry := s.applyStore.create(hash, kind, len(rs))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err, _ := s.applyStore.sf.Do(hash, func() (any, error) {
			tbl, buildErr := buildTableFn(rs)
			if buildErr != nil {
				return nil, buildErr
			}
			if s.Resolver != nil {
				s.applyStore.publishMu.Lock()
				s.Resolver.Publish(tbl)
				s.applyStore.publishMu.Unlock()
			}
			return tbl, nil
		})
		if err != nil {
			s.applyStore.finish(entry.ID, "failed", err.Error())
		} else {
			s.applyStore.finish(entry.ID, "succeeded", "")
		}
	}()

	select {
	case <-done:
	case <-time.After(applySyncWindow):
	}

	if got, ok := s.applyStore.Get(entry.ID); ok {
		return got
	}
	return entry
}

// handleApplyGet is GET /api/v1/applies/{id} — the poll endpoint a caller
// uses after receiving a 202 Accepted from /api/v1/rules/apply or a
// snapshot rollback. Returns 404 apply_expired once the ring has evicted
// (or TTL-expired) the id; callers fall back to /api/v1/apply/status or
// /api/v1/snapshots for the latest terminal state (AC-O6).
func (s *Server) handleApplyGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entry, ok := s.applyStore.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "apply_expired",
			"apply id no longer tracked; check /api/v1/apply/status or /api/v1/snapshots for latest")
		return
	}
	writeJSON(w, http.StatusOK, s.refreshApplyEntry(entry))
}

// handleAppliesList is GET /api/v1/applies — the escape hatch for a
// polling client whose cached apply_id has been evicted; returns the last
// applyRingCap live entries ordered by started_at DESC.
func (s *Server) handleAppliesList(w http.ResponseWriter, r *http.Request) {
	entries := s.applyStore.List()
	for i := range entries {
		entries[i] = *s.refreshApplyEntry(&entries[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
