package tgbot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBot records the ctx it received in Serve so tests can observe the
// lifetime that Manager gave it. Blocks until that ctx is Done.
type fakeBot struct {
	served   atomic.Bool
	serveCtx atomic.Pointer[context.Context]
	exited   atomic.Bool
	exitedCh chan struct{}
	serveErr error
}

func newFakeBot() *fakeBot {
	return &fakeBot{exitedCh: make(chan struct{})}
}

func (f *fakeBot) Serve(ctx context.Context) error {
	f.served.Store(true)
	c := ctx
	f.serveCtx.Store(&c)
	<-ctx.Done()
	f.exited.Store(true)
	close(f.exitedCh)
	return f.serveErr
}

// installFakeBotFactory replaces the package-level newBotFn with a stub
// that hands out a shared fakeBot. Returns the bot + a cleanup func.
func installFakeBotFactory(t *testing.T) *fakeBot {
	t.Helper()
	fb := newFakeBot()
	prev := newBotFn
	newBotFn = func(_ context.Context, cfg Config) (runnable, error) {
		return fb, nil
	}
	t.Cleanup(func() { newBotFn = prev })
	return fb
}

// TestManagerUpdateSurvivesCallerCtxCancel is the regression for the bug
// where Manager.Update used the caller's ctx (the HTTP request ctx) as
// the parent of the new bot's lifetime. Cancelling the caller ctx after
// Update returns must NOT stop the bot — the bot's parent is the
// daemon's rootCtx captured at Start-time.
func TestManagerUpdateSurvivesCallerCtxCancel(t *testing.T) {
	fb := installFakeBotFactory(t)

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	// Simulate boot with empty token so rootCtx is captured without
	// starting a bot — same shape as a fresh install.
	if err := m.Start(daemonCtx, "", nil); err != ErrBotDisabled {
		t.Fatalf("Start(empty) = %v, want ErrBotDisabled", err)
	}

	// Simulate the panel handler: caller ctx is short-lived.
	callerCtx, callerCancel := context.WithCancel(context.Background())
	if err := m.Update(callerCtx, "fake-token", []int64{42}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Give Serve a beat to start.
	waitUntil(t, 500*time.Millisecond, func() bool { return fb.served.Load() })

	// Now cancel the caller ctx. In the buggy version this killed the bot.
	callerCancel()
	// Give any (bad) cancellation a chance to propagate. Bot must NOT exit.
	time.Sleep(80 * time.Millisecond)
	if fb.exited.Load() {
		t.Fatal("bot exited after caller ctx cancel — Update parented on wrong ctx")
	}
	if st := m.Status(); !st.Enabled {
		t.Fatalf("Status.Enabled = false after caller cancel, want true; %+v", st)
	}

	// Sanity: cancelling the daemon ctx really does stop it.
	daemonCancel()
	select {
	case <-fb.exitedCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("bot did not exit after daemon ctx cancel")
	}
}

// TestManagerCleanupClearsRunningWhenServeExits verifies bug #2 — the
// old &m.cancel == &cancel compare left running=true after Serve exited
// on its own (e.g. Telegram 401). Now the generation counter handles it.
func TestManagerCleanupClearsRunningWhenServeExits(t *testing.T) {
	fb := installFakeBotFactory(t)

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	if err := m.Start(daemonCtx, "fake-token", []int64{42}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitUntil(t, 500*time.Millisecond, func() bool { return fb.served.Load() })
	if !m.Status().Enabled {
		t.Fatal("Status.Enabled = false after Start; want true")
	}

	// Simulate Serve exiting on its own (upstream 401) by cancelling the
	// bot's ctx from outside.
	ctxPtr := fb.serveCtx.Load()
	if ctxPtr == nil {
		t.Fatal("fakeBot never captured its Serve ctx")
	}
	// The bot ctx was derived from daemonCtx; the only way for a test to
	// force its cancel is Manager.Stop() (or daemon shutdown). Use Stop.
	m.Stop()
	select {
	case <-fb.exitedCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not cancel bot ctx")
	}
	// After Serve exits, cleanup goroutine must flip running=false.
	waitUntil(t, 500*time.Millisecond, func() bool { return !m.Status().Enabled })
	if st := m.Status(); st.Enabled {
		t.Fatalf("Status.Enabled = true after Stop + Serve exit; %+v", st)
	}
}

// TestManagerUpdateSupersededDoesNotStompRunning verifies that a stale
// goroutine (whose Serve exited late) does not overwrite the running
// state that a newer generation set. Generation counter enforces this.
func TestManagerUpdateSupersededDoesNotStompRunning(t *testing.T) {
	// Two sequential fake bots. First is started, then Update replaces
	// it. First bot's Serve exit must not clear running for the second.
	fbs := []*fakeBot{newFakeBot(), newFakeBot()}
	var idx atomic.Int32
	prev := newBotFn
	newBotFn = func(_ context.Context, cfg Config) (runnable, error) {
		i := idx.Add(1) - 1
		if int(i) >= len(fbs) {
			t.Fatalf("newBotFn called %d times, want <=%d", i+1, len(fbs))
		}
		return fbs[i], nil
	}
	t.Cleanup(func() { newBotFn = prev })

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	if err := m.Start(daemonCtx, "tok1", []int64{1}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitUntil(t, 500*time.Millisecond, func() bool { return fbs[0].served.Load() })

	// Update replaces with second bot; first bot's Serve exits.
	if err := m.Update(context.Background(), "tok2", []int64{2}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// First bot must have exited by now (Update called stopLocked).
	select {
	case <-fbs[0].exitedCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first bot did not exit after Update")
	}
	waitUntil(t, 500*time.Millisecond, func() bool { return fbs[1].served.Load() })

	// Give the first bot's stale cleanup goroutine a beat to (wrongly)
	// stomp on the new bot's running=true. Generation counter must prevent that.
	time.Sleep(80 * time.Millisecond)
	if st := m.Status(); !st.Enabled || st.AdminCount != 1 {
		t.Fatalf("stale cleanup stomped current state: %+v", st)
	}
}

func TestManagerFailedUpdateKeepsPreviousBotRunning(t *testing.T) {
	oldBot := newFakeBot()
	var calls atomic.Int32
	prev := newBotFn
	newBotFn = func(_ context.Context, cfg Config) (runnable, error) {
		switch calls.Add(1) {
		case 1:
			return oldBot, nil
		case 2:
			return nil, errors.New("new token rejected")
		default:
			t.Fatalf("unexpected factory call %d", calls.Load())
			return nil, errors.New("unexpected call")
		}
	}
	t.Cleanup(func() { newBotFn = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	if err := m.Start(ctx, "old-token", []int64{7}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(context.Background(), "bad-token", []int64{9}); err == nil {
		t.Fatal("failed replacement reported success")
	}
	select {
	case <-oldBot.exitedCh:
		t.Fatal("failed candidate validation stopped the previous bot")
	case <-time.After(80 * time.Millisecond):
	}
	st := m.Status()
	if !st.Enabled || st.AdminCount != 1 || st.TokenMasked != MaskToken("old-token") {
		t.Fatalf("previous bot was not preserved: %+v", st)
	}
}

func TestManagerCancelledValidationKeepsPreviousBotRunning(t *testing.T) {
	oldBot := newFakeBot()
	validationStarted := make(chan struct{})
	var calls atomic.Int32
	prev := newBotFn
	newBotFn = func(ctx context.Context, _ Config) (runnable, error) {
		if calls.Add(1) == 1 {
			return oldBot, nil
		}
		close(validationStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { newBotFn = prev })

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	if err := m.Start(daemonCtx, "old-token", []int64{7}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 500*time.Millisecond, func() bool { return oldBot.served.Load() })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- m.Update(ctx, "blackhole-token", []int64{9}) }()
	select {
	case <-validationStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("candidate validation did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Update error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Update ignored caller cancellation")
	}
	if st := m.Status(); !st.Enabled || st.TokenMasked != MaskToken("old-token") {
		t.Fatalf("cancelled validation replaced previous bot: %+v", st)
	}
	select {
	case <-oldBot.exitedCh:
		t.Fatal("cancelled validation stopped the previous bot")
	default:
	}
}

func TestManagerValidationTimeoutBoundsBlackhole(t *testing.T) {
	oldBot := newFakeBot()
	var calls atomic.Int32
	prev := newBotFn
	newBotFn = func(ctx context.Context, _ Config) (runnable, error) {
		if calls.Add(1) == 1 {
			return oldBot, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { newBotFn = prev })

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	m := NewManager(ManagerConfig{
		Handlers:          &stubHandlers{},
		ValidationTimeout: 25 * time.Millisecond,
	})
	if err := m.Start(daemonCtx, "old-token", []int64{7}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err := m.Update(context.Background(), "blackhole-token", []int64{9})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Update error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("validation timeout took %s, want bounded return", elapsed)
	}
	if st := m.Status(); !st.Enabled || st.TokenMasked != MaskToken("old-token") {
		t.Fatalf("timed-out validation replaced previous bot: %+v", st)
	}
}

func TestManagerCommitFailureKeepsPreviousBotRunning(t *testing.T) {
	oldBot := newFakeBot()
	candidate := newFakeBot()
	var calls atomic.Int32
	prev := newBotFn
	newBotFn = func(_ context.Context, _ Config) (runnable, error) {
		if calls.Add(1) == 1 {
			return oldBot, nil
		}
		return candidate, nil
	}
	t.Cleanup(func() { newBotFn = prev })

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()
	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	if err := m.Start(daemonCtx, "old-token", []int64{7}); err != nil {
		t.Fatal(err)
	}
	commitErr := errors.New("database unavailable")
	err := m.UpdateWithCommit(context.Background(), "new-token", []int64{9}, func() error {
		return commitErr
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("UpdateWithCommit error = %v, want commit error", err)
	}
	if candidate.served.Load() {
		t.Fatal("candidate started before settings commit")
	}
	if st := m.Status(); !st.Enabled || st.TokenMasked != MaskToken("old-token") {
		t.Fatalf("commit failure replaced previous bot: %+v", st)
	}
	select {
	case <-oldBot.exitedCh:
		t.Fatal("commit failure stopped the previous bot")
	default:
	}
}

func TestManagerDisableClearsCredentialStatus(t *testing.T) {
	m := NewManager(ManagerConfig{Handlers: &stubHandlers{}})
	m.token = "old-token"
	m.adminChatIDs = []int64{7}
	if err := m.Update(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.TokenSet || st.TokenMasked != "" || st.AdminCount != 0 {
		t.Fatalf("disable retained credential metadata: %+v", st)
	}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
