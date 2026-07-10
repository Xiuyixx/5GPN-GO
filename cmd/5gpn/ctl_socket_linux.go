//go:build linux

package main

// ctl_socket_linux.go implements the server side of the 5gpn-ctl Unix
// socket. See cmd/5gpn-ctl/main.go for the wire protocol; the summary:
//
//	>>> {"cmd": "<name>", "args": { ... }}      one JSON line
//	<<< {"ok": true|false, "error": "...",      one JSON line
//	     "data": { ... }}                       (connection closes)
//
// Authentication is SO_PEERCRED — the peer uid must equal the daemon
// uid (os.Getuid()) or be root. There is no token, no shared secret,
// no fallback. The socket lives at /run/5gpn/ctl.sock with 0700
// permissions; if /run/5gpn is not writable we fall back to
// /tmp/5gpn.sock. The systemd unit should carry:
//
//	[Service]
//	RuntimeDirectory=5gpn
//	RuntimeDirectoryMode=0755
//
// so /run/5gpn exists and is owned by the daemon uid.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/core"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

const (
	ctlSocketDir      = "/run/5gpn"
	ctlSocketPath     = "/run/5gpn/ctl.sock"
	ctlSocketFallback = "/tmp/5gpn.sock"
	ctlConnDeadline   = 30 * time.Second
	ctlSocketMode     = 0o700
)

// startCtlSocket listens on the ctl socket for the lifetime of ctx.
// It is called as a goroutine from main.go. All errors are logged;
// none are fatal to the daemon.
func startCtlSocket(ctx context.Context, applier *core.Applier, exits xexit.Store, dbh *sql.DB, logger *slog.Logger) {
	r := &ctlRouter{
		applier: applier,
		exits:   exits,
		db:      dbh,
		logger:  logger,
		startAt: time.Now(),
	}
	if err := r.serve(ctx); err != nil {
		logger.Error("ctl socket exited with error", "err", err)
	}
}

type ctlRouter struct {
	applier *core.Applier
	exits   xexit.Store
	db      *sql.DB
	logger  *slog.Logger
	startAt time.Time
}

// serve owns the socket lifecycle: create, listen, accept-loop, close.
// Returns on ctx cancellation.
func (r *ctlRouter) serve(ctx context.Context) error {
	path, err := prepareSocketPath(r.logger)
	if err != nil {
		return err
	}

	// Best-effort unlink of a stale socket from a prior run. Only OK
	// because we bound the process to a single daemon uid via systemd.
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("ctl socket: listen %s: %w", path, err)
	}
	defer ln.Close()
	defer os.Remove(path)

	// 0700 keeps other users off even if the runtime dir permissions
	// slip. Ownership is inherited from the daemon uid.
	if err := os.Chmod(path, ctlSocketMode); err != nil {
		r.logger.Warn("ctl socket chmod failed", "path", path, "err", err)
	}

	r.logger.Info("ctl socket listening", "path", path)

	// Close the listener when ctx cancels — this unblocks Accept().
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			r.logger.Warn("ctl accept failed", "err", err)
			continue
		}
		go r.handleConn(ctx, conn)
	}
}

// prepareSocketPath ensures the socket directory exists and returns
// the path we should bind to. Prefers /run/5gpn/ctl.sock; falls back
// to /tmp/5gpn.sock when the runtime dir is missing or unwritable.
func prepareSocketPath(logger *slog.Logger) (string, error) {
	if err := os.MkdirAll(ctlSocketDir, 0o755); err == nil {
		// Confirm we can actually write in it. Testing with a probe
		// file avoids surprises when MkdirAll returns success against
		// a pre-existing directory the daemon uid cannot write to.
		probe := filepath.Join(ctlSocketDir, ".probe")
		if f, ferr := os.Create(probe); ferr == nil {
			_ = f.Close()
			_ = os.Remove(probe)
			return ctlSocketPath, nil
		}
	}
	logger.Warn("ctl socket: /run/5gpn unavailable — falling back to /tmp/5gpn.sock")
	return ctlSocketFallback, nil
}

// handleConn runs one request/response cycle then closes the conn.
// The connection is a UnixConn — assert to reach SO_PEERCRED.
func (r *ctlRouter) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	uc, ok := conn.(*net.UnixConn)
	if !ok {
		r.writeResp(conn, response{OK: false, Error: "internal: not a unix conn"})
		return
	}

	if err := r.authorize(uc); err != nil {
		r.logger.Warn("ctl authz reject", "err", err)
		r.writeResp(conn, response{OK: false, Error: err.Error()})
		return
	}

	if err := conn.SetDeadline(time.Now().Add(ctlConnDeadline)); err != nil {
		r.writeResp(conn, response{OK: false, Error: "set deadline: " + err.Error()})
		return
	}

	var req request
	dec := json.NewDecoder(bufio.NewReader(conn))
	if err := dec.Decode(&req); err != nil {
		if err == io.EOF {
			return
		}
		r.writeResp(conn, response{OK: false, Error: "decode request: " + err.Error()})
		return
	}

	resp := r.dispatch(ctx, req)
	r.writeResp(conn, resp)
}

// authorize enforces SO_PEERCRED: peer uid must equal daemon uid or be
// root (0). Anyone else — even a member of the daemon's group — is
// rejected.
func (r *ctlRouter) authorize(uc *net.UnixConn) error {
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("syscall conn: %w", err)
	}
	var ucred *syscall.Ucred
	var sockErr error
	err = raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return fmt.Errorf("peer cred control: %w", err)
	}
	if sockErr != nil {
		return fmt.Errorf("peer cred lookup: %w", sockErr)
	}
	daemonUID := uint32(os.Getuid())
	if ucred.Uid != daemonUID && ucred.Uid != 0 {
		return fmt.Errorf("peer uid %d not authorized", ucred.Uid)
	}
	return nil
}

func (r *ctlRouter) writeResp(conn net.Conn, resp response) {
	body, err := json.Marshal(resp)
	if err != nil {
		return
	}
	body = append(body, '\n')
	_, _ = conn.Write(body)
}

// request / response are duplicated from cmd/5gpn-ctl/main.go so the
// server can decode without importing the client package. Keep the
// field names in lockstep.
type request struct {
	Cmd  string         `json:"cmd"`
	Args map[string]any `json:"args,omitempty"`
}

type response struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

// dispatch is the intentionally-small subcommand router. Do NOT reuse
// authMiddleware — SO_PEERCRED already gates the connection.
func (r *ctlRouter) dispatch(ctx context.Context, req request) response {
	switch req.Cmd {
	case "version":
		return okResp(map[string]any{"version": version})
	case "status":
		return r.cmdStatus(ctx)
	case "exits.list":
		return r.cmdExitsList(ctx)
	case "exits.switch":
		exitID, _ := req.Args["exit_id"].(string)
		return r.cmdExitsSwitch(ctx, exitID)
	case "rules.rollback":
		return r.cmdRulesRollback(ctx)
	case "chinalist.sync":
		return r.cmdChinalistSync(ctx)
	default:
		return errResp("unknown cmd: " + req.Cmd)
	}
}

func (r *ctlRouter) cmdStatus(ctx context.Context) response {
	activeExit := ""
	if r.exits != nil {
		if e, err := r.exits.Active(ctx); err == nil {
			activeExit = e.ExitID
		}
	}
	ruleCount := 0
	if r.applier != nil && r.applier.BaseConfig != nil {
		if active, err := db.GetActiveRuleVersion(r.db); err == nil && active != nil {
			if set, perr := rules.ParseYAML([]byte(active.RulesYAML)); perr == nil {
				ruleCount = len(set.Rules)
			}
		}
	}
	return okResp(map[string]any{
		"version":     version,
		"uptime":      time.Since(r.startAt).Round(time.Second).String(),
		"active_exit": activeExit,
		"rule_count":  ruleCount,
	})
}

func (r *ctlRouter) cmdExitsList(ctx context.Context) response {
	if r.exits == nil {
		return errResp("exit store unavailable")
	}
	items, err := r.exits.List(ctx)
	if err != nil {
		return errResp(err.Error())
	}
	out := make([]map[string]any, 0, len(items))
	for _, e := range items {
		out = append(out, map[string]any{
			"exit_id":  e.ExitID,
			"name":     e.Name,
			"protocol": e.Protocol,
			"uri":      e.URI,
			"active":   e.Active,
		})
	}
	return okResp(map[string]any{"exits": out})
}

func (r *ctlRouter) cmdExitsSwitch(ctx context.Context, exitID string) response {
	if exitID == "" {
		return errResp("missing exit_id")
	}
	if r.applier == nil {
		return errResp("applier unavailable")
	}
	res, err := r.applier.SwitchExit(ctx, exitID)
	if err != nil {
		return errResp(err.Error())
	}
	return okResp(map[string]any{
		"exit_id":     exitID,
		"snapshot_id": res.SnapshotID,
		"health":      res.Health,
		"rolled_back": res.RolledBack,
		"reason":      res.Reason,
	})
}

// cmdRulesRollback re-activates the rule_version tied to the previous
// snapshot. Mirrors internal/api/snapshots.go handleRollbackSnapshot
// but selects the target snapshot itself (most recent that is NOT the
// currently-active rule version's snapshot).
func (r *ctlRouter) cmdRulesRollback(ctx context.Context) response {
	if r.db == nil {
		return errResp("db unavailable")
	}
	active, err := db.GetActiveRuleVersion(r.db)
	if err != nil {
		return errResp("no active rule_version: " + err.Error())
	}
	versions, err := db.ListRuleVersions(r.db, 500)
	if err != nil {
		return errResp(err.Error())
	}
	var target *db.RuleVersion
	for i := range versions {
		v := &versions[i]
		if v.ID == active.ID {
			continue
		}
		target = v
		break
	}
	if target == nil {
		return errResp("no prior rule_version to roll back to")
	}
	if err := db.SetActiveRuleVersion(r.db, target.ID); err != nil {
		return errResp(err.Error())
	}
	_ = db.AppendAudit(r.db, db.AuditEntry{
		Actor:  "ctl",
		Action: "rules.rollback",
		Target: fmt.Sprintf("rule_version=%d snapshot=%d", target.ID, target.SnapshotID),
		Result: "ok",
	})
	return okResp(map[string]any{
		"rule_version_id": target.ID,
		"snapshot_id":     target.SnapshotID,
	})
}

func (r *ctlRouter) cmdChinalistSync(ctx context.Context) response {
	if r.applier == nil || r.applier.BaseConfig == nil {
		return errResp("config unavailable")
	}
	cfg := r.applier.BaseConfig
	source := cfg.DNS.ChinaListSource
	path := cfg.DNS.ChinaListPath
	if source == "" {
		return errResp("dns.chinalist_source not configured")
	}
	if path == "" {
		path = "/var/lib/5gpn/chinalist.txt"
	}
	if err := rules.Sync(ctx, r.db, source, path); err != nil {
		return errResp(err.Error())
	}
	_ = db.AppendAudit(r.db, db.AuditEntry{
		Actor:  "ctl",
		Action: "chinalist.sync",
		Target: source,
		Result: "ok",
	})
	return okResp(map[string]any{
		"source": source,
		"path":   path,
	})
}

func okResp(data map[string]any) response { return response{OK: true, Data: data} }
func errResp(msg string) response          { return response{OK: false, Error: msg} }
