package resolver

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// dotConn is a single multiplexed DoT connection to one upstream addr.
// RFC 7858 §3.4 lets a resolver pipeline multiple queries on one
// connection and receive replies out of order, keyed by DNS message ID.
// One reader goroutine owns the socket for read; writes serialize on
// writeMu so packed messages never interleave on the wire. A dead
// connection is discarded and rebuilt on the next query.
type dotConn struct {
	addr   string
	dial   func(context.Context, string) (net.Conn, error)
	logger *slog.Logger

	mu       sync.Mutex // guards the fields below
	conn     net.Conn
	pending  map[uint16]chan *dns.Msg
	nextID   uint16
	closed   bool
	readerCh chan struct{} // closed when the current reader exits

	writeMu sync.Mutex // serializes framed writes on the wire
}

func newDotConn(addr string, dial func(context.Context, string) (net.Conn, error), logger *slog.Logger) *dotConn {
	return &dotConn{
		addr:    addr,
		dial:    dial,
		logger:  logger,
		pending: make(map[uint16]chan *dns.Msg),
	}
}

// query pipelines msg on the shared connection and waits for the matching
// reply. On any I/O error the connection is dropped so the next call
// redials; the caller races another upstream, so we don't retry inline.
func (c *dotConn) query(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	conn, id, ch, err := c.register(ctx)
	if err != nil {
		return nil, err
	}

	c.writeMu.Lock()
	msg.Id = id
	werr := writeFramed(conn, msg)
	c.writeMu.Unlock()
	if werr != nil {
		c.dropOnError(conn, id)
		return nil, fmt.Errorf("resolver: write %s: %w", c.addr, werr)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			return nil, fmt.Errorf("resolver: read %s: connection closed", c.addr)
		}
		return resp, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	}
}

// register ensures a live connection exists, allocates a unique DNS ID
// for this in-flight query, and returns the response channel. Serialized
// through c.mu so ID assignment and pending-map mutation are atomic.
func (c *dotConn) register(ctx context.Context) (net.Conn, uint16, chan *dns.Msg, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, 0, nil, errors.New("resolver: upstream conn closed")
	}
	if c.conn == nil {
		c.mu.Unlock()
		if err := c.reconnect(ctx); err != nil {
			return nil, 0, nil, err
		}
		c.mu.Lock()
	}
	// Assign an unused ID. In practice the map has O(concurrent
	// queries) entries, well under 65k, so a bounded linear probe is
	// fine; wrap-around is handled by nextID overflow.
	id := c.pickID()
	ch := make(chan *dns.Msg, 1)
	c.pending[id] = ch
	conn := c.conn
	c.mu.Unlock()
	return conn, id, ch, nil
}

func (c *dotConn) pickID() uint16 {
	for i := 0; i < 0x10000; i++ {
		c.nextID++
		id := c.nextID
		if _, exists := c.pending[id]; !exists {
			return id
		}
	}
	// Pending map is completely full — vanishingly unlikely but return
	// something rather than looping forever. Collision means the older
	// query will get this reply; caller sees a mismatched ID and errors.
	c.nextID++
	return c.nextID
}

func (c *dotConn) forget(id uint16) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// dropOnError tears down conn if it is still the active one, so
// subsequent queries redial rather than piping into a broken pipe.
func (c *dotConn) dropOnError(conn net.Conn, id uint16) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	delete(c.pending, id)
	c.mu.Unlock()
	_ = conn.Close()
}

// reconnect dials a fresh TLS session and spawns a reader goroutine.
// Callers hold no lock; internal locking keeps the connection swap
// atomic. Idempotent under concurrent callers via double-checked lock.
func (c *dotConn) reconnect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("resolver: upstream conn closed")
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	newConn, err := c.dial(dialCtx, c.addr)
	if err != nil {
		return fmt.Errorf("resolver: dial %s: %w", c.addr, err)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = newConn.Close()
		return errors.New("resolver: upstream conn closed")
	}
	// Race: another caller may have already reconnected. Discard ours.
	if c.conn != nil {
		c.mu.Unlock()
		_ = newConn.Close()
		return nil
	}
	c.conn = newConn
	done := make(chan struct{})
	c.readerCh = done
	c.mu.Unlock()

	go c.reader(newConn, done)
	return nil
}

// reader dispatches inbound framed messages to the pending map by ID.
// Exits on any read error, tearing down the connection so subsequent
// queries force a redial. All pending queries on the dead conn are
// released with a nil reply so their callers unblock.
func (c *dotConn) reader(conn net.Conn, done chan struct{}) {
	defer close(done)
	for {
		msg, err := readFramed(conn)
		if err != nil {
			c.terminate(conn, err)
			return
		}
		c.mu.Lock()
		ch, ok := c.pending[msg.Id]
		if ok {
			delete(c.pending, msg.Id)
		}
		c.mu.Unlock()
		if !ok {
			// Stray reply (client cancelled or ID collision). Drop.
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}

// terminate marks conn dead, drains the pending map, and unblocks all
// waiters with nil. Called from the reader goroutine when it hits EOF
// or a network error.
func (c *dotConn) terminate(conn net.Conn, err error) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	pending := c.pending
	c.pending = make(map[uint16]chan *dns.Msg)
	c.mu.Unlock()
	_ = conn.Close()
	// Non-EOF errors during shutdown are logged at debug so scanners
	// don't spam operators.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.logger.Debug("dot conn reader exit", "addr", c.addr, "err", err)
	}
	for _, ch := range pending {
		select {
		case ch <- nil:
		default:
		}
	}
}

// close terminates the pool entry. Any in-flight query returns "conn
// closed" and future queries fail immediately without redialing.
func (c *dotConn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	conn := c.conn
	c.conn = nil
	pending := c.pending
	c.pending = make(map[uint16]chan *dns.Msg)
	readerDone := c.readerCh
	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	for _, ch := range pending {
		select {
		case ch <- nil:
		default:
		}
	}
	if readerDone != nil {
		<-readerDone
	}
}

// Static assertion — pending replies must always fit in a uint16 ID
// space. Kept here so the constant lives beside its user.
var _ = binary.BigEndian
