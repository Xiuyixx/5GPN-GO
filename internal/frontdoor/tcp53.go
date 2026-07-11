package frontdoor

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/miekg/dns"
)

// tcpServer mirrors udpServer for the :53 TCP fallback listener (large
// responses, truncated-UDP retries). Same eager-bind-in-listen() shape,
// for the same reasons: bind failures and the OS-assigned ephemeral
// port are both known before serve() ever runs.
type tcpServer struct {
	fd   *Frontdoor
	addr string

	mu     sync.Mutex
	ln     net.Listener
	server *dns.Server
}

func newTCPServer(fd *Frontdoor, addr string) *tcpServer {
	return &tcpServer{fd: fd, addr: addr}
}

// listen binds the TCP listener. It must be called before serve().
func (s *tcpServer) listen(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("frontdoor: tcp listen %s: %w", s.addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.server = &dns.Server{Listener: ln, Handler: dns.HandlerFunc(s.fd.ServeDNS)}
	s.mu.Unlock()
	return nil
}

// serve blocks, answering queries, until Shutdown is called or the
// underlying listener fails.
func (s *tcpServer) serve() error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return fmt.Errorf("frontdoor: tcp server %s: listen was never called", s.addr)
	}
	return server.ActivateAndServe()
}

// Shutdown gracefully stops the listener; a no-op if listen() was
// never called.
func (s *tcpServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.ShutdownContext(ctx)
}

// LocalAddr returns the bound socket's address — used by tests to
// discover the OS-assigned ephemeral port when addr ends in ":0".
func (s *tcpServer) LocalAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}
