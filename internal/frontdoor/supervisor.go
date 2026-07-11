package frontdoor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// MaxRestartsPerWindow / RestartWindow bound the supervisor's
// crash-loop protection: at most 5 restarts inside any trailing 60s
// window before it gives up and degrades to SERVFAIL (plan §4 Phase 2
// Round 3 rewrite).
const (
	MaxRestartsPerWindow = 5
	RestartWindow        = 60 * time.Second
)

// restartBackoff is the delay before restart attempts 1..5
// respectively (plan: "250ms, 500ms, 1s, 2s, 4s"). The Nth crash waits
// restartBackoff[min(N-1, len-1)] before retrying.
var restartBackoff = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
}

// ErrSupervisorGaveUp is returned by Run once the restart budget is
// exhausted. By the time Run returns it, onGiveUp has already been
// invoked exactly once.
var ErrSupervisorGaveUp = errors.New("frontdoor: supervisor exhausted restart budget")

// Supervisor watches a single long-running task — for Frontdoor,
// "bind and serve every :53 listener" — and restarts it in place on
// crash, bounded by MaxRestartsPerWindow within RestartWindow. It has
// no knowledge of what the task actually does; Frontdoor is the only
// caller in this phase.
type Supervisor struct {
	logger *slog.Logger

	mu       sync.Mutex
	restarts []time.Time // sliding window of restart timestamps
	givenUp  bool
}

// NewSupervisor returns a Supervisor logging through logger (or
// slog.Default() if nil).
func NewSupervisor(logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{logger: logger}
}

// Run invokes task once, then again on every crash (task returning a
// non-nil error, or panicking — both count as a crash and trigger a
// restart) until one of:
//   - ctx is cancelled: a clean stop, Run returns ctx.Err().
//   - task returns nil while ctx is still live: the task finished on
//     its own, Run returns nil.
//   - the restart budget inside RestartWindow is exhausted: onGiveUp is
//     invoked exactly once and Run returns ErrSupervisorGaveUp.
func (sv *Supervisor) Run(ctx context.Context, task func(ctx context.Context) error, onGiveUp func()) error {
	attempt := 0
	for {
		err := sv.runOnce(ctx, task)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}

		sv.logger.Warn("frontdoor: supervised task crashed", "err", err, "attempt", attempt+1)

		if !sv.recordRestart() {
			sv.mu.Lock()
			already := sv.givenUp
			sv.givenUp = true
			sv.mu.Unlock()
			if !already {
				sv.logger.Error("frontdoor: supervisor exhausted restart budget — degrading to SERVFAIL",
					"event", "5gpn.frontdoor.supervisor.giveup",
					"max_restarts", MaxRestartsPerWindow,
					"window", RestartWindow.String(),
				)
				if onGiveUp != nil {
					onGiveUp()
				}
			}
			return ErrSupervisorGaveUp
		}

		backoff := restartBackoff[min(attempt, len(restartBackoff)-1)]
		sv.logger.Info("frontdoor: restarting supervised task",
			"event", "5gpn.frontdoor.supervisor.restart",
			"attempt", attempt+1,
			"backoff", backoff.String(),
		)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		attempt++
	}
}

// runOnce executes task, converting any panic into an error so one bad
// crash can't unwind past the supervisor and take the caller's
// goroutine down with it.
func (sv *Supervisor) runOnce(ctx context.Context, task func(ctx context.Context) error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("frontdoor: supervised task panic: %v", rec)
		}
	}()
	return task(ctx)
}

// recordRestart appends now to the sliding window, evicts entries
// older than RestartWindow, and reports whether this restart is still
// within budget (true) or pushes the count over MaxRestartsPerWindow
// (false).
func (sv *Supervisor) recordRestart() bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-RestartWindow)
	kept := sv.restarts[:0]
	for _, t := range sv.restarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	sv.restarts = kept
	return len(sv.restarts) <= MaxRestartsPerWindow
}

// GivenUp reports whether the supervisor has ever exhausted its
// restart budget — a one-shot terminal state for a Supervisor
// instance's lifetime. Exposed for tests.
func (sv *Supervisor) GivenUp() bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.givenUp
}
