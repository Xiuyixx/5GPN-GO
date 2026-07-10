package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/metrics"
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

// handleMetrics returns the tail of the SQLite metrics_snapshot ring.
// M2 S4: the daemon now runs a real /proc-based collector alongside the
// API; the panel Dashboard reads through this endpoint.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	limit := 60
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 5000 {
		limit = n
	}
	rows, err := metrics.ListRecent(s.DB, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]MetricsSample, len(rows))
	for i, r := range rows {
		out[i] = MetricsSample(r)
	}
	writeJSON(w, http.StatusOK, out)
}

