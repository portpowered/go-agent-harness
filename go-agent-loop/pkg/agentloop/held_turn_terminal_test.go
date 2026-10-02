package agentloop

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// gatedErrorInferencer streams part of a response, then waits on gate and
// ends it with a terminal provider ERROR instead of MESSAGE.END.
type gatedErrorInferencer struct{ gate chan struct{} }

func (g *gatedErrorInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, fmt.Errorf("gatedErrorInferencer: streaming only")
}

func (g *gatedErrorInferencer) InferStream(ctx context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 8)
	go func() {
		defer close(ch)
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")}
		select {
		case <-g.gate:
		case <-ctx.Done():
			return
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithTerminal(
			"boom", "", messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)}
	}()
	return ch, nil
}

// heldTurnHistoryAfter executes "first", sends "second" while the first
// response is open (so the turn is held), ends the loop with end, and
// returns the conversation history after the loop stopped.
func heldTurnHistoryAfter(t *testing.T, end func(inf *gatedErrorInferencer, cancel context.CancelFunc)) []string {
	t.Helper()
	inf := &gatedErrorInferencer{gate: make(chan struct{})}
	loop, err := New(WithInferencer(inf), WithToolExecutor(&countingToolExecutor{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("first"))
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	awaitEvent(t, messages.StreamTypeTextDelta)(result.EventStream)
	if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	synctest.Wait()
	end(inf, cancel)
	for result.EventStream.HasNext() {
		result.EventStream.Response()
	}
	return describeTurnHistory(loop.GetConversationHistory())
}

// A held user turn must survive every way the loop ends outside a tick: a
// terminal provider ERROR and cancellation both end the loop with the turn
// in history, as it was before turns were held.
func TestHeldUserTurnSurvivesLoopEndOutsideATick(t *testing.T) {
	ends := map[string]func(*gatedErrorInferencer, context.CancelFunc){
		"terminal error": func(inf *gatedErrorInferencer, _ context.CancelFunc) { close(inf.gate) },
		"cancel":         func(_ *gatedErrorInferencer, cancel context.CancelFunc) { cancel() },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				history := heldTurnHistoryAfter(t, end)
				if want := []string{"user:first", "user:second"}; fmt.Sprint(history) != fmt.Sprint(want) {
					t.Fatalf("history after %s:\n got %q\nwant %q (the held user turn was lost)", name, history, want)
				}
			})
		})
	}
}

// ctxBoundRecorder honours its context once stuck, like a bounded async
// writer whose queue is full.
type ctxBoundRecorder struct {
	stuck   atomic.Bool
	records atomic.Int64
}

func (r *ctxBoundRecorder) Record(ctx context.Context, _ []messages.Message) error {
	r.records.Add(1)
	if !r.stuck.Load() {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// The recorder flush that records held turns when the loop is cancelled is
// bounded: a recorder that blocks until its context ends must not hang
// shutdown. The flush runs with WithSettleRecordTimeout's bound, outside the
// history lock, so the stream finishes once that bound elapses.
func TestSettleRecorderFlushOnCancelIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const bound = 2 * time.Second
		inf := &gatedErrorInferencer{gate: make(chan struct{})}
		rec := &ctxBoundRecorder{}
		loop, err := New(WithInferencer(inf), WithToolExecutor(&countingToolExecutor{}), WithRecorder(rec), WithSettleRecordTimeout(bound))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("first"))
		if err != nil {
			t.Fatalf("ExecuteStreaming: %v", err)
		}
		awaitEvent(t, messages.StreamTypeTextDelta)(result.EventStream)
		if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		synctest.Wait()
		rec.stuck.Store(true)
		started := time.Now()
		cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for result.EventStream.HasNext() {
				result.EventStream.Response()
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Hour):
			t.Fatal("stream never finished after cancel: the settle flush ignored its bound and blocked shutdown")
		}
		if elapsed := time.Since(started); elapsed < bound || elapsed > bound+time.Second {
			t.Fatalf("shutdown took %v, want the %v flush bound", elapsed, bound)
		}
		if history := describeTurnHistory(loop.GetConversationHistory()); fmt.Sprint(history) != fmt.Sprint([]string{"user:first", "user:second"}) {
			t.Fatalf("history: got %q, want the held turn placed", history)
		}
	})
}
