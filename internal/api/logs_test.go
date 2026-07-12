package api

import (
	"context"
	"testing"
	"time"
)

func TestSendLogFrameStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string)
	done := make(chan bool, 1)
	go func() {
		done <- sendLogFrame(ctx, out, "stub")
	}()

	cancel()
	select {
	case sent := <-done:
		if sent {
			t.Fatal("frame unexpectedly sent without a receiver")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked log send did not exit after cancellation")
	}
}
