package metrics

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	xdb "github.com/Xiuyixx/5GPN-Go/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.db")
	h, err := xdb.Open(xdb.Config{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := xdb.Migrate(h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInsertAndListRecent(t *testing.T) {
	h := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		if err := insertSample(h, Sample{
			TS:       now.Add(time.Duration(i) * time.Second),
			CPU:      float64(10 + i),
			MemBytes: int64(1_000_000 * (i + 1)),
			Conns:    int64(50 + i),
			TxBytes:  int64(100 * (i + 1)),
			RxBytes:  int64(200 * (i + 1)),
		}); err != nil {
			t.Fatalf("insert #%d: %v", i, err)
		}
	}
	out, err := ListRecent(h, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 5 {
		t.Fatalf("want 5 samples, got %d", len(out))
	}
	// Order must be oldest → newest.
	for i := 1; i < len(out); i++ {
		if !out[i-1].TS.Before(out[i].TS) {
			t.Fatalf("samples not sorted asc: %v then %v", out[i-1].TS, out[i].TS)
		}
	}
	if out[len(out)-1].CPU != 14 {
		t.Errorf("last CPU should be 14, got %v", out[len(out)-1].CPU)
	}
}

func TestTrimRespectsRetention(t *testing.T) {
	h := openTestDB(t)
	// Insert one 2h-old sample and one fresh one.
	old := time.Now().Add(-2 * time.Hour).UTC()
	fresh := time.Now().UTC()
	_ = insertSample(h, Sample{TS: old, CPU: 5})
	_ = insertSample(h, Sample{TS: fresh, CPU: 25})

	if err := trim(h, time.Hour); err != nil {
		t.Fatal(err)
	}
	out, err := ListRecent(h, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].CPU != 25 {
		t.Fatalf("expected only fresh sample, got %+v", out)
	}
}

func TestSampleWorksOffLinux(t *testing.T) {
	c := New(Config{})
	s := c.Sample()
	// TS must be recent, other fields are host-dependent so we just check
	// they don't blow up.
	if time.Since(s.TS) > time.Minute {
		t.Errorf("Sample TS too old: %v", s.TS)
	}
}
