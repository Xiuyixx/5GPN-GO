package api

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"time"
)

// MetricsSample is one point in the ring buffer.
type MetricsSample struct {
	TS       time.Time `json:"ts"`
	CPU      float64   `json:"cpu"`
	MemBytes int64     `json:"mem_bytes"`
	Conns    int64     `json:"conns"`
	TxBytes  int64     `json:"tx_bytes"`
	RxBytes  int64     `json:"rx_bytes"`
}

// M2 S2 stub — returns synthetic samples so the panel can render the
// Dashboard against a live shape. M2 S4 wires the real /proc + netlink
// collector and persists to SQLite metrics_snapshot.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	const points = 60
	now := time.Now()
	out := make([]MetricsSample, points)
	seed := rand.New(rand.NewSource(now.Unix() / 5))
	for i := range points {
		out[i] = MetricsSample{
			TS:       now.Add(-time.Duration(points-i-1) * 5 * time.Second),
			CPU:      15 + 10*seed.Float64(),
			MemBytes: int64(180+40*seed.Float64()) * 1024 * 1024,
			Conns:    int64(80 + 30*seed.Intn(3)),
			TxBytes:  int64(seed.Intn(2_000_000)),
			RxBytes:  int64(seed.Intn(2_000_000)),
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// LogsUnit is the SSE stub payload (M2 S4 replaces with real journalctl pipe).
func (s *Server) handleLogsSSE(w http.ResponseWriter, r *http.Request) {
	unit := r.URL.Query().Get("unit")
	if unit == "" {
		unit = "5gpn"
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
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-tick.C:
			line, _ := json.Marshal(map[string]any{
				"ts":     t.UTC().Format(time.RFC3339Nano),
				"unit":   unit,
				"level":  "info",
				"msg":    "stub log line — M2 S4 wires real journalctl",
				"seq":    i,
			})
			_, _ = w.Write([]byte("data: " + string(line) + "\n\n"))
			flusher.Flush()
			i++
		}
	}
}
