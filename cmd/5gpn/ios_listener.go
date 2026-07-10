package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/activation"

	"github.com/Xiuyixx/5GPN-Go/internal/ios"
)

// runIOSListener serves the inetd-style iOS profile server. When run under
// systemd with a matching .socket unit (LISTEN_FDS set) the pre-opened
// listeners are used directly; otherwise it opens a fresh listener on the
// configured HTTP port. Graceful shutdown closes the listener on ctx.Done()
// and waits up to 2s for in-flight conns to finish.
func runIOSListener(ctx context.Context, port int, wwwDir string, logger *slog.Logger) error {
	listeners, err := activation.Listeners()
	if err != nil {
		logger.Warn("ios: activation.Listeners failed", "err", err)
		listeners = nil
	}

	if len(listeners) == 0 {
		if port <= 0 {
			return nil
		}
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return fmt.Errorf("ios: listen :%d: %w", port, err)
		}
		listeners = []net.Listener{l}
		logger.Info("ios: listening", "port", port, "www", wwwDir)
	} else {
		logger.Info("ios: using systemd socket activation", "listeners", len(listeners), "www", wwwDir)
	}

	routes := ios.DefaultRoutes()
	var wg sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		for _, l := range listeners {
			_ = l.Close()
		}
		close(stopped)
	}()

	for _, l := range listeners {
		wg.Add(1)
		go func(l net.Listener) {
			defer wg.Done()
			for {
				conn, err := l.Accept()
				if err != nil {
					if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
						return
					}
					logger.Warn("ios: accept", "err", err)
					continue
				}
				wg.Add(1)
				go func(c net.Conn) {
					defer wg.Done()
					defer c.Close()
					if err := ios.ServeConn(c, wwwDir, routes, 10*time.Second); err != nil {
						logger.Debug("ios: serve", "err", err)
					}
				}(conn)
			}
		}(l)
	}

	<-stopped
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		logger.Warn("ios: shutdown timeout — in-flight conns dropped")
	}
	return nil
}
