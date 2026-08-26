package engine

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
)

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

func TestSetTickRate_DefaultIsZero(t *testing.T) {
	ts := newTickTestEngine()
	if ts.engine.tickRate != 0 {
		t.Fatalf("expected default tickRate 0, got %v", ts.engine.tickRate)
	}
}

func TestSetTickRate_SetsValue(t *testing.T) {
	ts := newTickTestEngine()
	ts.engine.SetTickRate(50 * time.Millisecond)
	if ts.engine.tickRate != 50*time.Millisecond {
		t.Fatalf("expected tickRate 50ms, got %v", ts.engine.tickRate)
	}
}

func TestSetTickRate_ZeroMeansNoDelay(t *testing.T) {
	ts := newTickTestEngine()
	ts.engine.SetTickRate(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deltas := fullTextDeltas("hello")
	ts.writeModelDeltas(ctx, deltas)

	start := time.Now()
	err := ts.engine.TickN(ctx, len(deltas))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected fast execution with zero tick rate, took %v", elapsed)
	}
}

func TestManualTick_NotAffectedByTickRate(t *testing.T) {
	ts := newTickTestEngine()
	ts.engine.SetTickRate(500 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deltas := fullTextDeltas("manual")
	ts.writeModelDeltas(ctx, deltas)

	start := time.Now()
	err := ts.engine.TickN(ctx, len(deltas))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("manual ticks should not be throttled by tick rate, took %v", elapsed)
	}
}

func TestRunHotLoop_TickRateThrottlesLoop(t *testing.T) {
	tickRate := 25 * time.Millisecond
	eng, kernelRunner := newTickRateTestEngine(tickRate)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up kernel event reader to detect turn completion.
	eventCh := kernelRunner.NewDeltaEventReader(64)

	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		errCh <- eng.RunHotLoop(ctx)
	}()

	// Drain kernel events until channel closes (LOOP.END processed).
	for range eventCh {
	}
	elapsed := time.Since(start)
	cancel()

	// Wait for hot loop to exit.
	<-errCh

	// The engine processes at least 5 ticks (MESSAGE.START → TEXT.START → TEXT.DELTA
	// → TEXT.END → MESSAGE.END) plus coordinator ticks. With a 25ms tick rate,
	// elapsed should be measurably longer than without tick rate.
	// Use a conservative lower bound: at least 3 * tickRate.
	minExpected := 3 * tickRate
	if elapsed < minExpected {
		t.Fatalf("expected at least %v with tick rate %v, got %v", minExpected, tickRate, elapsed)
	}
}

func TestRunHotLoop_NoTickRateRunsFast(t *testing.T) {
	eng, kernelRunner := newTickRateTestEngine(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh := kernelRunner.NewDeltaEventReader(64)

	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		errCh <- eng.RunHotLoop(ctx)
	}()

	for range eventCh {
	}
	elapsed := time.Since(start)
	cancel()

	<-errCh

	// Without tick rate, the turn should complete quickly.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected fast execution without tick rate, took %v", elapsed)
	}
}
