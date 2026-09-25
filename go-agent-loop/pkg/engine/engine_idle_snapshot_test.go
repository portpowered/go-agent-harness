package engine

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Regression: Tick used to hold the loop-state write lock while blocked
// waiting for participant input, so an idle running loop blocked history
// snapshots and AddMessages until the next event or context cancellation.
func TestTickWaitingForInputDoesNotBlockStateReaders(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	tickDone := make(chan error, 1)
	go func() { tickDone <- ts.engine.TickOnce(ctx) }()

	readers := make(chan struct{})
	go func() {
		defer close(readers)
		ts.engine.AddMessages([]messages.Message{messages.NewTextMessage(messages.RoleUser, "hello")})
		_ = ts.engine.ConversationHistorySnapshot()
		_ = ts.engine.ConversationDeltasSnapshot()
		_ = ts.engine.TickState()
	}()
	select {
	case <-readers:
	case <-ctx.Done():
		t.Fatal("state readers blocked while Tick was waiting for input")
	}
	if got := len(ts.engine.ConversationHistorySnapshot()); got != 1 {
		t.Fatalf("history length while tick is waiting: got %d, want 1", got)
	}

	deltasBefore := ts.engine.TickState().DeltaBufferLen
	ts.writeModelDeltas(ctx, fullTextDeltas("hi")[:1])
	select {
	case err := <-tickDone:
		if err != nil {
			t.Fatalf("TickOnce: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("TickOnce did not consume the delta written after it started waiting")
	}
	state := ts.engine.TickState()
	if state.TickCount != 1 || state.DeltaBufferLen != deltasBefore+1 {
		t.Fatalf("after tick: count=%d deltas=%d, want count=1 deltas=%d", state.TickCount, state.DeltaBufferLen, deltasBefore+1)
	}
}

// Concurrent Tick callers must still consume and apply one input per tick.
func TestConcurrentTicksApplyEachInputOnce(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	const ticks = 4
	done := make(chan error, ticks)
	for range ticks {
		go func() { done <- ts.engine.TickOnce(ctx) }()
	}
	ts.writeModelDeltas(ctx, fullTextDeltas("x")[:ticks])
	for range ticks {
		if err := <-done; err != nil {
			t.Fatalf("TickOnce: %v", err)
		}
	}
	state := ts.engine.TickState()
	if state.TickCount != ticks || state.DeltaBufferLen != ticks {
		t.Fatalf("got count=%d deltas=%d, want %d each", state.TickCount, state.DeltaBufferLen, ticks)
	}
}
