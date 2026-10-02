package agentloop

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestNew_DefaultBufferCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 64 {
		t.Errorf("ModelInbox capacity: got %d, want 64", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 64 {
		t.Errorf("ToolInbox capacity: got %d, want 64", got)
	}
	if got := outputs.UserInbox.Cap(); got != 64 {
		t.Errorf("UserInbox capacity: got %d, want 64", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 64 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 64", got)
	}
}

func TestNew_GlobalBufferCapacityOverride(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(WithInferencer(inf), WithBufferCapacity(128))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 128 {
		t.Errorf("ModelInbox capacity: got %d, want 128", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 128 {
		t.Errorf("ToolInbox capacity: got %d, want 128", got)
	}
	if got := outputs.UserInbox.Cap(); got != 128 {
		t.Errorf("UserInbox capacity: got %d, want 128", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 128 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 128", got)
	}
}

func TestNew_PerParticipantBufferCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithModelBufferCapacity(256),
		WithToolBufferCapacity(32),
		WithUserBufferCapacity(16),
		WithKernelBufferCapacity(512),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 256 {
		t.Errorf("ModelInbox capacity: got %d, want 256", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 32 {
		t.Errorf("ToolInbox capacity: got %d, want 32", got)
	}
	if got := outputs.UserInbox.Cap(); got != 16 {
		t.Errorf("UserInbox capacity: got %d, want 16", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 512 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 512", got)
	}
}

func TestNew_PerParticipantOverridesGlobal(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithBufferCapacity(128),
		WithModelBufferCapacity(256),
		// Tool, User, Kernel not overridden — should use global 128
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	outputs := loop.engine.State().LoopState.Outputs
	if got := outputs.ModelInbox.Cap(); got != 256 {
		t.Errorf("ModelInbox capacity: got %d, want 256 (per-participant override)", got)
	}
	if got := outputs.ToolInbox.Cap(); got != 128 {
		t.Errorf("ToolInbox capacity: got %d, want 128 (global fallback)", got)
	}
	if got := outputs.UserInbox.Cap(); got != 128 {
		t.Errorf("UserInbox capacity: got %d, want 128 (global fallback)", got)
	}
	if got := outputs.KernelDeltaInbox.Cap(); got != 128 {
		t.Errorf("KernelDeltaInbox capacity: got %d, want 128 (global fallback)", got)
	}
}

func TestNew_UserRunnerOutboxCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithUserBufferCapacity(32),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	// UserRunner is accessible via GetUserRunner; verify both Inbox and Outbox.
	userRunner := loop.engine.GetUserRunner()
	if got := userRunner.Inbox.Cap(); got != 32 {
		t.Errorf("UserRunner.Inbox capacity: got %d, want 32", got)
	}
	if got := userRunner.Outbox.Cap(); got != 32 {
		t.Errorf("UserRunner.Outbox capacity: got %d, want 32", got)
	}
}

func TestNew_KernelRunnerDeltaInboxCapacity(t *testing.T) {
	inf := &mockInferencer{
		responses: nil,
	}
	loop, err := New(
		WithInferencer(inf),
		WithKernelBufferCapacity(128),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	kernelRunner := loop.engine.GetKernelRunner()
	if got := kernelRunner.DeltaInbox.Cap(); got != 128 {
		t.Errorf("KernelRunner.DeltaInbox capacity: got %d, want 128", got)
	}
}

// nonStreamingInferencer serves canned results through Infer only, so the
// model runner synthesizes the response's deltas itself.
type nonStreamingInferencer struct{ mockInferencer }

func (*nonStreamingInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, errors.New("streaming unsupported")
}

// countingToolExecutor counts executed calls.
type countingToolExecutor struct{ executed atomic.Int64 }

func (e *countingToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.executed.Add(1)
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: "done"}, nil
}

// TestExecuteStreamingSlowConsumerLosesNoToolCallOrLoopEnd stalls the event
// consumer until the kernel's delta inbox is full at the default capacity,
// then drains it. Every tool call must still execute and the stream must end
// with LOOP.END: a full inbox applies backpressure, it never drops a tool or
// terminal delta.
func TestExecuteStreamingSlowConsumerLosesNoToolCallOrLoopEnd(t *testing.T) {
	// 250 calls produce well over the kernel inbox plus both event buffers.
	const numCalls = 250
	calls := make([]messages.ToolCall, numCalls)
	tools := make([]messages.ToolDefinition, numCalls)
	for i := range numCalls {
		name := fmt.Sprintf("tool_%d", i)
		calls[i] = messages.ToolCall{ID: fmt.Sprintf("call-%d", i), Name: name, Arguments: "{}"}
		tools[i] = messages.ToolDefinition{Name: name}
	}
	inf := &nonStreamingInferencer{mockInferencer{responses: []messages.InferenceResult{
		{Message: messages.Message{Role: messages.RoleAssistant, ToolCalls: calls}, ToolCalls: calls},
		{Message: messages.NewTextMessage(messages.RoleAssistant, "all done")},
	}}}
	executor := &countingToolExecutor{}
	loop, err := New(WithInferencer(inf), WithToolExecutor(executor), WithTools(tools))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("run every tool"))
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	inbox := loop.engine.GetKernelRunner().DeltaInbox
	for inbox.Len() < inbox.Cap() {
		if ctx.Err() != nil {
			t.Fatalf("kernel delta inbox never filled behind the stalled consumer: len=%d cap=%d", inbox.Len(), inbox.Cap())
		}
		runtime.Gosched()
	}

	var last messages.StreamMessage
	toolCallEnds := 0
	for result.EventStream.HasNext() {
		last = result.EventStream.Response()
		if last.Type == messages.StreamTypeToolCallEnd {
			toolCallEnds++
		}
	}
	if last.Type != messages.StreamTypeLoopEnd {
		t.Fatalf("stream ended with %s, want LOOP.END", last.Type)
	}
	if toolCallEnds != numCalls {
		t.Fatalf("stream carried %d tool calls, want %d", toolCallEnds, numCalls)
	}
	if got := executor.executed.Load(); got != numCalls {
		t.Fatalf("executed %d tool calls, want %d", got, numCalls)
	}
}
