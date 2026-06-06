package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-loop/pkg/subsystems"
)

// ---------------------------------------------------------------------------
// Backpressure & buffer saturation tests (US-021)
//
// These tests verify system behavior when TypedBuffers fill up. The expected
// behavior under saturation is:
//
//   - TypedBuffer.Write is non-blocking: when the buffer is full, the write
//     is silently dropped (returns false) and the OnDrop callback fires.
//   - No panics, deadlocks, or goroutine leaks occur under saturation.
//   - The system continues operating (degrades gracefully) — it does not block
//     or crash when buffers are full.
//   - The OnDrop callback is the sole mechanism for operators to detect drops;
//     it incurs zero allocation overhead on the success path.
//
// The tests use buffer capacity of 1 to make saturation deterministic and
// verifiable without timing dependencies.
// ---------------------------------------------------------------------------

// backpressureTestState builds an engine with buffer capacity 1 for
// deterministic saturation testing. Participants are NOT started; tests
// inject data directly into runner outboxes and tick manually.
type backpressureTestState struct {
	engine       *Engine
	modelRunner  *participants.ModelRunner
	toolRunner   *participants.ToolRunner
	userRunner   *participants.UserRunner
	kernelRunner *participants.KernelRunner
}

func newBackpressureTestEngine() *backpressureTestState {
	const bufCap = 1

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

	return &backpressureTestState{
		engine:       eng,
		modelRunner:  modelRunner,
		toolRunner:   toolRunner,
		userRunner:   userRunner,
		kernelRunner: kernelRunner,
	}
}

// ---------------------------------------------------------------------------
// Test: Model produces deltas faster than consumer reads them
// ---------------------------------------------------------------------------

// TestBackpressure_ModelDeltaOutboxSaturated verifies that when the model
// runner's DeltaOutbox (capacity 1) is full, additional writes are dropped
// and the OnDrop callback fires. This simulates the model producing deltas
// faster than the engine can consume them.
func TestBackpressure_ModelDeltaOutboxSaturated(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx := context.Background()

	var dropCount atomic.Int64
	ts.modelRunner.DeltaOutbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Fill the buffer (capacity 1).
	ok := ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})
	if !ok {
		t.Fatal("first write should succeed (buffer was empty)")
	}

	// Buffer is now full. Subsequent writes should be dropped.
	ok = ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("dropped"),
	})
	if ok {
		t.Error("second write should fail (buffer full)")
	}

	ok = ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextEndValue(),
	})
	if ok {
		t.Error("third write should fail (buffer full)")
	}

	if dropCount.Load() != 2 {
		t.Errorf("OnDrop: got %d calls, want 2", dropCount.Load())
	}

	// Verify buffer occupancy: exactly 1 item (the first write).
	if ts.modelRunner.DeltaOutbox.Len() != 1 {
		t.Errorf("buffer Len: got %d, want 1", ts.modelRunner.DeltaOutbox.Len())
	}

	// Consume the item; buffer should be empty now.
	if _, read := ts.modelRunner.DeltaOutbox.Read(); !read {
		t.Error("expected to read the first delta")
	}
	if ts.modelRunner.DeltaOutbox.Len() != 0 {
		t.Errorf("after drain: got Len %d, want 0", ts.modelRunner.DeltaOutbox.Len())
	}
}

// TestBackpressure_ModelDeltasSaturateDuringStreaming verifies that when
// the model runner writes deltas via its DeltaOutbox (capacity 1) and
// the engine is not consuming, the OnDrop fires for excess deltas. After
// the engine ticks to drain one item, the next write succeeds.
func TestBackpressure_ModelDeltasSaturateDuringStreaming(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var dropCount atomic.Int64
	ts.modelRunner.DeltaOutbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Simulate the model producing 5 deltas without the engine consuming.
	deltas := fullTextDeltas("saturated")
	for _, d := range deltas {
		ts.modelRunner.DeltaOutbox.Write(ctx, d)
	}

	// Only the first delta fits; the rest are dropped.
	expectedDrops := int64(len(deltas) - 1)
	if dropCount.Load() != expectedDrops {
		t.Errorf("OnDrop: got %d calls, want %d", dropCount.Load(), expectedDrops)
	}

	// Engine ticks once, consuming the sole buffered delta.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	// After draining, a new write should succeed.
	dropsBefore := dropCount.Load()
	ok := ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("after-drain"),
	})
	if !ok {
		t.Error("write after drain should succeed")
	}
	if dropCount.Load() != dropsBefore {
		t.Error("OnDrop should not fire after buffer was drained")
	}
}

// ---------------------------------------------------------------------------
// Test: Tool results arrive while tool delta outbox is full
// ---------------------------------------------------------------------------

// TestBackpressure_ToolDeltaOutboxSaturated verifies that when the tool
// runner's DeltaOutbox (capacity 1) is full, additional tool result deltas
// are dropped and the OnDrop callback fires.
func TestBackpressure_ToolDeltaOutboxSaturated(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx := context.Background()

	var dropCount atomic.Int64
	ts.toolRunner.DeltaOutbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Fill the tool delta outbox.
	ok := ts.toolRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleTool,
		Value: messages.NewMessageStartValue(),
	})
	if !ok {
		t.Fatal("first write should succeed")
	}

	// Additional tool result deltas should be dropped.
	for i := 0; i < 3; i++ {
		ts.toolRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleTool,
			Value: messages.NewTextDeltaValue("result"),
		})
	}

	if dropCount.Load() != 3 {
		t.Errorf("OnDrop: got %d calls, want 3", dropCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Test: User sends messages while user inbox is full
// ---------------------------------------------------------------------------

// TestBackpressure_UserOutboxSaturated verifies that when the user runner's
// Outbox (capacity 1) is full, UserRunner.Write returns an error indicating
// the buffer is full.
func TestBackpressure_UserOutboxSaturated(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx := context.Background()

	var dropCount atomic.Int64
	ts.userRunner.Outbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// First write succeeds (buffer empty).
	err := ts.userRunner.Write(ctx, messages.NewTextMessage(messages.RoleUser, "first"))
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	// Second write should fail — buffer is full.
	err = ts.userRunner.Write(ctx, messages.NewTextMessage(messages.RoleUser, "second"))
	if err == nil {
		t.Error("second Write should return an error when buffer is full")
	}

	// OnDrop should have fired for the failed write.
	if dropCount.Load() != 1 {
		t.Errorf("OnDrop: got %d calls, want 1", dropCount.Load())
	}
}

// TestBackpressure_UserInboxSaturated verifies that when the Coordinator
// writes a UserRequest to UserInbox (capacity 1) and it is already full,
// the OnDrop callback fires. This happens when the user runner cannot
// process requests fast enough (or is not started).
func TestBackpressure_UserInboxSaturated(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx := context.Background()

	var dropCount atomic.Int64
	ts.userRunner.Inbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Pre-fill the UserInbox (capacity 1).
	ok := ts.userRunner.Inbox.Write(ctx, messages.UserRequest{
		Message: messages.NewTextMessage(messages.RoleAssistant, "response-1"),
	})
	if !ok {
		t.Fatal("first write should succeed")
	}

	// Second write should be dropped.
	ok = ts.userRunner.Inbox.Write(ctx, messages.UserRequest{
		Message: messages.NewTextMessage(messages.RoleAssistant, "response-2"),
	})
	if ok {
		t.Error("second write should fail (buffer full)")
	}

	if dropCount.Load() != 1 {
		t.Errorf("OnDrop: got %d calls, want 1", dropCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Test: KernelDeltaInbox saturated during coordination
// ---------------------------------------------------------------------------

// TestBackpressure_KernelDeltaInboxSaturated verifies that when the
// Coordinator dispatches a SYSTEM.FULL_MESSAGE to KernelDeltaInbox
// (capacity 1) and the CoordinatorDelta also attempts to forward deltas,
// the OnDrop callback fires for excess writes.
func TestBackpressure_KernelDeltaInboxSaturated(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx := context.Background()

	var dropCount atomic.Int64
	ts.kernelRunner.DeltaInbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Pre-fill KernelDeltaInbox (capacity 1).
	ok := ts.kernelRunner.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue("occupying"),
		},
	})
	if !ok {
		t.Fatal("first write should succeed")
	}

	// Additional writes should be dropped.
	for i := 0; i < 3; i++ {
		ts.kernelRunner.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
			Delta: messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewTextDeltaValue("excess"),
			},
		})
	}

	if dropCount.Load() != 3 {
		t.Errorf("OnDrop: got %d calls, want 3", dropCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Test: End-to-end OnDrop callback integration
// ---------------------------------------------------------------------------

// TestBackpressure_OnDropCallbackIntegration verifies the OnDrop callback
// pattern from US-005 works end-to-end: callbacks are set on all participant
// buffers, and each fires when its respective buffer is saturated.
func TestBackpressure_OnDropCallbackIntegration(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx := context.Background()

	// Track drops per buffer.
	var modelInboxDrops, modelOutboxDrops atomic.Int64
	var toolInboxDrops, toolOutboxDrops atomic.Int64
	var userInboxDrops, userOutboxDrops atomic.Int64
	var kernelInboxDrops atomic.Int64

	ts.modelRunner.Inbox.SetOnDrop(func() { modelInboxDrops.Add(1) })
	ts.modelRunner.DeltaOutbox.SetOnDrop(func() { modelOutboxDrops.Add(1) })
	ts.toolRunner.Inbox.SetOnDrop(func() { toolInboxDrops.Add(1) })
	ts.toolRunner.DeltaOutbox.SetOnDrop(func() { toolOutboxDrops.Add(1) })
	ts.userRunner.Inbox.SetOnDrop(func() { userInboxDrops.Add(1) })
	ts.userRunner.Outbox.SetOnDrop(func() { userOutboxDrops.Add(1) })
	ts.kernelRunner.DeltaInbox.SetOnDrop(func() { kernelInboxDrops.Add(1) })

	// Saturate each buffer (capacity 1) with 2 writes: 1 succeeds, 1 drops.
	ts.modelRunner.Inbox.Write(ctx, messages.InferenceRequest{})
	ts.modelRunner.Inbox.Write(ctx, messages.InferenceRequest{})

	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta})

	ts.toolRunner.Inbox.Write(ctx, messages.ToolBatchRequest{})
	ts.toolRunner.Inbox.Write(ctx, messages.ToolBatchRequest{})

	ts.toolRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	ts.toolRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta})

	ts.userRunner.Inbox.Write(ctx, messages.UserRequest{})
	ts.userRunner.Inbox.Write(ctx, messages.UserRequest{})

	ts.userRunner.Outbox.Write(ctx, messages.UserResponse{})
	ts.userRunner.Outbox.Write(ctx, messages.UserResponse{})

	ts.kernelRunner.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{})
	ts.kernelRunner.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{})

	// Verify each buffer's OnDrop fired exactly once.
	checks := []struct {
		name  string
		drops int64
	}{
		{"ModelInbox", modelInboxDrops.Load()},
		{"ModelDeltaOutbox", modelOutboxDrops.Load()},
		{"ToolInbox", toolInboxDrops.Load()},
		{"ToolDeltaOutbox", toolOutboxDrops.Load()},
		{"UserInbox", userInboxDrops.Load()},
		{"UserOutbox", userOutboxDrops.Load()},
		{"KernelDeltaInbox", kernelInboxDrops.Load()},
	}
	for _, c := range checks {
		if c.drops != 1 {
			t.Errorf("%s OnDrop: got %d calls, want 1", c.name, c.drops)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Context cancellation does NOT trigger OnDrop
// ---------------------------------------------------------------------------

// TestBackpressure_ContextCancelDoesNotTriggerOnDrop verifies that when a
// Write fails due to context cancellation (not buffer saturation), the
// OnDrop callback is NOT invoked. This is a critical distinction: OnDrop
// should only fire for capacity-related drops.
func TestBackpressure_ContextCancelDoesNotTriggerOnDrop(t *testing.T) {
	ts := newBackpressureTestEngine()

	var dropCount atomic.Int64
	ts.modelRunner.DeltaOutbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Cancel context before writing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok := ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeTextDelta,
	})
	if ok {
		t.Error("write with cancelled context should fail")
	}
	if dropCount.Load() != 0 {
		t.Errorf("OnDrop should not fire on context cancellation, got %d", dropCount.Load())
	}
}

// ---------------------------------------------------------------------------
// Test: Graceful recovery after saturation
// ---------------------------------------------------------------------------

// TestBackpressure_RecoveryAfterSaturation verifies that after a buffer is
// saturated and drops occur, the system recovers once the consumer drains
// the buffer. New writes succeed and no further OnDrop callbacks fire.
func TestBackpressure_RecoveryAfterSaturation(t *testing.T) {
	ts := newBackpressureTestEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var dropCount atomic.Int64
	ts.modelRunner.DeltaOutbox.SetOnDrop(func() {
		dropCount.Add(1)
	})

	// Phase 1: Saturate the buffer.
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})
	ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("dropped"),
	})

	if dropCount.Load() != 1 {
		t.Fatalf("phase 1: expected 1 drop, got %d", dropCount.Load())
	}

	// Phase 2: Engine ticks (consumes the buffered delta).
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	// Phase 3: Write again — should succeed without OnDrop.
	dropsBefore := dropCount.Load()
	ok := ts.modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("recovered"),
	})
	if !ok {
		t.Error("write after recovery should succeed")
	}
	if dropCount.Load() != dropsBefore {
		t.Error("OnDrop should not fire after recovery")
	}

	// Phase 4: Engine can process the recovered delta.
	if err := ts.engine.TickOnce(ctx); err != nil {
		t.Fatalf("second TickOnce: %v", err)
	}

	state := ts.engine.TickState()
	if state.TickCount != 2 {
		t.Errorf("TickCount: got %d, want 2", state.TickCount)
	}
	if state.DeltaBufferLen != 2 {
		t.Errorf("DeltaBufferLen: got %d, want 2", state.DeltaBufferLen)
	}
}
