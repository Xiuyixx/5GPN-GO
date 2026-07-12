package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// SSELogger is set by the daemon at startup. Nil means "use the built-in
// journalctl driver on Linux, or a friendly stub on other hosts".
var SSELogger func(ctx context.Context, unit string, out chan<- string)

// M2 S4: pipe real `journalctl -u <unit> -f -o json` to the SSE stream
// on Linux hosts; fall back to a stub on macOS/tests so the panel still
// renders something.
//
// The SSE session outlives the underlying journalctl process: when
// journalctl exits (permission denied, unit doesn't exist, etc.) the
// session stays open with a periodic keepalive so the panel status pill
// stays "connected" instead of oscillating, and the error stderr from
// journalctl is surfaced as a log frame so the operator can see WHY
// there are no entries. Session closes only when the client disconnects
// or auth is invalid.
func (s *Server) handleLogsSSE(w http.ResponseWriter, r *http.Request) {
	unit := strings.TrimSpace(r.URL.Query().Get("unit"))
	if unit == "" {
		unit = "5gpn"
	}
	// Basic sanitization — reject anything that looks like a shell arg.
	for _, c := range unit {
		if !isUnitChar(c) {
			writeError(w, http.StatusBadRequest, "bad_unit", "unit must be alphanumeric plus - _ . @")
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable buffering under nginx-style proxies
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported")
		return
	}
	// Flush headers immediately so EventSource fires onopen before we
	// wait on the first log line.
	flusher.Flush()
	ctx := r.Context()

	// Announce the stream so the client has a definite onopen payload.
	writeSSE(w, flusher, helloFrame(unit))

	lines := make(chan string, 32)
	go func() {
		// Do not close `lines` — the outer loop keys off ctx.Done for
		// termination. Closing would race the outer receive and cause
		// early return before keepalives can hold the session open.
		if SSELogger != nil {
			SSELogger(ctx, unit, lines)
		} else if runtime.GOOS == "linux" {
			journalctlStream(ctx, unit, lines)
		} else {
			stubStream(ctx, unit, lines)
		}
		// Producer done. Park until the client disconnects so the SSE
		// session stays alive under keepalives.
		<-ctx.Done()
	}()

	// Keepalive comment every 15s so idle streams do not get culled by
	// proxies or SSH tunnels. Colon prefix marks it as an SSE comment;
	// the browser ignores it but keeps the socket warm.
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case line := <-lines:
			writeSSE(w, flusher, line)
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, line string) {
	_, _ = w.Write([]byte("data: " + line + "\n\n"))
	flusher.Flush()
}

func isUnitChar(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return c == '-' || c == '_' || c == '.' || c == '@'
}

// journalctlStream pipes real journalctl JSON events into out. Each SSE
// data frame is a JSON object with the shape the frontend already
// consumes ({ts, level, msg, unit, seq}). Extra journalctl fields are
// dropped so we don't leak internal metadata to the panel.
//
// journalctl writes structured JSON to stdout and human-readable errors
// (e.g. "No journal files were opened due to insufficient permissions.")
// to stderr, so both streams are drained and stderr becomes error
// frames. Returns when journalctl exits or ctx is cancelled.
func journalctlStream(ctx context.Context, unit string, out chan<- string) {
	cmd := exec.CommandContext(ctx, "journalctl", "-u", unit, "-f", "-o", "json", "--no-pager")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		out <- errorFrame(unit, err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		out <- errorFrame(unit, err)
		return
	}
	if err := cmd.Start(); err != nil {
		out <- errorFrame(unit, err)
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Drain stderr in the background so we surface things like
	// "insufficient permissions" or "Failed to look up unit" as visible
	// log frames instead of silently disconnecting.
	go drainStderr(ctx, unit, stderr, out)

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var seq int
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		payload := map[string]any{
			"unit":  unit,
			"seq":   seq,
			"ts":    journalTimestamp(raw),
			"level": journalPriority(raw),
			"msg":   asString(raw["MESSAGE"]),
		}
		body, _ := json.Marshal(payload)
		// Guard the send with ctx.Done() — a bare `out <- ...` blocks
		// when the SSE client has disconnected (nobody's receiving),
		// which leaves this goroutine wedged forever and the deferred
		// cmd.Process.Kill() / cmd.Wait() never runs — so the child
		// journalctl process (and its stdout/stderr pipe FDs held by
		// this daemon) leak. Under repeated Logs-tab open/close this
		// consumed thousands of pipe FDs on VPS in the wild.
		select {
		case out <- string(body):
		case <-ctx.Done():
			return
		}
		seq++
	}
	// If journalctl exits without producing entries — the common
	// permission-denied case, where stderr already told us why — do
	// nothing extra. If it dies with a real Wait error, surface it.
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		select {
		case out <- errorFrame(unit, fmt.Errorf("journalctl exited: %w", err)):
		case <-ctx.Done():
		}
	}
}

// drainStderr forwards each line of journalctl stderr as an error frame.
// Typical lines:
//   - "No journal files were opened due to insufficient permissions."
//   - "Failed to look up unit '<name>.service'."
func drainStderr(ctx context.Context, unit string, r io.Reader, out chan<- string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		select {
		case out <- errorFrame(unit, fmt.Errorf("journalctl: %s", line)):
		case <-ctx.Done():
			return
		}
	}
}

func stubStream(ctx context.Context, unit string, out chan<- string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-tick.C:
			body, _ := json.Marshal(map[string]any{
				"ts":    t.UTC().Format(time.RFC3339Nano),
				"unit":  unit,
				"level": "info",
				"msg":   "stub log line — journalctl unavailable on this host",
				"seq":   i,
			})
			out <- string(body)
			i++
		}
	}
}

// helloFrame is emitted right after the SSE headers so the client sees
// something on onopen even before any real log entry arrives.
func helloFrame(unit string) string {
	body, _ := json.Marshal(map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"unit":  unit,
		"level": "info",
		"msg":   fmt.Sprintf("stream opened for unit %q", unit),
		"seq":   0,
	})
	return string(body)
}

func errorFrame(unit string, err error) string {
	body, _ := json.Marshal(map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"unit":  unit,
		"level": "error",
		"msg":   fmt.Sprintf("%v", err),
		"seq":   0,
	})
	return string(body)
}

func journalTimestamp(raw map[string]any) string {
	// journalctl -o json emits __REALTIME_TIMESTAMP as a microsecond epoch.
	if v, ok := raw["__REALTIME_TIMESTAMP"]; ok {
		if s := asString(v); s != "" {
			return s
		}
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func journalPriority(raw map[string]any) string {
	pri := asString(raw["PRIORITY"])
	switch pri {
	case "0", "1", "2":
		return "error"
	case "3":
		return "error"
	case "4":
		return "warn"
	case "5", "6":
		return "info"
	case "7":
		return "debug"
	}
	return "info"
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	}
	return ""
}
