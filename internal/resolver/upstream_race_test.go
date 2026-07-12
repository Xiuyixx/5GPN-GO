package resolver

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// upstreamHarness builds an Upstream whose TLSDial hands out net.Pipe
// connections whose server sides are driven by per-address behaviors.
// Each behavior is a function; call count and total dials are exposed
// as atomic counters so tests can assert without racing.
type upstreamHarness struct {
	t         *testing.T
	up        *Upstream
	dialCount atomic.Int64
	perAddr   map[string]func(ctx context.Context, server net.Conn)
}

func newUpstreamHarness(t *testing.T) *upstreamHarness {
	t.Helper()
	h := &upstreamHarness{
		t:       t,
		perAddr: make(map[string]func(context.Context, net.Conn)),
	}
	h.up = NewUpstream()
	h.up.CN = nil
	h.up.Proxy = nil
	h.up.Fallback = nil
	h.up.Timeout = 3 * time.Second
	h.up.FallbackDelay = 200 * time.Millisecond
	// A dial-scoped ctx is unsafe to pass to the behavior goroutine
	// because reconnect() cancels its dial ctx as soon as the dial
	// returns (mirroring how real TLS dialers work — the conn survives
	// dial-ctx cancellation). We give each behavior an independent
	// lifetime tied to the test cleanup instead.
	behaviorCtx, cancelBehaviors := context.WithCancel(context.Background())
	t.Cleanup(cancelBehaviors)
	h.up.TLSDial = func(_ context.Context, addr string) (net.Conn, error) {
		h.dialCount.Add(1)
		behavior, ok := h.perAddr[addr]
		if !ok {
			return nil, errors.New("no behavior registered for " + addr)
		}
		client, server := net.Pipe()
		go behavior(behaviorCtx, server)
		return client, nil
	}
	t.Cleanup(h.up.Close)
	return h
}

// answerWith replies to every framed query with rcode+answer after delay.
func (h *upstreamHarness) answerWith(delay time.Duration, rcode int, a net.IP) func(context.Context, net.Conn) {
	return func(ctx context.Context, server net.Conn) {
		defer func() { _ = server.Close() }()
		for {
			var hdr [2]byte
			if _, err := io.ReadFull(server, hdr[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint16(hdr[:])
			buf := make([]byte, n)
			if _, err := io.ReadFull(server, buf); err != nil {
				return
			}
			q := new(dns.Msg)
			if err := q.Unpack(buf); err != nil {
				return
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			m := new(dns.Msg)
			m.SetReply(q)
			m.Rcode = rcode
			if rcode == dns.RcodeSuccess && a != nil {
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   a,
				})
			}
			packed, _ := m.Pack()
			var out [2]byte
			binary.BigEndian.PutUint16(out[:], uint16(len(packed)))
			_, _ = server.Write(out[:])
			_, _ = server.Write(packed)
		}
	}
}

// blackhole reads the query and never responds. Server closes on ctx.Done.
func (h *upstreamHarness) blackhole() func(context.Context, net.Conn) {
	return func(ctx context.Context, server net.Conn) {
		defer func() { _ = server.Close() }()
		go func() {
			<-ctx.Done()
			_ = server.Close()
		}()
		// Keep reading so the client's Write doesn't block on a full
		// pipe buffer.
		buf := make([]byte, 4096)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}
}

// TestUpstreamRace_FastPrimaryWins — with two primaries at different
// speeds, the fast one's reply must return quickly and TierPrimary is
// reported. No fallback dial happens.
func TestUpstreamRace_FastPrimaryWins(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"fast.cn:853", "slow.cn:853"}
	h.up.Proxy = []string{"fb.proxy:853"}
	h.perAddr["fast.cn:853"] = h.answerWith(10*time.Millisecond, dns.RcodeSuccess, net.ParseIP("1.2.3.4"))
	h.perAddr["slow.cn:853"] = h.answerWith(400*time.Millisecond, dns.RcodeSuccess, net.ParseIP("9.9.9.9"))
	h.perAddr["fb.proxy:853"] = h.answerWith(5*time.Millisecond, dns.RcodeSuccess, net.ParseIP("8.8.8.8"))

	start := time.Now()
	resp, tier, err := h.up.Query(context.Background(), makeQuery("example.com", dns.TypeA), "direct")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if tier != TierPrimary {
		t.Fatalf("tier = %v, want TierPrimary", tier)
	}
	if elapsed := time.Since(start); elapsed > 180*time.Millisecond {
		t.Fatalf("Query took %s — fallback fired unnecessarily", elapsed)
	}
	if a, ok := resp.Answer[0].(*dns.A); !ok || !a.A.Equal(net.ParseIP("1.2.3.4")) {
		t.Fatalf("Answer = %+v, want fast primary IP", resp.Answer)
	}
}

// TestUpstreamRace_FallbackWinsWhenPrimaryBlackholed — both primaries
// silently drop packets; fallback answers after FallbackDelay elapses.
// tier must be TierFallback so metrics can flag the degradation.
// Fallback is an EXPLICIT pool (not auto-cross-category) — safer default
// since cross-category fallback would leak GFW-polluted answers.
func TestUpstreamRace_FallbackWinsWhenPrimaryBlackholed(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"dead1.cn:853", "dead2.cn:853"}
	h.up.Fallback = []string{"live.fb:853"}
	h.up.FallbackDelay = 100 * time.Millisecond
	h.perAddr["dead1.cn:853"] = h.blackhole()
	h.perAddr["dead2.cn:853"] = h.blackhole()
	h.perAddr["live.fb:853"] = h.answerWith(10*time.Millisecond, dns.RcodeSuccess, net.ParseIP("8.8.8.8"))

	start := time.Now()
	resp, tier, err := h.up.Query(context.Background(), makeQuery("example.com", dns.TypeA), "direct")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if tier != TierFallback {
		t.Fatalf("tier = %v, want TierFallback", tier)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Fatalf("Query elapsed = %s, want ~FallbackDelay+answer", elapsed)
	}
	if a, ok := resp.Answer[0].(*dns.A); !ok || !a.A.Equal(net.ParseIP("8.8.8.8")) {
		t.Fatalf("Answer = %+v, want fallback IP", resp.Answer)
	}
}

// TestUpstreamRace_ServfailNotAccepted — a primary that returns
// SERVFAIL must not short-circuit the race; the healthy fallback wins.
// Fallback is explicit here (auto cross-category was removed).
func TestUpstreamRace_ServfailNotAccepted(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"bad.cn:853"}
	h.up.Fallback = []string{"good.fb:853"}
	h.up.FallbackDelay = 50 * time.Millisecond
	h.perAddr["bad.cn:853"] = h.answerWith(5*time.Millisecond, dns.RcodeServerFailure, nil)
	h.perAddr["good.fb:853"] = h.answerWith(20*time.Millisecond, dns.RcodeSuccess, net.ParseIP("8.8.8.8"))

	resp, tier, err := h.up.Query(context.Background(), makeQuery("example.com", dns.TypeA), "direct")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if tier != TierFallback {
		t.Fatalf("tier = %v, want TierFallback (primary SERVFAIL must be skipped)", tier)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %s, want NOERROR", dns.RcodeToString[resp.Rcode])
	}
}

// TestUpstreamRace_NXDOMAINIsAuthoritative — NXDOMAIN is a valid final
// answer; the race must not wait for fallback to overrule it.
func TestUpstreamRace_NXDOMAINIsAuthoritative(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"a.cn:853"}
	h.up.Proxy = []string{"b.proxy:853"}
	h.up.FallbackDelay = 300 * time.Millisecond
	h.perAddr["a.cn:853"] = h.answerWith(5*time.Millisecond, dns.RcodeNameError, nil)
	// Fallback would answer SUCCESS if it ever ran — assertion catches
	// an implementation that treats NXDOMAIN as a failure.
	h.perAddr["b.proxy:853"] = h.answerWith(5*time.Millisecond, dns.RcodeSuccess, net.ParseIP("1.2.3.4"))

	start := time.Now()
	resp, tier, err := h.up.Query(context.Background(), makeQuery("nope.example.com", dns.TypeA), "direct")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if tier != TierPrimary || resp.Rcode != dns.RcodeNameError {
		t.Fatalf("got tier=%v rcode=%s, want TierPrimary NXDOMAIN", tier, dns.RcodeToString[resp.Rcode])
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Query elapsed = %s, want short-circuit before FallbackDelay", elapsed)
	}
}

// TestUpstreamRace_AllFail — every upstream blackholes; Query returns
// the context-deadline error at Timeout rather than hanging.
func TestUpstreamRace_AllFail(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"x.cn:853"}
	h.up.Proxy = []string{"y.proxy:853"}
	h.up.Timeout = 200 * time.Millisecond
	h.up.FallbackDelay = 50 * time.Millisecond
	h.perAddr["x.cn:853"] = h.blackhole()
	h.perAddr["y.proxy:853"] = h.blackhole()

	start := time.Now()
	_, _, err := h.up.Query(context.Background(), makeQuery("example.com", dns.TypeA), "direct")
	if err == nil {
		t.Fatal("Query returned nil error despite all upstreams dead")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("Query elapsed = %s, want ~Timeout", elapsed)
	}
}

func TestUpstreamRace_TimeoutRedialsOnImmediateRetry(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"blackhole.cn:853"}
	h.up.Timeout = 30 * time.Millisecond

	var generations atomic.Int64
	h.perAddr["blackhole.cn:853"] = func(ctx context.Context, server net.Conn) {
		defer func() { _ = server.Close() }()
		generation := generations.Add(1)
		msg, err := readFramed(server)
		if err != nil {
			return
		}
		if generation == 1 {
			// Keep the first connection silent until the client retires it.
			buf := make([]byte, 1)
			_, _ = server.Read(buf)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(msg)
		_ = writeFramed(server, resp)
	}

	if _, _, err := h.up.Query(context.Background(), makeQuery("first.example", dns.TypeA), "direct"); err == nil {
		t.Fatal("blackholed first query returned nil error")
	}
	h.up.Timeout = time.Second
	if _, _, err := h.up.Query(context.Background(), makeQuery("second.example", dns.TypeA), "direct"); err != nil {
		t.Fatalf("immediate retry after blackhole: %v", err)
	}
	if got := h.dialCount.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
}

func TestUpstreamRace_TimeoutDoesNotRetireReplacementGeneration(t *testing.T) {
	h := newUpstreamHarness(t)
	const (
		target = "target.cn:853"
		other  = "other.cn:853"
	)
	h.up.CN = []string{target, other}
	h.up.Timeout = 180 * time.Millisecond
	h.up.FallbackDelay = time.Second

	firstRead := make(chan struct{})
	firstClosed := make(chan struct{})
	secondRead := make(chan struct{})
	releaseSecond := make(chan struct{})
	var targetGeneration atomic.Int64
	h.perAddr[target] = func(ctx context.Context, server net.Conn) {
		defer func() { _ = server.Close() }()
		switch targetGeneration.Add(1) {
		case 1:
			if _, err := readFramed(server); err != nil {
				return
			}
			close(firstRead)
			for {
				if _, err := readFramed(server); err != nil {
					close(firstClosed)
					return
				}
			}
		case 2:
			msg, err := readFramed(server)
			if err != nil {
				return
			}
			close(secondRead)
			select {
			case <-releaseSecond:
			case <-ctx.Done():
				return
			}
			resp := new(dns.Msg)
			resp.SetReply(msg)
			_ = writeFramed(server, resp)
		}
	}
	h.perAddr[other] = h.blackhole()

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := h.up.Query(context.Background(), makeQuery("first.example", dns.TypeA), "direct")
		firstDone <- err
	}()
	waitSignal(t, firstRead, "first target query")

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, shortErr := h.up.queryOne(shortCtx, target, makeQuery("retire.example", dns.TypeA))
	shortCancel()
	if !errors.Is(shortErr, context.DeadlineExceeded) {
		t.Fatalf("short query error = %v, want deadline exceeded", shortErr)
	}
	waitSignal(t, firstClosed, "first target generation retirement")

	type queryResult struct {
		msg *dns.Msg
		err error
	}
	secondDone := make(chan queryResult, 1)
	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	go func() {
		msg, err := h.up.queryOne(secondCtx, target, makeQuery("second.example", dns.TypeA))
		secondDone <- queryResult{msg: msg, err: err}
	}()
	waitSignal(t, secondRead, "replacement target query")

	select {
	case err := <-firstDone:
		if err == nil {
			t.Fatal("timed-out multi-upstream query returned nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out multi-upstream query did not return")
	}
	close(releaseSecond)

	select {
	case got := <-secondDone:
		if got.err != nil {
			t.Fatalf("replacement generation was retired by older timeout: %v", got.err)
		}
		if got.msg == nil || got.msg.Rcode != dns.RcodeSuccess {
			t.Fatalf("replacement response = %+v", got.msg)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement generation query did not return")
	}
}

// TestUpstreamRace_PipelinedQueriesShareConn — five concurrent queries
// to the same addr must reuse the single multiplexed dotConn (dialCount
// stays at 1 across the burst). RFC 7858 pipelining lets us amortize
// the TLS handshake.
func TestUpstreamRace_PipelinedQueriesShareConn(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"only.cn:853"}
	h.up.Proxy = []string{"unused.proxy:853"}
	h.up.FallbackDelay = 500 * time.Millisecond
	h.perAddr["only.cn:853"] = h.answerWith(10*time.Millisecond, dns.RcodeSuccess, net.ParseIP("1.2.3.4"))
	h.perAddr["unused.proxy:853"] = h.blackhole()

	// Warm the conn with one query so the reader is up.
	if _, _, err := h.up.Query(context.Background(), makeQuery("warm.example.com", dns.TypeA), "direct"); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	baseDials := h.dialCount.Load()

	var wg sync.WaitGroup
	const n = 5
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := h.up.Query(context.Background(), makeQuery("q.example.com", dns.TypeA), "direct")
			errs <- err
			_ = i
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent Query: %v", e)
		}
	}
	if delta := h.dialCount.Load() - baseDials; delta != 0 {
		t.Fatalf("dialCount grew by %d during pipelined burst, want 0 (conn reused)", delta)
	}
}

// TestUpstreamRace_ReconnectsAfterDrop — after the reader observes a
// closed conn, the next query must redial and succeed.
func TestUpstreamRace_ReconnectsAfterDrop(t *testing.T) {
	h := newUpstreamHarness(t)
	h.up.CN = []string{"flap.cn:853"}
	h.up.Proxy = []string{"fb.proxy:853"}
	h.up.FallbackDelay = 500 * time.Millisecond

	answered := atomic.Int64{}
	h.perAddr["flap.cn:853"] = func(ctx context.Context, server net.Conn) {
		defer func() { _ = server.Close() }()
		// Answer the first query, then close to simulate a drop.
		var hdr [2]byte
		if _, err := io.ReadFull(server, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint16(hdr[:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(server, buf); err != nil {
			return
		}
		q := new(dns.Msg)
		_ = q.Unpack(buf)
		m := new(dns.Msg)
		m.SetReply(q)
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("1.2.3.4"),
		})
		packed, _ := m.Pack()
		var out [2]byte
		binary.BigEndian.PutUint16(out[:], uint16(len(packed)))
		// Count the response attempt before writing. net.Pipe can wake the
		// client reader before the server goroutine resumes after Write, so
		// incrementing afterwards makes the assertion scheduler-dependent.
		answered.Add(1)
		_, _ = server.Write(out[:])
		_, _ = server.Write(packed)
	}
	h.perAddr["fb.proxy:853"] = h.blackhole()

	if _, _, err := h.up.Query(context.Background(), makeQuery("one.example.com", dns.TypeA), "direct"); err != nil {
		t.Fatalf("first Query: %v", err)
	}
	// Give the reader a moment to observe the server's EOF.
	time.Sleep(50 * time.Millisecond)

	if _, _, err := h.up.Query(context.Background(), makeQuery("two.example.com", dns.TypeA), "direct"); err != nil {
		t.Fatalf("second Query after drop: %v", err)
	}
	if got := answered.Load(); got != 2 {
		t.Fatalf("answered = %d, want 2 (reconnect happened)", got)
	}
	if h.dialCount.Load() < 2 {
		t.Fatalf("dialCount = %d, want >=2 (redial occurred)", h.dialCount.Load())
	}
}

func TestDotConn_StaleReaderOnlyFailsItsGeneration(t *testing.T) {
	oldClient, oldServer := net.Pipe()
	newClient, newServer := net.Pipe()
	defer func() { _ = oldServer.Close() }()
	defer func() { _ = newServer.Close() }()

	oldWaiter := make(chan *dns.Msg, 1)
	newWaiter := make(chan *dns.Msg, 1)
	c := newDotConn("upstream:853", nil, nil)
	c.conn = newClient
	c.pending[1] = pendingQuery{conn: oldClient, ch: oldWaiter}
	c.pending[2] = pendingQuery{conn: newClient, ch: newWaiter}
	t.Cleanup(c.close)

	c.terminate(oldClient, io.EOF)

	select {
	case got := <-oldWaiter:
		if got != nil {
			t.Fatalf("old generation waiter received %v, want nil", got)
		}
	default:
		t.Fatal("old generation waiter was not released")
	}
	select {
	case <-newWaiter:
		t.Fatal("stale reader released a waiter owned by the new connection")
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != newClient {
		t.Fatal("stale reader cleared the active replacement connection")
	}
	if pending, ok := c.pending[2]; !ok || pending.conn != newClient {
		t.Fatal("new connection's pending query was removed")
	}
}

func TestDotConn_IDCapacityDoesNotOverwritePendingQuery(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	c := newDotConn("full:853", nil, nil)
	c.conn = client
	sharedWaiter := make(chan *dns.Msg, 1)
	for i := 0; i < 1<<16; i++ {
		c.pending[uint16(i)] = pendingQuery{conn: client, ch: sharedWaiter}
	}
	defer c.close()

	_, _, _, err := c.register(context.Background())
	if !errors.Is(err, ErrDoTQueryCapacity) {
		t.Fatalf("register error = %v, want ErrDoTQueryCapacity", err)
	}
	if got := len(c.pending); got != 1<<16 {
		t.Fatalf("pending size = %d, want 65536", got)
	}
	if pending := c.pending[c.nextID]; pending.conn != client || pending.ch != sharedWaiter {
		t.Fatal("capacity exhaustion overwrote an existing pending query")
	}
}

func TestDotConn_DeadlineRetiresBlackholedConnection(t *testing.T) {
	var dials atomic.Int64
	dial := func(context.Context, string) (net.Conn, error) {
		client, server := net.Pipe()
		if dials.Add(1) == 1 {
			go func() {
				defer func() { _ = server.Close() }()
				_, _ = io.Copy(io.Discard, server)
			}()
			return client, nil
		}
		go func() {
			defer func() { _ = server.Close() }()
			msg, err := readFramed(server)
			if err != nil {
				return
			}
			resp := new(dns.Msg)
			resp.SetReply(msg)
			_ = writeFramed(server, resp)
		}()
		return client, nil
	}

	c := newDotConn("blackhole:853", dial, nil)
	t.Cleanup(c.close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.query(ctx, makeQuery("first.example", dns.TypeA)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first query error = %v, want deadline exceeded", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := c.query(ctx2, makeQuery("second.example", dns.TypeA)); err != nil {
		t.Fatalf("second query after blackhole: %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2 after blackholed connection retirement", got)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
