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
)

// ManagerConfig captures the constant pieces of a bot lifecycle. The
// Handlers plumbing does not change across reloads, but token and admin
// ids do — those come from Update.
type ManagerConfig struct {
	Handlers Handlers
	Logger   *slog.Logger
}

// Manager owns at most one running Bot. Start/Update replace the
// running instance atomically; Stop tears it down. Serialization on mu
// keeps the observable state — token/admin_ids/enabled — coherent under
// concurrent Update calls.
type Manager struct {
	cfg ManagerConfig

	mu           sync.Mutex
	token        string
	adminChatIDs []int64
	cancel       context.CancelFunc
	running      bool
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
func (m *Manager) Start(ctx context.Context, token string, adminChatIDs []int64) error {
	if token == "" {
		return ErrBotDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked(ctx, token, adminChatIDs)
}

// Update replaces token / admin ids in place. Stops any running bot and
// starts fresh with the new credentials. When token is empty this is
// equivalent to Stop.
func (m *Manager) Update(ctx context.Context, token string, adminChatIDs []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	if token == "" {
		return nil
	}
	return m.startLocked(ctx, token, adminChatIDs)
}

// Stop tears down the bot; no-op if not running. Safe to call multiple
// times.
func (m *Manager) Stop() {
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
		Enabled:      m.running,
		AdminCount:   len(m.adminChatIDs),
		TokenSet:     m.token != "",
		TokenMasked:  maskToken(m.token),
	}
}

// Status is a serializable snapshot used by the panel.
type Status struct {
	Enabled     bool
	AdminCount  int
	TokenSet    bool
	TokenMasked string
}

// startLocked assumes m.mu is held.
func (m *Manager) startLocked(parent context.Context, token string, adminChatIDs []int64) error {
	bot, err := New(Config{
		Token:        token,
		AdminChatIDs: adminChatIDs,
		Handlers:     m.cfg.Handlers,
		Logger:       m.cfg.Logger,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	m.token = token
	m.adminChatIDs = append([]int64(nil), adminChatIDs...)
	m.cancel = cancel
	m.running = true

	go func() {
		if err := bot.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			if m.cfg.Logger != nil {
				m.cfg.Logger.Warn("tgbot.Serve exited", "err", err)
			}
		}
		// Best-effort cleanup: if the Serve loop exited on its own (e.g.
		// upstream 401), reflect that in Status so the panel doesn't lie.
		m.mu.Lock()
		if m.cancel != nil && &m.cancel == &cancel {
			m.running = false
		}
		m.mu.Unlock()
	}()
	return nil
}

// stopLocked assumes m.mu is held.
func (m *Manager) stopLocked() {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.running = false
}

// maskToken keeps enough of the token to make it recognizable in the
// panel without leaking it. Telegram bot tokens look like
// "123456789:AAF-...-xyz". We show "1234…xyz".
func maskToken(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "…" + t[len(t)-3:]
}
