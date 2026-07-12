// Package tgbot runs the Telegram bot half of the panel. It shares
// business logic with the Web API through the Handlers interface — the
// daemon injects an implementation backed by the same database + orchestrator
// the panel uses. That way `/exits`, `/rules`, `/status` etc. always return
// the same view the panel does, and the two entrypoints never drift.
package tgbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// DefaultValidationTimeout bounds the synchronous getMe request used to
// validate bot credentials before a Manager replaces the running instance.
const DefaultValidationTimeout = 10 * time.Second

// telegramAPIEndpoint is a test seam for the credential-validation request.
var telegramAPIEndpoint = tgbotapi.APIEndpoint

type validationHTTPClient struct {
	ctx   context.Context
	inner *http.Client
}

func (c validationHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.inner.Do(req.Clone(c.ctx))
}

// Config controls the bot runtime. Token being empty disables the bot.
type Config struct {
	Token          string
	AdminChatIDs   []int64
	Handlers       Handlers // required
	Logger         *slog.Logger
	PollTimeoutSec int // 0 -> 30
}

// Bot runs the Telegram update loop.
type Bot struct {
	cfg       Config
	api       *tgbotapi.BotAPI
	handlers  *dispatcher
	log       *slog.Logger
	whitelist map[int64]bool
	mu        sync.RWMutex
}

// New builds a Bot with a bounded credential-validation request.
func New(cfg Config) (*Bot, error) {
	return NewWithContext(context.Background(), cfg)
}

// NewWithContext builds a Bot and validates its token through Telegram's
// getMe endpoint. The request honors ctx and is independently capped by
// DefaultValidationTimeout. The short-lived validation context is not reused
// by Serve's long-poll client.
func NewWithContext(ctx context.Context, cfg Config) (*Bot, error) {
	if cfg.Token == "" {
		return nil, ErrBotDisabled
	}
	if cfg.Handlers == nil {
		return nil, errors.New("tgbot: Handlers required")
	}
	if len(cfg.AdminChatIDs) == 0 {
		// Enforce plan security: refuse to start the bot without a whitelist.
		return nil, errors.New("tgbot: refusing to start without admin_chat_ids whitelist")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.PollTimeoutSec <= 0 {
		cfg.PollTimeoutSec = 30
	}
	if ctx == nil {
		ctx = context.Background()
	}
	validationCtx, cancel := context.WithTimeout(ctx, DefaultValidationTimeout)
	defer cancel()
	api, err := tgbotapi.NewBotAPIWithClient(cfg.Token, telegramAPIEndpoint, validationHTTPClient{
		ctx:   validationCtx,
		inner: &http.Client{Timeout: DefaultValidationTimeout},
	})
	if err != nil {
		return nil, fmt.Errorf("tgbot: init api: %w", err)
	}
	// The request context belongs only to getMe. Runtime long polling is
	// cancelled by Bot.Serve's daemon-lifetime context instead.
	api.Client = &http.Client{}
	b := &Bot{
		cfg:       cfg,
		api:       api,
		log:       cfg.Logger,
		whitelist: map[int64]bool{},
	}
	for _, id := range cfg.AdminChatIDs {
		b.whitelist[id] = true
	}
	b.handlers = newDispatcher(cfg.Handlers)
	return b, nil
}

// ErrBotDisabled is returned by New when no token is configured.
var ErrBotDisabled = errors.New("tgbot: disabled (empty token)")

// Serve runs the long-poll loop until ctx is cancelled.
func (b *Bot) Serve(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = b.cfg.PollTimeoutSec
	updates := b.api.GetUpdatesChan(u)

	b.log.Info("tgbot listening", "username", b.api.Self.UserName, "admins", len(b.whitelist))

	go func() {
		<-ctx.Done()
		b.api.StopReceivingUpdates()
	}()

	for update := range updates {
		if update.Message == nil {
			if update.CallbackQuery != nil {
				b.handleCallback(ctx, update.CallbackQuery)
			}
			continue
		}
		msg := update.Message
		if !b.allowed(msg.Chat.ID) && !isPublicCommand(msg.Text) {
			b.log.Warn("tgbot: unauthorized chat", "chat_id", msg.Chat.ID, "from", msg.From.UserName)
			// Silent per plan: log audit but do not respond.
			b.cfg.Handlers.RecordUnauthorized(msg.Chat.ID, msg.From.UserName)
			continue
		}
		b.handleMessage(ctx, msg)
	}
	return nil
}

func isPublicCommand(text string) bool {
	cmd, _ := splitCommand(strings.TrimSpace(text))
	return cmd == "/id"
}

func (b *Bot) allowed(chatID int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.whitelist[chatID]
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	// Take the first space-separated token as the command.
	cmd, args := splitCommand(text)
	reply, kb, err := b.handlers.Dispatch(ctx, cmd, args, msg)
	if err != nil {
		b.log.Warn("tgbot: dispatch error", "cmd", cmd, "err", err)
		reply = fmt.Sprintf("error: %v", err)
	}
	if reply == "" {
		return
	}
	out := tgbotapi.NewMessage(msg.Chat.ID, reply)
	out.ParseMode = tgbotapi.ModeMarkdown
	if kb != nil {
		out.ReplyMarkup = kb
	}
	if _, err := b.api.Send(out); err != nil {
		b.log.Warn("tgbot: send", "err", err)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	if !b.allowed(cb.Message.Chat.ID) {
		return
	}
	// Acknowledge immediately so the client stops the spinner.
	if _, err := b.api.Request(tgbotapi.NewCallback(cb.ID, "")); err != nil {
		b.log.Warn("tgbot: cb ack", "err", err)
	}
	// Route callback data as if it were a slash command.
	cmd, args := splitCommand("/" + strings.TrimPrefix(cb.Data, "/"))
	reply, kb, err := b.handlers.Dispatch(ctx, cmd, args, cb.Message)
	if err != nil {
		reply = fmt.Sprintf("error: %v", err)
	}
	if reply == "" {
		return
	}
	edit := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, reply)
	edit.ParseMode = tgbotapi.ModeMarkdown
	if kb != nil {
		edit.ReplyMarkup = kb
	}
	if _, err := b.api.Send(edit); err != nil {
		b.log.Warn("tgbot: edit", "err", err)
	}
}

func splitCommand(text string) (string, []string) {
	// Strip @botusername suffix Telegram appends in group chats.
	if idx := strings.Index(text, "@"); idx > 0 && !strings.ContainsRune(text[:idx], ' ') {
		endName := strings.Index(text[idx:], " ")
		if endName < 0 {
			text = text[:idx]
		} else {
			text = text[:idx] + text[idx+endName:]
		}
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", nil
	}
	return strings.ToLower(fields[0]), fields[1:]
}

// StartupTimeout is a small helper used by daemon glue to bound the
// initial connect (Telegram getMe) so a broken token fails fast rather
// than blocking the bot goroutine indefinitely.
const StartupTimeout = 10 * time.Second
