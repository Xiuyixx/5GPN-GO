// Package tgbot's Manager handles the "hot reload" case: the operator
// changes the token or admin ids in the panel and expects the bot to
// come online without restarting the daemon. cmd/5gpn wires one Manager
// at boot; the settings handler calls Update() to replace or stop it.
package tgbot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ManagerConfig captures the constant pieces of a bot lifecycle. The
// Handlers plumbing does not change across reloads, but token and admin
// ids do — those come from Update.
type ManagerConfig struct {
	Handlers          Handlers
	Logger            *slog.Logger
	ValidationTimeout time.Duration // default DefaultValidationTimeout
}

// Manager owns at most one running Bot. Start/Update replace the
// running instance atomically; Stop tears it down. Serialization on mu
// keeps the observable state — token/admin_ids/enabled — coherent under
// concurrent Update calls.
type Manager struct {
	cfg ManagerConfig

	updateMu     sync.Mutex
	mu           sync.Mutex
	rootCtx      context.Context // daemon lifetime ctx captured on first Start
	token        string
	adminChatIDs []int64
	cancel       context.CancelFunc
	running      bool
	generation   uint64 // bumped on every activation; cleanup uses it to detect "still me?"
}

// newBotFn is the seam for tests to inject a fake Bot without hitting
// Telegram's servers. Production keeps the real context-aware constructor.
var newBotFn = func(ctx context.Context, cfg Config) (runnable, error) {
	return NewWithContext(ctx, cfg)
}

// runnable is the minimum surface Manager needs from a live Bot. Kept
// unexported: the Bot type from bot.go satisfies it implicitly.
type runnable interface {
	Serve(ctx context.Context) error
}

// NewManager builds a Manager. Use Start on daemon boot to bring the bot
// up from config; use Update from the settings handler.
func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{cfg: cfg}
}

// Start brings the bot online in a background goroutine. Idempotent — a
// second Start (or an Update) will Stop the prior instance first.
// Returns ErrBotDisabled when token is empty (so callers can skip
// cleanly on a fresh install with no bot configured yet). Returns
// whatever error tgbot.New reports otherwise.
//
// The ctx passed here is captured as the Manager's *root* lifetime ctx:
// all bots spawned by later Update() calls derive from it. That way a
// panel-driven restart (whose caller ctx is a short-lived HTTP request
// ctx) does not kill the new bot the moment the HTTP response is written.
func (m *Manager) Start(ctx context.Context, token string, adminChatIDs []int64) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	// Always remember the daemon ctx, even on the empty-token path, so a
	// later panel-driven Update can spawn a bot rooted in the correct
	// lifetime.
	if m.rootCtx == nil {
		m.rootCtx = ctx
	}
	m.mu.Unlock()
	if token == "" {
		return ErrBotDisabled
	}
	bot, err := m.buildCandidate(ctx, token, adminChatIDs)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.stopLocked()
	m.activateLocked(bot, token, adminChatIDs)
	m.mu.Unlock()
	return nil
}

// Update replaces token / admin ids in place. The candidate is fully built
// and validated before the running bot is stopped. When token is empty this
// is equivalent to Stop.
//
// ctx governs validation only; the activated bot still derives its lifetime
// from the daemon root context captured by Start.
func (m *Manager) Update(ctx context.Context, token string, adminChatIDs []int64) error {
	return m.UpdateWithCommit(ctx, token, adminChatIDs, nil)
}

// UpdateWithCommit validates a replacement, runs commit while the old bot is
// still live, and swaps runtime state only after commit succeeds. Settings
// handlers use this to keep SQLite and the running bot in one failure domain:
// a database error leaves the previous bot untouched.
func (m *Manager) UpdateWithCommit(ctx context.Context, token string, adminChatIDs []int64, commit func() error) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var candidate runnable
	var err error
	if token != "" {
		candidate, err = m.buildCandidate(ctx, token, adminChatIDs)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	if candidate == nil {
		m.token = ""
		m.adminChatIDs = nil
		return nil
	}
	m.activateLocked(candidate, token, adminChatIDs)
	return nil
}

// Stop tears down the bot; no-op if not running. Safe to call multiple
// times.
func (m *Manager) Stop() {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// Status returns a snapshot of the current bot lifecycle. Safe for
// concurrent use.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{
		Enabled:     m.running,
		AdminCount:  len(m.adminChatIDs),
		TokenSet:    m.token != "",
		TokenMasked: maskToken(m.token),
	}
}

// Status is a serializable snapshot used by the panel.
type Status struct {
	Enabled     bool
	AdminCount  int
	TokenSet    bool
	TokenMasked string
}

func (m *Manager) buildCandidate(ctx context.Context, token string, adminChatIDs []int64) (runnable, error) {
	timeout := m.cfg.ValidationTimeout
	if timeout <= 0 {
		timeout = DefaultValidationTimeout
	}
	validationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return newBotFn(validationCtx, Config{
		Token:        token,
		AdminChatIDs: adminChatIDs,
		Handlers:     m.cfg.Handlers,
		Logger:       m.cfg.Logger,
	})
}

// activateLocked assumes m.mu is held and candidate already passed validation.
func (m *Manager) activateLocked(bot runnable, token string, adminChatIDs []int64) {
	parent := m.rootCtx
	if parent == nil {
		// Defensive: Start should have set this. Falling back keeps a
		// caller that only ever uses Update() from crashing, at the cost
		// of losing daemon-shutdown propagation into this bot.
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.token = token
	m.adminChatIDs = append([]int64(nil), adminChatIDs...)
	m.cancel = cancel
	m.running = true
	m.generation++
	myGen := m.generation

	go func() {
		if err := bot.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			if m.cfg.Logger != nil {
				m.cfg.Logger.Warn("tgbot.Serve exited", "err", err)
			}
		}
		// If this goroutine's bot is still the current bot (no newer
		// startLocked has run since), reflect the Serve exit in Status so
		// the panel does not lie. A newer generation means Update already
		// swapped state and this cleanup must not stomp on it.
		m.mu.Lock()
		if m.generation == myGen {
			m.running = false
			m.cancel = nil
		}
		m.mu.Unlock()
	}()
}

// stopLocked assumes m.mu is held.
func (m *Manager) stopLocked() {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.running = false
}

// MaskToken keeps enough of the token to make it recognizable in the
// panel without leaking it. Telegram bot tokens look like
// "123456789:AAF-...-xyz". We show "1234…xyz". Exported so the
// wizard/settings handler can echo a masked form without duplicating
// the rule.
func MaskToken(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "…" + t[len(t)-3:]
}

// maskToken is the internal alias kept for backwards compatibility with
// existing callers inside the package.
func maskToken(t string) string { return MaskToken(t) }
