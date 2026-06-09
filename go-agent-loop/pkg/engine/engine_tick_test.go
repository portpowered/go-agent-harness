package engine

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
)

// noopInferencer never produces output; used for tick control tests where
// deltas are injected directly into runner outboxes.
type noopInferencer struct{}

func (n *noopInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, nil
}

func (n *noopInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

type tickTestState struct {
	engine       *Engine
	modelRunner  *participants.ModelRunner
	toolRunner   *participants.ToolRunner
	userRunner   *participants.UserRunner
	kernelRunner *participants.KernelRunner
}

// newTickTestEngine creates a minimal engine for manual tick control tests.
// Participants are NOT started; tests inject deltas directly into runner outboxes.
func newTickTestEngine() *tickTestState {
	bufCap := 64
	modelRunner := participants.NewModelRunner(&noopInferencer{}, bufCap)
	toolRunner := participants.NewToolRunner(&messages.DefaultToolExecutor{}, bufCap)
	userRunner := participants.NewUserRunner(bufCap)
	kernelRunner := participants.NewKernelRunner(nil, bufCap)

	coord := subsystems.NewCoordinator(nil)
	coordDelta := subsystems.NewCoordinatorDelta(*kernelRunner.DeltaInbox, nil)
	hlps := []subsystems.Subsystem{coord, coordDelta}

	eng := NewEngine(
		ModeAskOnce,
		nil,
		hlps,
		modelRunner,
		toolRunner,
		userRunner,
		kernelRunner,
		nil,
	)

	return &tickTestState{
		engine:       eng,
		modelRunner:  modelRunner,
		toolRunner:   toolRunner,
		userRunner:   userRunner,
		kernelRunner: kernelRunner,
	}
}

// writeModelDeltas writes a slice of deltas to the model runner's outbox.
func (ts *tickTestState) writeModelDeltas(ctx context.Context, deltas []messages.StreamMessage) {
	for _, d := range deltas {
		ts.modelRunner.DeltaOutbox.Write(ctx, d)
	}
}

// fullTextDeltas returns the complete delta sequence for an assistant text message.
func fullTextDeltas(text string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
}

func tickCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// --- TickOnce tests ---

func TestTickOnce_ConsumesModelDelta(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Write a single delta to model outbox.
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})

	err := ts.engine.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	// Delta should have been consumed (outbox empty).
	if ts.modelRunner.DeltaOutbox.HasData() {
		t.Error("expected model outbox to be empty after TickOnce")
	}

	// Delta buffer should contain the consumed delta.
	state := ts.engine.TickState()
	if state.DeltaBufferLen != 1 {
		t.Errorf("DeltaBufferLen: got %d, want 1", state.DeltaBufferLen)
	}
}

func TestTickOnce_TickCountIncrements(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	if ts.engine.TickState().TickCount != 0 {
		t.Fatal("initial tick count should be 0")
	}

	// Write deltas for two ticks.
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextStartValue(),
	})

	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if ts.engine.TickState().TickCount != 1 {
		t.Errorf("after tick 1: got %d, want 1", ts.engine.TickState().TickCount)
	}

	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if ts.engine.TickState().TickCount != 2 {
		t.Errorf("after tick 2: got %d, want 2", ts.engine.TickState().TickCount)
	}
}

func TestTickOnce_ContextCancelled(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := ts.engine.TickOnce(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Tick count should not increment on error.
	if ts.engine.TickState().TickCount != 0 {
		t.Errorf("tick count should be 0 on error, got %d", ts.engine.TickState().TickCount)
	}
}

// --- TickN tests ---

func TestTickN_ProcessesMultipleDeltas(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Write 3 deltas (one consumed per tick).
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue(),
	})
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue(),
	})
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("hello"),
	})

	err := ts.engine.TickN(ctx, 3)
	if err != nil {
		t.Fatalf("TickN(3): %v", err)
	}

	state := ts.engine.TickState()
	if state.TickCount != 3 {
		t.Errorf("TickCount: got %d, want 3", state.TickCount)
	}
	if state.DeltaBufferLen != 3 {
		t.Errorf("DeltaBufferLen: got %d, want 3", state.DeltaBufferLen)
	}
}

func TestTickN_ZeroTicks(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	err := ts.engine.TickN(ctx, 0)
	if err != nil {
		t.Fatalf("TickN(0): %v", err)
	}

	if ts.engine.TickState().TickCount != 0 {
		t.Error("TickN(0) should not increment tick count")
	}
}

func TestTickN_StopsOnError(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Write only 1 delta but request 3 ticks. Second tick will block and timeout.
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue(),
	})

	shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer shortCancel()

	err := ts.engine.TickN(shortCtx, 3)
	if err == nil {
		t.Fatal("expected error from TickN with insufficient data")
	}

	// First tick should have succeeded.
	if ts.engine.TickState().TickCount != 1 {
		t.Errorf("expected 1 successful tick, got %d", ts.engine.TickState().TickCount)
	}
}

// --- TickUntil tests ---

func TestTickUntil_ConditionMetAfterProcessing(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Write a full model response. After all deltas are consumed, the Coordinator
	// should send the final response to UserInbox.
	deltas := fullTextDeltas("hello world")
	ts.writeModelDeltas(ctx, deltas)

	predicate := func() bool {
		return ts.engine.TickState().UserInboxLen > 0
	}

	ticks, err := ts.engine.TickUntil(ctx, predicate, 10)
	if err != nil {
		t.Fatalf("TickUntil: %v", err)
	}

	// Should take exactly 5 ticks (one per delta) to process the full response.
	if ticks != 5 {
		t.Errorf("expected 5 ticks, got %d", ticks)
	}

	// UserInbox should have the final response.
	state := ts.engine.TickState()
	if state.UserInboxLen != 1 {
		t.Errorf("UserInboxLen: got %d, want 1", state.UserInboxLen)
	}
	if state.TickCount != 5 {
		t.Errorf("TickCount: got %d, want 5", state.TickCount)
	}
}

func TestTickUntil_AlreadySatisfied(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Predicate is already true.
	ticks, err := ts.engine.TickUntil(ctx, func() bool { return true }, 10)
	if err != nil {
		t.Fatalf("TickUntil: %v", err)
	}
	if ticks != 0 {
		t.Errorf("expected 0 ticks when already satisfied, got %d", ticks)
	}
	if ts.engine.TickState().TickCount != 0 {
		t.Error("no ticks should have been executed")
	}
}

func TestTickUntil_MaxTicksExceeded(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Write enough deltas to not block, but predicate never satisfied.
	for i := 0; i < 5; i++ {
		ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
			Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("x"),
		})
	}

	ticks, err := ts.engine.TickUntil(ctx, func() bool { return false }, 5)
	if err == nil {
		t.Fatal("expected error when predicate not satisfied")
	}
	if ticks != 5 {
		t.Errorf("expected 5 ticks attempted, got %d", ticks)
	}
}

// --- TickState tests ---

func TestTickState_InitialState(t *testing.T) {
	ts := newTickTestEngine()
	state := ts.engine.TickState()

	if state.TickCount != 0 {
		t.Errorf("TickCount: got %d, want 0", state.TickCount)
	}
	if state.ConversationLen != 0 {
		t.Errorf("ConversationLen: got %d, want 0", state.ConversationLen)
	}
	if state.DeltaBufferLen != 0 {
		t.Errorf("DeltaBufferLen: got %d, want 0", state.DeltaBufferLen)
	}
	if state.ModelInboxLen != 0 {
		t.Errorf("ModelInboxLen: got %d, want 0", state.ModelInboxLen)
	}
	if state.ToolInboxLen != 0 {
		t.Errorf("ToolInboxLen: got %d, want 0", state.ToolInboxLen)
	}
	if state.UserInboxLen != 0 {
		t.Errorf("UserInboxLen: got %d, want 0", state.UserInboxLen)
	}
	if state.KernelDeltaInboxLen != 0 {
		t.Errorf("KernelDeltaInboxLen: got %d, want 0", state.KernelDeltaInboxLen)
	}
}

func TestTickState_ReflectsStateAfterTicks(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Add a user message to conversation history.
	ts.engine.AddMessages([]messages.Message{
		messages.NewTextMessage(messages.RoleUser, "hi"),
	})

	state := ts.engine.TickState()
	if state.ConversationLen != 1 {
		t.Errorf("ConversationLen after AddMessages: got %d, want 1", state.ConversationLen)
	}

	// Write model deltas and tick through them.
	deltas := fullTextDeltas("response")
	ts.writeModelDeltas(ctx, deltas)

	if err := ts.engine.TickN(ctx, 5); err != nil {
		t.Fatalf("TickN(5): %v", err)
	}

	state = ts.engine.TickState()
	if state.TickCount != 5 {
		t.Errorf("TickCount: got %d, want 5", state.TickCount)
	}
	// ConversationBuffer should now have user message + assistant message.
	if state.ConversationLen != 2 {
		t.Errorf("ConversationLen: got %d, want 2", state.ConversationLen)
	}
	// DeltaBuffer has user message deltas (from AddMessages) + model deltas.
	if state.DeltaBufferLen < 5 {
		t.Errorf("DeltaBufferLen: got %d, want >= 5", state.DeltaBufferLen)
	}
}

func TestTickState_BufferOccupancyAfterDispatch(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	// Write a full model response with tool calls → Coordinator dispatches to ToolInbox.
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-1", "test_tool")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-1", "test_tool", `{"arg":"val"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	ts.writeModelDeltas(ctx, deltas)

	// Tick through all 4 deltas.
	if err := ts.engine.TickN(ctx, 4); err != nil {
		t.Fatalf("TickN(4): %v", err)
	}

	// Coordinator should have dispatched a ToolBatchRequest.
	state := ts.engine.TickState()
	if state.ToolInboxLen != 1 {
		t.Errorf("ToolInboxLen: got %d, want 1", state.ToolInboxLen)
	}
}

// --- Assertions between ticks (demonstrating the core use case) ---

func TestManualTickControl_AssertionsBetweenTicks(t *testing.T) {
	ts := newTickTestEngine()
	ctx, cancel := tickCtx(t)
	defer cancel()

	deltas := fullTextDeltas("step by step")
	ts.writeModelDeltas(ctx, deltas)

	// Tick 1: MESSAGE.START consumed.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	s := ts.engine.TickState()
	if s.TickCount != 1 {
		t.Errorf("after tick 1: TickCount=%d", s.TickCount)
	}
	if s.ConversationLen != 0 {
		t.Errorf("after tick 1: ConversationLen=%d (no full message yet)", s.ConversationLen)
	}

	// Tick 2: TEXT.START consumed.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	s = ts.engine.TickState()
	if s.TickCount != 2 {
		t.Errorf("after tick 2: TickCount=%d", s.TickCount)
	}

	// Tick 3: TEXT.DELTA consumed.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}

	// Tick 4: TEXT.END consumed.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 4: %v", err)
	}

	// Still no full message in ConversationBuffer (no MESSAGE.END yet).
	s = ts.engine.TickState()
	if s.ConversationLen != 0 {
		t.Errorf("before MESSAGE.END: ConversationLen=%d, want 0", s.ConversationLen)
	}

	// Tick 5: MESSAGE.END consumed → full message reconstructed → Coordinator dispatches.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("tick 5: %v", err)
	}

	s = ts.engine.TickState()
	if s.TickCount != 5 {
		t.Errorf("after tick 5: TickCount=%d", s.TickCount)
	}
	// Full message should be in ConversationBuffer now.
	if s.ConversationLen != 1 {
		t.Errorf("after MESSAGE.END: ConversationLen=%d, want 1", s.ConversationLen)
	}
	// Coordinator should have sent final response to UserInbox (no tool calls).
	if s.UserInboxLen != 1 {
		t.Errorf("after MESSAGE.END: UserInboxLen=%d, want 1", s.UserInboxLen)
	}
}
