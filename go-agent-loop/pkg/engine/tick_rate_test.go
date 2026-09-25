package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type observedTickClock struct {
	*clock.Deterministic
	admitted chan time.Duration
}

func (c *observedTickClock) NewTimer(delay time.Duration) clock.Timer {
	timer := c.Deterministic.NewTimer(delay)
	c.admitted <- delay
	return timer
}

func awaitTickTimer(t *testing.T, source *observedTickClock) time.Duration {
	t.Helper()
	select {
	case delay := <-source.admitted:
		return delay
	case <-time.After(5 * time.Second):
		t.Fatal("hot loop did not admit its pacing timer")
		return 0
	}
}

func TestHotLoopUsesInjectedTimeAndCancelsPacing(t *testing.T) {
	const interval = 25 * time.Millisecond
	source := &observedTickClock{Deterministic: clock.NewDeterministic(time.Unix(42, 0), time.Millisecond), admitted: make(chan time.Duration, 8)}
	eng, _ := newTickRateTestEngine(interval)
	eng.SetClock(source)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- eng.RunHotLoop(ctx) }()
	if delay := awaitTickTimer(t, source); delay != interval {
		t.Fatalf("pacing delay = %v", delay)
	}
	before := eng.TickState().TickCount
	source.AdvanceBy(24 * time.Millisecond)
	if got := eng.TickState().TickCount; got != before {
		t.Fatalf("tick advanced early: %d -> %d", before, got)
	}
	source.AdvanceBy(time.Millisecond)
	if delay := awaitTickTimer(t, source); delay != interval {
		t.Fatalf("next pacing delay = %v", delay)
	}
	if got := eng.TickState().TickCount; got <= before {
		t.Fatal("clock advance did not release next tick")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation waited for virtual time to advance")
	}
}

// textInferencer returns a single text response as streaming deltas.
// It produces MESSAGE.START → TEXT.START → TEXT.DELTA → TEXT.END → MESSAGE.END.
type textInferencer struct {
	text string
}

func (ti *textInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, ti.text),
	}, nil
}

func (ti *textInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 8)
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(ti.text)}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(ch)
	return ch, nil
}

// newTickRateTestEngine creates an engine with real participants and a textInferencer.
// The kernel event reader is returned so the caller can detect turn completion.
func newTickRateTestEngine(tickRate time.Duration) (*Engine, *participants.KernelRunner) {
	bufCap := 64
	inf := &textInferencer{text: "hello"}
	modelRunner := participants.NewModelRunner(inf, bufCap)
	toolRunner := participants.NewToolRunner(&messages.DefaultToolExecutor{}, bufCap)
	userRunner := participants.NewUserRunner(bufCap)
	kernelRunner := participants.NewKernelRunner(nil, bufCap)

	coord := subsystems.NewCoordinator(nil)
	coordDelta := subsystems.NewCoordinatorDelta(kernelRunner.DeltaInbox, nil)
	interruptHandler := subsystems.NewInterruptHandler(modelRunner, toolRunner, nil)
	hlps := []subsystems.Subsystem{interruptHandler, coord, coordDelta}

	eng := NewEngine(ModeAskOnce, nil, hlps, modelRunner, toolRunner, userRunner, kernelRunner, nil)

	// Add a user message so RunHotLoop has something to seed the initial inference from.
	eng.AddMessages([]messages.Message{messages.NewTextMessage(messages.RoleUser, "test")})

	if tickRate > 0 {
		eng.SetTickRate(tickRate)
	}

	return eng, kernelRunner
}

func newObservedTickClock() *observedTickClock {
	return &observedTickClock{
		Deterministic: clock.NewDeterministic(time.Unix(42, 0), time.Millisecond),
		admitted:      make(chan time.Duration, 64),
	}
}

// runTurnAdvancingPacing runs one hot-loop turn to completion on the
// injected clock. Every pacing timer the loop admits is released by
// advancing virtual time by exactly its delay; the requested delays are
// returned in order. No wall-clock time bounds the pacing itself.
func runTurnAdvancingPacing(t *testing.T, eng *Engine, kernelRunner *participants.KernelRunner, source *observedTickClock) []time.Duration {
	t.Helper()
	eng.SetClock(source)
	eventCh := kernelRunner.NewDeltaEventReader(64)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loopDone := make(chan error, 1)
	go func() { loopDone <- eng.RunHotLoop(ctx) }()
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		for range eventCh {
		}
	}()

	var delays []time.Duration
	failure := time.NewTimer(5 * time.Second)
	defer failure.Stop()
	for waiting := true; waiting; {
		select {
		case delay := <-source.admitted:
			delays = append(delays, delay)
			source.AdvanceBy(delay)
		case <-turnDone:
			waiting = false
		case <-failure.C:
			t.Fatalf("turn did not complete; pacing delays so far: %v", delays)
		}
	}
	cancel()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("hot loop did not stop after cancellation")
	}
	return delays
}

// A configured tick rate paces every hot-loop tick of a turn by exactly
// that interval on the injected clock.
func TestRunHotLoop_TickRateThrottlesLoop(t *testing.T) {
	const tickRate = 25 * time.Millisecond
	eng, kernelRunner := newTickRateTestEngine(tickRate)
	delays := runTurnAdvancingPacing(t, eng, kernelRunner, newObservedTickClock())

	// Each tick consumes one of MESSAGE.START, TEXT.START, TEXT.DELTA,
	// TEXT.END and MESSAGE.END, and the next tick cannot start until the
	// previous pacing timer is released, so the four ticks before the one
	// that ends the turn are always paced. (The last tick's timer may be
	// admitted after the turn is observed complete.)
	if len(delays) < 4 {
		t.Fatalf("paced ticks = %d (%v), want at least 4", len(delays), delays)
	}
	for index, delay := range delays {
		if delay != tickRate {
			t.Fatalf("pacing delay[%d] = %v, want %v", index, delay, tickRate)
		}
	}
}

// Without a tick rate (the default, or reset to zero) the hot loop never
// requests a pacing timer.
func TestRunHotLoop_ZeroTickRateNeverPaces(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Engine)
	}{
		{name: "default", setup: func(*Engine) {}},
		{name: "reset to zero", setup: func(eng *Engine) { eng.SetTickRate(50 * time.Millisecond); eng.SetTickRate(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, kernelRunner := newTickRateTestEngine(0)
			tc.setup(eng)
			if delays := runTurnAdvancingPacing(t, eng, kernelRunner, newObservedTickClock()); len(delays) != 0 {
				t.Fatalf("pacing delays = %v, want none without a tick rate", delays)
			}
		})
	}
}

// Manual ticks (TickN) are never paced, even with a tick rate configured.
func TestManualTick_NotAffectedByTickRate(t *testing.T) {
	ts := newTickTestEngine()
	source := newObservedTickClock()
	ts.engine.SetClock(source)
	ts.engine.SetTickRate(500 * time.Millisecond)

	ctx, cancel := tickCtx(t)
	defer cancel()

	deltas := fullTextDeltas("manual")
	ts.writeModelDeltas(ctx, deltas)
	if err := ts.engine.TickN(ctx, len(deltas)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ts.engine.TickState().TickCount; got != len(deltas) {
		t.Fatalf("tick count = %d, want %d", got, len(deltas))
	}
	select {
	case delay := <-source.admitted:
		t.Fatalf("manual tick requested a %v pacing timer", delay)
	default:
	}
}
