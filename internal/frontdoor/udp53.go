package frontdoor

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/miekg/dns"
)

// udpServer wraps a single miekg/dns UDP listener bound to addr. The
// socket is opened eagerly in listen() — before serve() is ever
// called — so bind failures (port in use, permission denied) and the
// OS-assigned port (when addr ends in ":0", as frontdoor_test.go uses
// for isolation) are both known immediately, rather than surfacing
// only once serve() runs in its own goroutine.
type udpServer struct {
	fd   *Frontdoor
	addr string

	mu     sync.Mutex
	conn   net.PacketConn
	server *dns.Server
}

func newUDPServer(fd *Frontdoor, addr string) *udpServer {
	return &udpServer{fd: fd, addr: addr}
}

// listen binds the UDP socket. It must be called before serve().
func (s *udpServer) listen(ctx context.Context) error {
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp", s.addr)
	if err != nil {
		return fmt.Errorf("frontdoor: udp listen %s: %w", s.addr, err)
	}
	s.mu.Lock()
	s.conn = conn
	s.server = &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(s.fd.ServeDNS)}
	s.mu.Unlock()
	return nil
}

// serve blocks, answering queries, until Shutdown is called or the
// underlying connection fails.
func (s *udpServer) serve() error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return fmt.Errorf("frontdoor: udp server %s: listen was never called", s.addr)
	}
	return server.ActivateAndServe()
}

// Shutdown gracefully stops the listener; a no-op if listen() was
// never called.
func (s *udpServer) Shutdown(ctx context.Context) error {
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
func (s *udpServer) LocalAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}
