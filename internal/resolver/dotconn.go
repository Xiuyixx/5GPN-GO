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
	pending  map[uint16]pendingQuery
	nextID   uint16
	closed   bool
	readerCh chan struct{} // closed when the current reader exits

	writeMu sync.Mutex // serializes framed writes on the wire
}

// pendingQuery binds a DNS ID to the connection generation on which it was
// written. IDs can be reused after reconnect, so an old reader must never
// consume or fail a newer connection's waiter merely because the numeric ID
// matches.
type pendingQuery struct {
	conn net.Conn
	ch   chan *dns.Msg
}

// ErrDoTQueryCapacity means every DNS message ID on one multiplexed DoT
// connection is already assigned to an in-flight query.
var ErrDoTQueryCapacity = errors.New("resolver: DoT query capacity exhausted")

func newDotConn(addr string, dial func(context.Context, string) (net.Conn, error), logger *slog.Logger) *dotConn {
	if logger == nil {
		logger = slog.Default()
	}
	return &dotConn{
		addr:    addr,
		dial:    dial,
		logger:  logger,
		pending: make(map[uint16]pendingQuery),
	}
}

// query pipelines msg on the shared connection and waits for the matching
// reply. On any I/O error the connection is dropped so the next call
// redials; the caller races another upstream, so we don't retry inline.
func (c *dotConn) query(ctx context.Context, msg *dns.Msg) (*dns.Msg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, id, ch, err := c.register(ctx)
	if err != nil {
		return nil, err
	}

	c.writeMu.Lock()
	msg.Id = id
	werr := writeFramed(conn, msg)
	c.writeMu.Unlock()
	if werr != nil {
		c.dropOnError(conn)
		return nil, fmt.Errorf("resolver: write %s: %w", c.addr, werr)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			return nil, fmt.Errorf("resolver: read %s: connection closed", c.addr)
		}
		return resp, nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// A connection that accepted a query but stayed silent until the
			// deadline is a black hole. Retire it so the next query redials.
			c.retire(conn)
		} else {
			c.forget(conn, id)
		}
		return nil, ctx.Err()
	}
}

// register ensures a live connection exists, allocates a unique DNS ID
// for this in-flight query, and returns the response channel. Serialized
// through c.mu so ID assignment and pending-map mutation are atomic.
func (c *dotConn) register(ctx context.Context) (net.Conn, uint16, chan *dns.Msg, error) {
	for {
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return nil, 0, nil, err
		}
		if c.closed {
			c.mu.Unlock()
			return nil, 0, nil, errors.New("resolver: upstream conn closed")
		}
		if c.conn != nil {
			// Assign an unused ID. In practice the map has O(concurrent
			// queries) entries, well under 65k, so a bounded linear probe is
			// fine; wrap-around is handled by nextID overflow.
			id, err := c.pickID()
			if err != nil {
				c.mu.Unlock()
				return nil, 0, nil, err
			}
			ch := make(chan *dns.Msg, 1)
			conn := c.conn
			c.pending[id] = pendingQuery{conn: conn, ch: ch}
			c.mu.Unlock()
			return conn, id, ch, nil
		}
		c.mu.Unlock()
		if err := c.reconnect(ctx); err != nil {
			return nil, 0, nil, err
		}
		// The connection can die between reconnect returning and this
		// goroutine reacquiring c.mu. Loop until a live generation is pinned.
	}
}

func (c *dotConn) pickID() (uint16, error) {
	for i := 0; i < 0x10000; i++ {
		c.nextID++
		id := c.nextID
		if _, exists := c.pending[id]; !exists {
			return id, nil
		}
	}
	return 0, ErrDoTQueryCapacity
}

func (c *dotConn) forget(conn net.Conn, id uint16) {
	c.mu.Lock()
	if pending, ok := c.pending[id]; ok && pending.conn == conn {
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// dropOnError tears down conn if it is still the active one, so
// subsequent queries redial rather than piping into a broken pipe.
func (c *dotConn) dropOnError(conn net.Conn) {
	c.retire(conn)
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
	if err := ctx.Err(); err != nil {
		_ = newConn.Close()
		return err
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
		pending, ok := c.pending[msg.Id]
		if ok && pending.conn == conn {
			delete(c.pending, msg.Id)
		} else {
			ok = false
		}
		c.mu.Unlock()
		if !ok {
			// Stray reply (client cancelled or ID collision). Drop.
			continue
		}
		select {
		case pending.ch <- msg:
		default:
		}
	}
}

// terminate retires conn and unblocks only its own waiters with nil. Called
// from the reader goroutine when it hits EOF or a network error.
func (c *dotConn) terminate(conn net.Conn, err error) {
	c.retire(conn)
	// Non-EOF errors during shutdown are logged at debug so scanners
	// don't spam operators.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		c.logger.Debug("dot conn reader exit", "addr", c.addr, "err", err)
	}
}

// retire closes one connection generation and fails only the queries that
// were registered on that generation. A stale reader may call this after a
// replacement connection is already active; the replacement and its pending
// queries are left untouched.
func (c *dotConn) retire(conn net.Conn) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	failed := make([]chan *dns.Msg, 0)
	for id, pending := range c.pending {
		if pending.conn == conn {
			delete(c.pending, id)
			failed = append(failed, pending.ch)
		}
	}
	c.mu.Unlock()
	_ = conn.Close()
	for _, ch := range failed {
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
	c.pending = make(map[uint16]pendingQuery)
	readerDone := c.readerCh
	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	for _, pending := range pending {
		select {
		case pending.ch <- nil:
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
