package tgbot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Handlers is the seam between the bot and the daemon. It intentionally
// mirrors the Web API surface so the two entrypoints share the same
// business logic. All returns are pre-formatted Markdown strings ready
// to send.
type Handlers interface {
	Start(ctx context.Context, chatID int64) (text string, err error)
	Menu(ctx context.Context, chatID int64) (text string, keyboard *tgbotapi.InlineKeyboardMarkup, err error)
	Status(ctx context.Context) (string, error)
	ListExits(ctx context.Context) (string, error)
	ListRules(ctx context.Context) (string, error)
	SwitchExit(ctx context.Context, id string) (string, error)
	AddExit(ctx context.Context, id, uri string) (string, error)
	DeleteExit(ctx context.Context, id string) (string, error)
	Uptime(ctx context.Context) (string, error)
	LoadAvg(ctx context.Context) (string, error)
	MemInfo(ctx context.Context) (string, error)
	CPUStat(ctx context.Context) (string, error)
	TCPStat(ctx context.Context) (string, error)
	NFConntrack(ctx context.Context) (string, error)
	Route(ctx context.Context) (string, error)
	ExitIP(ctx context.Context) (string, error)
	WireGuard(ctx context.Context) (string, error)
	Dev(ctx context.Context) (string, error)
	WWW(ctx context.Context) (string, error)
	JSON(ctx context.Context) (string, error)
	Cancel(ctx context.Context, chatID int64) (string, error)

	// AsID returns the numeric chat id for the /id command (echoing what the
	// bot's peer thinks their chat id is).
	AsID(chatID int64, from string) (string, error)

	// RecordUnauthorized is called when a whitelisted-chat check fails so
	// audit logging can happen server-side.
	RecordUnauthorized(chatID int64, from string)
}

// dispatcher fans /command tokens out to Handlers.
type dispatcher struct {
	h Handlers
}

func newDispatcher(h Handlers) *dispatcher {
	return &dispatcher{h: h}
}

// Dispatch executes one bot command. Returns the reply Markdown, an
// optional inline keyboard for /menu, and any error.
func (d *dispatcher) Dispatch(ctx context.Context, cmd string, args []string, msg *tgbotapi.Message) (string, *tgbotapi.InlineKeyboardMarkup, error) {
	chatID := msg.Chat.ID

	switch cmd {
	case "/start":
		s, err := d.h.Start(ctx, chatID)
		return s, nil, err
	case "/menu":
		return d.h.Menu(ctx, chatID)
	case "/status":
		s, err := d.h.Status(ctx)
		return s, nil, err
	case "/exits":
		if len(args) >= 2 && args[0] == "switch" {
			s, err := d.h.SwitchExit(ctx, args[1])
			return s, nil, err
		}
		if len(args) >= 3 && args[0] == "add" {
			s, err := d.h.AddExit(ctx, args[1], strings.Join(args[2:], " "))
			return s, nil, err
		}
		if len(args) >= 2 && args[0] == "del" {
			s, err := d.h.DeleteExit(ctx, args[1])
			return s, nil, err
		}
		s, err := d.h.ListExits(ctx)
		return s, nil, err
	case "/rules":
		s, err := d.h.ListRules(ctx)
		return s, nil, err
	case "/a":
		return fmt.Sprintf("*current exit*\n\n%s", mustDo(d.h.ListExits(ctx))), nil, nil
	case "/cancel":
		s, err := d.h.Cancel(ctx, chatID)
		return s, nil, err
	case "/dev":
		s, err := d.h.Dev(ctx)
		return s, nil, err
	case "/id":
		s, err := d.h.AsID(chatID, msg.From.UserName)
		return s, nil, err
	case "/ip":
		s, err := d.h.ExitIP(ctx)
		return s, nil, err
	case "/json":
		s, err := d.h.JSON(ctx)
		return s, nil, err
	case "/loadavg":
		s, err := d.h.LoadAvg(ctx)
		return s, nil, err
	case "/meminfo":
		s, err := d.h.MemInfo(ctx)
		return s, nil, err
	case "/nf_conntrack_count":
		s, err := d.h.NFConntrack(ctx)
		return s, nil, err
	case "/route":
		s, err := d.h.Route(ctx)
		return s, nil, err
	case "/stat":
		s, err := d.h.CPUStat(ctx)
		return s, nil, err
	case "/tcp":
		s, err := d.h.TCPStat(ctx)
		return s, nil, err
	case "/uptime":
		s, err := d.h.Uptime(ctx)
		return s, nil, err
	case "/wireguard":
		s, err := d.h.WireGuard(ctx)
		return s, nil, err
	case "/www":
		s, err := d.h.WWW(ctx)
		return s, nil, err
	}
	if strings.HasPrefix(cmd, "/") {
		return "unknown command — /menu to list options", nil, nil
	}
	return "", nil, nil
}

func mustDo(s string, err error) string {
	if err != nil {
		return fmt.Sprintf("_error: %v_", err)
	}
	return s
}

// DispatchCommands enumerates every command the dispatcher understands.
// Useful for /menu keyboards and for the M2 tgbot-legacy-commands doc.
func DispatchCommands() []string {
	return []string{
		"/start", "/menu", "/status", "/exits", "/rules",
		"/a", "/cancel", "/dev", "/id", "/ip",
		"/json", "/loadavg", "/meminfo", "/nf_conntrack_count",
		"/route", "/stat", "/tcp", "/uptime", "/wireguard", "/www",
	}
}

// NotImplemented is a convenience for stub handler methods.
var NotImplemented = errors.New("tgbot: not implemented")
