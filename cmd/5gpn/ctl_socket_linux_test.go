//go:build linux

package main

import (
	"context"
	"testing"
	"time"
)

// TestDispatch_Version confirms the router returns the daemon version
// string on a "version" cmd with no DB or applier wired.
func TestDispatch_Version(t *testing.T) {
	r := &ctlRouter{startAt: time.Now()}
	resp := r.dispatch(context.Background(), request{Cmd: "version"})
	if !resp.OK {
		t.Fatalf("version: want ok, got error %q", resp.Error)
	}
	if resp.Data["version"] != version {
		t.Errorf("version data = %v, want %q", resp.Data["version"], version)
	}
}

// TestDispatch_UnknownCmd rejects unknown subcommands with a clear error.
func TestDispatch_UnknownCmd(t *testing.T) {
	r := &ctlRouter{startAt: time.Now()}
	resp := r.dispatch(context.Background(), request{Cmd: "bogus"})
	if resp.OK {
		t.Fatalf("unknown cmd: want !ok, got %+v", resp)
	}
	if resp.Error == "" {
		t.Errorf("unknown cmd: expected error message, got empty")
	}
}

// TestDispatch_ExitsSwitchEmptyID rejects blank exit_id args before
// touching the applier.
func TestDispatch_ExitsSwitchEmptyID(t *testing.T) {
	r := &ctlRouter{startAt: time.Now()}
	resp := r.dispatch(context.Background(), request{
		Cmd:  "exits.switch",
		Args: map[string]any{"exit_id": ""},
	})
	if resp.OK {
		t.Fatalf("empty exit_id: want !ok, got %+v", resp)
	}
}

// TestDispatch_StatusZeroWired returns a status snapshot even when no
// exit store or DB is wired — fields are zero-valued but the shape
// is stable for the client renderer.
func TestDispatch_StatusZeroWired(t *testing.T) {
	r := &ctlRouter{startAt: time.Now().Add(-2 * time.Second)}
	resp := r.dispatch(context.Background(), request{Cmd: "status"})
	if !resp.OK {
		t.Fatalf("status: want ok, got error %q", resp.Error)
	}
	if _, ok := resp.Data["uptime"]; !ok {
		t.Errorf("status: uptime missing")
	}
	if _, ok := resp.Data["version"]; !ok {
		t.Errorf("status: version missing")
	}
}
