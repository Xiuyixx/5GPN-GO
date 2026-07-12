package tgbot

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	xrules "github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// DefaultHandlers backs the bot with the same database + orchestrator the
// panel uses. Method bodies favor short, cheap /proc reads so the bot
// stays useful on tiny VPSes; heavy work stays behind the panel API.
type DefaultHandlers struct {
	DB        *sql.DB
	Logger    *slog.Logger
	Startup   int64 // unix seconds, for /uptime
	ExitState *ExitState
	IOSPort   int
}

// ExitState is a minimal registry so the bot can serve /exits and
// /exits switch without depending on the panel's in-memory list. Wired
// by daemon glue.
type ExitState struct {
	Items  func() []Exit
	Switch func(id string) error
	Add    func(id, uri string) error
	Delete func(id string) error
	Active func() string
}

// Exit is a slim view used for /exits.
type Exit struct {
	ID       string
	Protocol string
	Server   string
	Port     int
	Active   bool
}

func (d *DefaultHandlers) Start(_ context.Context, _ int64) (string, error) {
	return `*5gpn bot*
send /menu for the interactive dashboard, or /status for one-shot health.`, nil
}

func (d *DefaultHandlers) Menu(_ context.Context, _ int64) (string, *tgbotapi.InlineKeyboardMarkup, error) {
	rows := [][]tgbotapi.InlineKeyboardButton{
		{
			tgbotapi.NewInlineKeyboardButtonData("status", "status"),
			tgbotapi.NewInlineKeyboardButtonData("exits", "exits"),
			tgbotapi.NewInlineKeyboardButtonData("rules", "rules"),
		},
		{
			tgbotapi.NewInlineKeyboardButtonData("cpu", "stat"),
			tgbotapi.NewInlineKeyboardButtonData("mem", "meminfo"),
			tgbotapi.NewInlineKeyboardButtonData("uptime", "uptime"),
		},
		{
			tgbotapi.NewInlineKeyboardButtonData("route", "route"),
			tgbotapi.NewInlineKeyboardButtonData("tcp", "tcp"),
			tgbotapi.NewInlineKeyboardButtonData("wireguard", "wireguard"),
		},
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	return "*menu* — tap a button, or type /help for the full command list.", &kb, nil
}

func (d *DefaultHandlers) Status(ctx context.Context) (string, error) {
	uptime, _ := d.Uptime(ctx)
	load, _ := d.LoadAvg(ctx)
	mem, _ := d.MemInfo(ctx)
	active := "—"
	if d.ExitState != nil && d.ExitState.Active != nil {
		active = d.ExitState.Active()
	}
	return fmt.Sprintf("*status*\n%s\n%s\n%s\nexit: `%s`", uptime, load, mem, active), nil
}

func (d *DefaultHandlers) ListExits(_ context.Context) (string, error) {
	if d.ExitState == nil || d.ExitState.Items == nil {
		return "no exits configured", nil
	}
	items := d.ExitState.Items()
	if len(items) == 0 {
		return "no exits configured", nil
	}
	b := &strings.Builder{}
	b.WriteString("*exits*\n")
	for _, e := range items {
		mark := "·"
		if e.Active {
			mark = "▶"
		}
		fmt.Fprintf(b, "%s `%s` %s %s:%d\n", mark, e.ID, e.Protocol, e.Server, e.Port)
	}
	return b.String(), nil
}

func (d *DefaultHandlers) ListRules(_ context.Context) (string, error) {
	active, err := db.GetActiveRuleVersion(d.DB)
	if err == db.ErrNoRows {
		return "no active rule version yet — apply from the panel first", nil
	}
	if err != nil {
		return "", err
	}
	set, err := xrules.ParseYAML([]byte(active.RulesYAML))
	if err != nil {
		return "", err
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "*rules* — %d entries (v#%d)\n", len(set.Rules), active.ID)
	for _, r := range set.Rules {
		enabled := "on"
		if !r.Enabled {
			enabled = "off"
		}
		fmt.Fprintf(b, "· `%s` %s %s → %s (%s)\n", r.ID, r.Kind, r.Pattern, r.Action, enabled)
	}
	return b.String(), nil
}

func (d *DefaultHandlers) SwitchExit(_ context.Context, id string) (string, error) {
	if d.ExitState == nil || d.ExitState.Switch == nil {
		return "", fmt.Errorf("exit switching not wired")
	}
	if err := d.ExitState.Switch(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("switched to `%s`", id), nil
}

func (d *DefaultHandlers) AddExit(_ context.Context, id, uri string) (string, error) {
	if _, err := xexit.Parse(uri); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if d.ExitState == nil || d.ExitState.Add == nil {
		return "", fmt.Errorf("exit add not wired")
	}
	if err := d.ExitState.Add(id, uri); err != nil {
		return "", err
	}
	return fmt.Sprintf("added `%s`", id), nil
}

func (d *DefaultHandlers) DeleteExit(_ context.Context, id string) (string, error) {
	if d.ExitState == nil || d.ExitState.Delete == nil {
		return "", fmt.Errorf("exit delete not wired")
	}
	if err := d.ExitState.Delete(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted `%s`", id), nil
}

func (d *DefaultHandlers) Uptime(_ context.Context) (string, error) {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/uptime"); err == nil {
			parts := strings.Fields(string(raw))
			if len(parts) > 0 {
				return fmt.Sprintf("uptime: `%ss`", parts[0]), nil
			}
		}
	}
	return "uptime: (unavailable off Linux)", nil
}

func (d *DefaultHandlers) LoadAvg(_ context.Context) (string, error) {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
			return "load: `" + strings.TrimSpace(string(raw)) + "`", nil
		}
	}
	return "load: (unavailable off Linux)", nil
}

func (d *DefaultHandlers) MemInfo(_ context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "mem: (unavailable off Linux)", nil
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "", err
	}
	b := &strings.Builder{}
	b.WriteString("*meminfo*\n")
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemTotal:") ||
			strings.HasPrefix(line, "MemAvailable:") ||
			strings.HasPrefix(line, "SwapTotal:") ||
			strings.HasPrefix(line, "SwapFree:") {
			b.WriteString("· ")
			b.WriteString(strings.TrimSpace(line))
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (d *DefaultHandlers) CPUStat(_ context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "cpu stat: (unavailable off Linux)", nil
	}
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			return "cpu: `" + strings.TrimSpace(line) + "`", nil
		}
	}
	return "cpu: (no aggregate line)", nil
}

func (d *DefaultHandlers) TCPStat(_ context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "tcp: (unavailable off Linux)", nil
	}
	raw, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("tcp sockets: `%d`", strings.Count(string(raw), "\n")-1), nil
}

func (d *DefaultHandlers) NFConntrack(_ context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "conntrack: (unavailable off Linux)", nil
	}
	raw, err := os.ReadFile("/proc/sys/net/netfilter/nf_conntrack_count")
	if err != nil {
		return "", err
	}
	return "conntrack: `" + strings.TrimSpace(string(raw)) + "`", nil
}

func (d *DefaultHandlers) Route(_ context.Context) (string, error) {
	return runShort("ip", "route")
}

func (d *DefaultHandlers) ExitIP(_ context.Context) (string, error) {
	// This probes the daemon host's direct egress. It does not prove the
	// selected mihomo exit path.
	out, err := runShort("curl", "-sf", "--max-time", "6", "https://ipinfo.io/ip")
	if err != nil {
		return "", err
	}
	return "host egress ip (not active-exit probe): `" + strings.TrimSpace(out) + "`", nil
}

func (d *DefaultHandlers) WireGuard(_ context.Context) (string, error) {
	out, err := runShort("wg", "show")
	if err != nil {
		return "wireguard not installed / no interfaces", nil
	}
	return "*wireguard*\n```\n" + out + "\n```", nil
}

func (d *DefaultHandlers) Dev(_ context.Context) (string, error) {
	out, err := runShort("ip", "-brief", "link", "show")
	if err != nil {
		return "", err
	}
	return "*interfaces*\n```\n" + out + "\n```", nil
}

func (d *DefaultHandlers) WWW(_ context.Context) (string, error) {
	port := d.IOSPort
	if port <= 0 {
		return "iOS encrypted DNS profile: `/ios-dot.mobileconfig` on the panel HTTPS endpoint", nil
	}
	return fmt.Sprintf("legacy iOS asset listener: `/ios-dot.mobileconfig` on :%d", port), nil
}

func (d *DefaultHandlers) JSON(_ context.Context) (string, error) {
	out, err := runShort("ip", "-json", "-4", "route", "show", "default")
	if err != nil {
		return "", err
	}
	return "```json\n" + out + "\n```", nil
}

func (d *DefaultHandlers) Cancel(_ context.Context, chatID int64) (string, error) {
	return fmt.Sprintf("ok (cancel · chat=%d)", chatID), nil
}

func (d *DefaultHandlers) AsID(chatID int64, from string) (string, error) {
	return fmt.Sprintf("chat id: `%s` · user: `%s`", strconv.FormatInt(chatID, 10), from), nil
}

func (d *DefaultHandlers) RecordUnauthorized(chatID int64, from string) {
	_ = db.AppendAudit(d.DB, db.AuditEntry{
		Actor:  "tgbot:" + from,
		Action: "tgbot.unauthorized",
		Target: strconv.FormatInt(chatID, 10),
		Result: "denied",
	})
}

// runShort executes cmd with a 6s ceiling and returns combined output.
// Trims trailing whitespace so replies look tight.
func runShort(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}
