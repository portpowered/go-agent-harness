package engine

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// idleReaderBound is how long state readers may take on an idle loop. They
// never wait for loop input, so any bound far above a lock hand-off works; the
// old behavior blocked them until the tick context expired.
const idleReaderBound = 2 * time.Second

// awaitTickWaiting returns once a Tick call holds tickMu, i.e. it has entered
// the wait for participant input.
func awaitTickWaiting(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.Now().Add(idleReaderBound)
	for e.tickMu.TryLock() {
		e.tickMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("Tick never started waiting for input")
		}
		runtime.Gosched()
	}
}

// readStateWithin runs every public state reader and fails if they do not
// all return within idleReaderBound.
func readStateWithin(t *testing.T, e *Engine, add []messages.Message) {
	t.Helper()
	readers := make(chan struct{})
	go func() {
		defer close(readers)
		if len(add) > 0 {
			e.AddMessages(add)
		}
		_ = e.ConversationHistorySnapshot()
		_ = e.ConversationDeltasSnapshot()
		_ = e.InteractionStateSnapshot()
		_ = e.TickState()
	}()
	select {
	case <-readers:
	case <-time.After(idleReaderBound):
		t.Fatal("state readers blocked while the loop was waiting for input")
	}
}

// Regression: Tick used to hold the loop-state write lock while blocked
// waiting for participant input, so an idle loop blocked history snapshots and
// AddMessages until the next event or context cancellation.
func TestTickWaitingForInputDoesNotBlockStateReaders(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	tickDone := make(chan error, 1)
	go func() { tickDone <- ts.engine.TickOnce(ctx) }()
	awaitTickWaiting(t, ts.engine)

	readStateWithin(t, ts.engine, []messages.Message{messages.NewTextMessage(messages.RoleUser, "hello")})
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

// A running hot loop with no participant input is idle; reading its history
// must return immediately rather than waiting for the next event.
func TestIdleHotLoopHistoryReadDoesNotBlock(t *testing.T) {
	ts := newTickTestEngine()
	ts.engine.AddMessages([]messages.Message{messages.NewTextMessage(messages.RoleUser, "seed")})
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan error, 1)
	go func() { loopDone <- ts.engine.RunHotLoopContinuous(ctx) }()
	defer func() {
		cancel()
		<-loopDone
	}()
	awaitTickWaiting(t, ts.engine)

	readStateWithin(t, ts.engine, nil)
	history := ts.engine.ConversationHistorySnapshot()
	if len(history) != 1 || history[0].TextContent() != "seed" {
		t.Fatalf("idle loop history = %+v, want the seeded user message", history)
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
