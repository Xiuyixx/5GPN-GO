package tgbot

import (
	"context"
	"errors"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// stubHandlers records the last call for each method so tests can
// verify dispatch without hitting a real Telegram server.
type stubHandlers struct {
	last   string
	args   []string
	unauth int
}

func (s *stubHandlers) call(name string, args ...string) (string, error) {
	s.last = name
	s.args = args
	return name, nil
}

func (s *stubHandlers) Start(_ context.Context, _ int64) (string, error)       { return s.call("start") }
func (s *stubHandlers) Menu(_ context.Context, _ int64) (string, *tgbotapi.InlineKeyboardMarkup, error) {
	_, err := s.call("menu")
	return "menu", nil, err
}
func (s *stubHandlers) Status(_ context.Context) (string, error)                { return s.call("status") }
func (s *stubHandlers) ListExits(_ context.Context) (string, error)             { return s.call("exits") }
func (s *stubHandlers) ListRules(_ context.Context) (string, error)             { return s.call("rules") }
func (s *stubHandlers) SwitchExit(_ context.Context, id string) (string, error) { return s.call("switch", id) }
func (s *stubHandlers) AddExit(_ context.Context, id, uri string) (string, error) {
	return s.call("add", id, uri)
}
func (s *stubHandlers) DeleteExit(_ context.Context, id string) (string, error) { return s.call("del", id) }
func (s *stubHandlers) Uptime(_ context.Context) (string, error)                { return s.call("uptime") }
func (s *stubHandlers) LoadAvg(_ context.Context) (string, error)               { return s.call("load") }
func (s *stubHandlers) MemInfo(_ context.Context) (string, error)               { return s.call("mem") }
func (s *stubHandlers) CPUStat(_ context.Context) (string, error)               { return s.call("cpu") }
func (s *stubHandlers) TCPStat(_ context.Context) (string, error)               { return s.call("tcp") }
func (s *stubHandlers) NFConntrack(_ context.Context) (string, error)           { return s.call("conntrack") }
func (s *stubHandlers) Route(_ context.Context) (string, error)                 { return s.call("route") }
func (s *stubHandlers) ExitIP(_ context.Context) (string, error)                { return s.call("ip") }
func (s *stubHandlers) WireGuard(_ context.Context) (string, error)             { return s.call("wg") }
func (s *stubHandlers) Dev(_ context.Context) (string, error)                   { return s.call("dev") }
func (s *stubHandlers) WWW(_ context.Context) (string, error)                   { return s.call("www") }
func (s *stubHandlers) JSON(_ context.Context) (string, error)                  { return s.call("json") }
func (s *stubHandlers) Cancel(_ context.Context, _ int64) (string, error)       { return s.call("cancel") }
func (s *stubHandlers) AsID(_ int64, _ string) (string, error)                  { return s.call("id") }
func (s *stubHandlers) RecordUnauthorized(_ int64, _ string)                    { s.unauth++ }

func TestDispatchCoversAllCommands(t *testing.T) {
	s := &stubHandlers{}
	d := newDispatcher(s)
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, From: &tgbotapi.User{UserName: "u"}}
	for _, cmd := range DispatchCommands() {
		_, _, err := d.Dispatch(context.Background(), cmd, nil, msg)
		if err != nil {
			t.Fatalf("dispatch %s: %v", cmd, err)
		}
		if s.last == "" {
			t.Fatalf("dispatch %s: no handler called", cmd)
		}
	}
}

func TestDispatchExitsSubcommands(t *testing.T) {
	s := &stubHandlers{}
	d := newDispatcher(s)
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, From: &tgbotapi.User{UserName: "u"}}

	if _, _, err := d.Dispatch(context.Background(), "/exits", []string{"switch", "wg1"}, msg); err != nil {
		t.Fatal(err)
	}
	if s.last != "switch" || len(s.args) != 1 || s.args[0] != "wg1" {
		t.Fatalf("switch dispatch: last=%s args=%v", s.last, s.args)
	}

	if _, _, err := d.Dispatch(context.Background(), "/exits", []string{"add", "wg2", "trojan://pw@example.com:443"}, msg); err != nil {
		t.Fatal(err)
	}
	if s.last != "add" || s.args[0] != "wg2" {
		t.Fatalf("add dispatch: %+v", s)
	}

	if _, _, err := d.Dispatch(context.Background(), "/exits", []string{"del", "wg2"}, msg); err != nil {
		t.Fatal(err)
	}
	if s.last != "del" || s.args[0] != "wg2" {
		t.Fatalf("del dispatch: %+v", s)
	}
}

func TestDispatchRejectsUnknown(t *testing.T) {
	s := &stubHandlers{}
	d := newDispatcher(s)
	msg := &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 42}, From: &tgbotapi.User{UserName: "u"}}
	reply, _, _ := d.Dispatch(context.Background(), "/nope", nil, msg)
	if !strings.Contains(reply, "unknown") {
		t.Fatalf("unknown cmd should hint, got: %q", reply)
	}
}

func TestSplitCommandStripsBotName(t *testing.T) {
	cases := map[string]struct {
		cmd  string
		args []string
	}{
		"/status":               {cmd: "/status"},
		"/status@botname":       {cmd: "/status"},
		"/status@botname foo":   {cmd: "/status", args: []string{"foo"}},
		"/exits switch wg1":     {cmd: "/exits", args: []string{"switch", "wg1"}},
		"/exits@bot switch wg1": {cmd: "/exits", args: []string{"switch", "wg1"}},
	}
	for input, want := range cases {
		cmd, args := splitCommand(input)
		if cmd != want.cmd {
			t.Errorf("splitCommand(%q) cmd=%q, want %q", input, cmd, want.cmd)
		}
		if len(args) != len(want.args) {
			t.Errorf("splitCommand(%q) args=%v, want %v", input, args, want.args)
			continue
		}
		for i, a := range args {
			if a != want.args[i] {
				t.Errorf("splitCommand(%q)[%d] = %q, want %q", input, i, a, want.args[i])
			}
		}
	}
}

func TestNewRefusesEmptyWhitelist(t *testing.T) {
	_, err := New(Config{Token: "x", Handlers: &stubHandlers{}, AdminChatIDs: nil})
	if err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatalf("expected whitelist refusal, got %v", err)
	}
}

func TestNewRefusesEmptyToken(t *testing.T) {
	_, err := New(Config{Token: "", Handlers: &stubHandlers{}, AdminChatIDs: []int64{1}})
	if !errors.Is(err, ErrBotDisabled) {
		t.Fatalf("expected ErrBotDisabled, got %v", err)
	}
}

func TestNewRefusesNilHandlers(t *testing.T) {
	_, err := New(Config{Token: "x", Handlers: nil, AdminChatIDs: []int64{1}})
	if err == nil {
		t.Fatal("expected error for nil handlers")
	}
}

func TestDispatchCommandsListCount(t *testing.T) {
	got := DispatchCommands()
	if len(got) != 20 {
		t.Fatalf("expected 20 commands, got %d: %v", len(got), got)
	}
}
