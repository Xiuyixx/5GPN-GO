// Package metrics samples host + daemon-visible counters every N seconds
// and persists them into the SQLite metrics_snapshot ring. The API layer
// reads from that ring instead of synthesizing samples.
//
// On non-Linux hosts /proc reads fail; the collector still writes zero
// samples so the panel keeps rendering a live chart during dev.
package metrics

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Sample matches the metrics_snapshot row + Server.MetricsSample JSON.
type Sample struct {
	TS       time.Time `json:"ts"`
	CPU      float64   `json:"cpu"`
	MemBytes int64     `json:"mem_bytes"`
	Conns    int64     `json:"conns"`
	TxBytes  int64     `json:"tx_bytes"`
	RxBytes  int64     `json:"rx_bytes"`
}

// Config controls the collector.
type Config struct {
	DB         *sql.DB
	Interval   time.Duration // default 10s
	Retention  time.Duration // default 24h
	Logger     *slog.Logger
	Iface      string        // network interface for tx/rx counters; auto-detect if empty
}

// Collector owns the sample loop and the SQLite ring.
type Collector struct {
	cfg      Config
	log      *slog.Logger
	prevCPU  cpuSnapshot
	prevNet  netSnapshot
}

// New builds a Collector.
func New(cfg Config) *Collector {
	if cfg.Interval == 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.Retention == 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Collector{cfg: cfg, log: cfg.Logger}
}

// Run samples on Config.Interval until ctx is done.
func (c *Collector) Run(ctx context.Context) {
	tick := time.NewTicker(c.cfg.Interval)
	defer tick.Stop()
	// Take an initial reading so the first tick has a delta to compute against.
	c.prevCPU, _ = readCPU()
	c.prevNet, _ = readNet(c.cfg.Iface)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s := c.Sample()
			if c.cfg.DB != nil {
				if err := insertSample(c.cfg.DB, s); err != nil {
					c.log.Warn("metrics: insert", "err", err)
				}
				if err := trim(c.cfg.DB, c.cfg.Retention); err != nil {
					c.log.Warn("metrics: trim", "err", err)
				}
			}
		}
	}
}

// Sample takes a single point-in-time reading. Public so the API layer
// can seed the panel with something on a cold start.
func (c *Collector) Sample() Sample {
	now := time.Now().UTC()
	cpu, _ := readCPU()
	net, _ := readNet(c.cfg.Iface)
	mem, _ := readMemBytes()
	conns, _ := readTCPConns()

	cpuPct := 0.0
	if c.prevCPU.total > 0 && cpu.total > c.prevCPU.total {
		diff := cpu.total - c.prevCPU.total
		idle := cpu.idle - c.prevCPU.idle
		if diff > 0 && idle >= 0 {
			cpuPct = float64(diff-idle) / float64(diff) * 100.0
		}
	}
	c.prevCPU = cpu

	tx := int64(0)
	rx := int64(0)
	if c.prevNet.tx > 0 {
		tx = int64(net.tx - c.prevNet.tx)
		rx = int64(net.rx - c.prevNet.rx)
		if tx < 0 {
			tx = 0
		}
		if rx < 0 {
			rx = 0
		}
	}
	c.prevNet = net

	return Sample{
		TS:       now,
		CPU:      cpuPct,
		MemBytes: mem,
		Conns:    conns,
		TxBytes:  tx,
		RxBytes:  rx,
	}
}

// ListRecent pulls the tail of the ring for the panel Dashboard.
func ListRecent(db *sql.DB, limit int) ([]Sample, error) {
	if limit <= 0 || limit > 5000 {
		limit = 60
	}
	rows, err := db.Query(
		`SELECT ts, cpu, mem, conns, tx_bytes, rx_bytes
		   FROM metrics_snapshot ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Sample, 0, limit)
	for rows.Next() {
		var s Sample
		if err := rows.Scan(&s.TS, &s.CPU, &s.MemBytes, &s.Conns, &s.TxBytes, &s.RxBytes); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	// Caller expects chronological order (oldest first).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func insertSample(db *sql.DB, s Sample) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO metrics_snapshot(ts, cpu, mem, conns, tx_bytes, rx_bytes)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		s.TS, s.CPU, s.MemBytes, s.Conns, s.TxBytes, s.RxBytes,
	)
	return err
}

func trim(db *sql.DB, retention time.Duration) error {
	cutoff := time.Now().Add(-retention).UTC()
	_, err := db.Exec(`DELETE FROM metrics_snapshot WHERE ts < ?`, cutoff)
	return err
}

// --- /proc readers (Linux only, graceful on macOS) ---

type cpuSnapshot struct{ total, idle int64 }

func readCPU() (cpuSnapshot, error) {
	if runtime.GOOS != "linux" {
		return cpuSnapshot{}, errors.New("not linux")
	}
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSnapshot{}, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return cpuSnapshot{}, errors.New("short cpu line")
		}
		var total, idle int64
		for i, f := range fields[1:] {
			v, _ := strconv.ParseInt(f, 10, 64)
			total += v
			if i == 3 {
				idle = v
			}
		}
		return cpuSnapshot{total: total, idle: idle}, nil
	}
	return cpuSnapshot{}, errors.New("no cpu line")
}

type netSnapshot struct{ tx, rx uint64 }

func readNet(iface string) (netSnapshot, error) {
	if runtime.GOOS != "linux" {
		return netSnapshot{}, errors.New("not linux")
	}
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return netSnapshot{}, err
	}
	var totalRx, totalTx uint64
	for _, line := range strings.Split(string(raw), "\n") {
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if name == "lo" {
			continue
		}
		if iface != "" && name != iface {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 16 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		totalRx += rx
		totalTx += tx
	}
	return netSnapshot{tx: totalTx, rx: totalRx}, nil
}

func readMemBytes() (int64, error) {
	if runtime.GOOS != "linux" {
		return 0, errors.New("not linux")
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	// Report used memory = MemTotal - MemAvailable in bytes.
	var memTotal, memAvail int64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			memTotal = v * 1024
		case "MemAvailable:":
			memAvail = v * 1024
		}
	}
	if memTotal == 0 {
		return 0, errors.New("no MemTotal")
	}
	return memTotal - memAvail, nil
}

func readTCPConns() (int64, error) {
	if runtime.GOOS != "linux" {
		return 0, errors.New("not linux")
	}
	// Sum ESTABLISHED-state sockets from /proc/net/tcp + /proc/net/tcp6.
	// Older behavior counted every line in /proc/net/tcp — that swept
	// in TIME_WAIT, CLOSE_WAIT, FIN_WAIT_2 and every listening socket
	// on the host, producing "5058 live connections" on a box with a
	// couple hundred actual streams and thousands of teardown-tail
	// sockets. The Dashboard label is "实时数量" — only ESTABLISHED
	// carries that meaning.
	total := int64(0)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			// tcp6 may be missing on IPv6-disabled kernels; ignore.
			if path == "/proc/net/tcp" {
				return 0, err
			}
			continue
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if i == 0 || line == "" {
				continue
			}
			// Field 3 (0-indexed) is state as a two-hex-char string;
			// 01 = TCP_ESTABLISHED. Header line skipped above.
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			if fields[3] == "01" {
				total++
			}
		}
	}
	return total, nil
}
