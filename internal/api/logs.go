package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported")
		return
	}
	ctx := r.Context()

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		if SSELogger != nil {
			SSELogger(ctx, unit, lines)
			return
		}
		if runtime.GOOS == "linux" {
			journalctlStream(ctx, unit, lines)
			return
		}
		stubStream(ctx, unit, lines)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
			flusher.Flush()
		}
	}
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
func journalctlStream(ctx context.Context, unit string, out chan<- string) {
	cmd := exec.CommandContext(ctx, "journalctl", "-u", unit, "-f", "-o", "json", "--no-pager")
	stdout, err := cmd.StdoutPipe()
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
		out <- string(body)
		seq++
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

func errorFrame(unit string, err error) string {
	body, _ := json.Marshal(map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"unit":  unit,
		"level": "error",
		"msg":   fmt.Sprintf("journalctl: %v", err),
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
