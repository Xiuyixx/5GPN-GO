//go:build e2e

// Harness for the M4 e2e smoke suite. Every file in this directory is
// gated by the `e2e` build tag so `go test ./...` on a dev machine does
// nothing here; CI runs `go test -tags e2e ./tests/e2e/...`.

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// daemon captures a running 5gpn instance.
type daemon struct {
	cmd        *exec.Cmd
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	addr       string // 127.0.0.1:<port>
	setupToken string
}

// URL builds an http:// URL under the running daemon.
func (d *daemon) URL(path string) string {
	return "http://" + d.addr + path
}

// Stop terminates the daemon and reports its output on failure.
func (d *daemon) Stop() {
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Signal(os.Interrupt)
		_ = d.cmd.Wait()
	}
}

// startDaemon builds (or reuses) the 5gpn binary and starts it under a
// fresh state dir on a random port. Fatal on any startup issue.
func startDaemon(t *testing.T) *daemon {
	t.Helper()

	bin := os.Getenv("E2E_BINARY")
	if bin == "" {
		bin = filepath.Join("..", "..", "dist", "5gpn")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("daemon binary missing at %s (set E2E_BINARY or run `make build` first): %v", bin, err)
	}

	stateDir := t.TempDir()
	// Reuse configs/example.yaml verbatim: it is the schema-authoritative
	// sample and passes validate. Panel is bound via --listen below, so
	// the port literal in the file is irrelevant to the e2e run.
	srcCfg, err := os.ReadFile(filepath.Join("..", "..", "configs", "example.yaml"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	cfgPath := filepath.Join(stateDir, "config.yaml")
	if err := os.WriteFile(cfgPath, srcCfg, 0o600); err != nil {
		t.Fatal(err)
	}

	// Pick a free port ourselves so we can predict where to probe.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin,
		"--config", cfgPath,
		"--data", stateDir,
		"--listen", addr,
		"--insecure",
	)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	d := &daemon{cmd: cmd, stdout: stdout, stderr: stderr, addr: addr}
	t.Cleanup(func() {
		d.Stop()
		if t.Failed() {
			t.Logf("--- daemon stderr ---\n%s", stderr.String())
			t.Logf("--- daemon stdout ---\n%s", stdout.String())
		}
	})

	if err := waitReady(d.URL("/api/v1/bootstrap"), 10*time.Second); err != nil {
		t.Fatalf("daemon did not become ready: %v\n---stderr---\n%s", err, stderr.String())
	}
	d.setupToken = extractSetupToken(stdout.String())
	return d
}

var setupTokenRE = regexp.MustCompile(`5GPN SETUP TOKEN[^\n]*\n\n\s+([0-9a-f]{64})`)

func extractSetupToken(out string) string {
	m := setupTokenRE.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
}
