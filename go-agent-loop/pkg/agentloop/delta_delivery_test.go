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
