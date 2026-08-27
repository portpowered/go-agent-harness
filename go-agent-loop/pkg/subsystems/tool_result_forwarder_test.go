package subsystems

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

func forwarderState(mode state.ExecutionMode, results ...messages.Message) *state.LoopState {
	ls := &state.LoopState{Mode: mode}
	ls.Inputs.ToolOutputMessage = append(ls.Inputs.ToolOutputMessage, results...)
	return ls
}

func textResult(id, text string) messages.Message {
	return messages.Message{
		Role:         messages.RoleTool,
		ToolCallID:   id,
		ContentParts: []messages.ContentPart{messages.NewTextPart(text)},
	}
}

func recvToolEnd(t *testing.T, ch <-chan messages.StreamMessage) messages.StreamMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded TOOLCALL.END")
		return messages.StreamMessage{}
	}
}

func assertNoSessionSend(ch chan messages.StreamMessage) bool {
	select {
	case <-ch:
		return false
	case <-time.After(100 * time.Millisecond):
		return true
	}
}

func TestToolResultForwarder_ForwardsEachResultOnceInOrder(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	f := NewToolResultForwarder(ch, nil)
	history := []messages.Message{{
		Role: messages.RoleAssistant,
		ToolCalls: []messages.ToolCall{
			{ID: "tc-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
			{ID: "tc-2", Name: "get_time", Arguments: `{}`},
		},
	}}

	ls := forwarderState(state.DuplexSession,
		textResult("tc-1", `{"forecast":"sunny"}`),
		textResult("tc-2", `{"time":"noon"}`),
	)
	ls.History.ConversationBuffer = history

	ctx := context.Background()
	if err := f.Execute(ctx, ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	first := recvToolEnd(t, ch)
	if first.Type != messages.StreamTypeToolCallEnd {
		t.Fatalf("first forward type = %s, want %s", first.Type, messages.StreamTypeToolCallEnd)
	}
	v1, ok := first.Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("first value = %T, want *messages.ToolCallEndValue", first.Value)
	}
	if v1.ToolCallID != "tc-1" || v1.Name != "get_weather" || v1.Arguments != `{"forecast":"sunny"}` {
		t.Fatalf("first forward = %+v", v1)
	}

	second := recvToolEnd(t, ch)
	v2, ok := second.Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("second value = %T, want *messages.ToolCallEndValue", second.Value)
	}
	if v2.ToolCallID != "tc-2" || v2.Name != "get_time" || v2.Arguments != `{"time":"noon"}` {
		t.Fatalf("second forward = %+v", v2)
	}

	// Re-running ticks over the same loop state must not deliver again.
	ls.Inputs.ToolOutputMessage = append(ls.Inputs.ToolOutputMessage[:0],
		textResult("tc-1", `{"forecast":"sunny"}`), textResult("tc-2", `{"time":"noon"}`))
	if err := f.Execute(ctx, ls); err != nil {
		t.Fatalf("replayed Execute: %v", err)
	}
	if !assertNoSessionSend(ch) {
		t.Fatal("duplicate delivery after replaying the same tool call IDs")
	}
}

func TestToolResultForwarder_EmptyContentStillForwardedOnce(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	f := NewToolResultForwarder(ch, nil)

	ls := forwarderState(state.DuplexSession, messages.Message{
		Role:         messages.RoleTool,
		ToolCallID:   "tc-empty",
		ContentParts: []messages.ContentPart{messages.NewTextPart("")},
	})

	if err := f.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	forwarded := recvToolEnd(t, ch)
	v, ok := forwarded.Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.ToolCallEndValue", forwarded.Value)
	}
	if v.ToolCallID != "tc-empty" || v.Arguments != "" {
		t.Fatalf("forward = %+v, want empty output keeping the call paired", v)
	}
	if !assertNoSessionSend(ch) {
		t.Fatal("empty-content result delivered more than once")
	}
}

func TestToolResultForwarder_ZeroResultsDeliversNothing(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	f := NewToolResultForwarder(ch, nil)

	if err := f.Execute(context.Background(), forwarderState(state.DuplexSession)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !assertNoSessionSend(ch) {
		t.Fatal("zero-result pass delivered a session send")
	}
}

func TestToolResultForwarder_NoOpOutsideDuplexSession(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	f := NewToolResultForwarder(ch, nil)

	for _, mode := range []state.ExecutionMode{state.ModeAskOnce, state.ModeStreaming, state.ModeTurnTaking} {
		if err := f.Execute(context.Background(), forwarderState(mode, textResult("tc-1", "result"))); err != nil {
			t.Fatalf("Execute (mode %d): %v", mode, err)
		}
	}
	if !assertNoSessionSend(ch) {
		t.Fatal("turn-based mode produced a session-sink send")
	}
}

func TestToolResultForwarder_SkipsResultsWithoutCallID(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	f := NewToolResultForwarder(ch, nil)

	ls := forwarderState(state.DuplexSession,
		messages.Message{Role: messages.RoleTool, ContentParts: []messages.ContentPart{messages.NewTextPart("orphan")}},
		textResult("tc-1", "paired"),
	)
	if err := f.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	forwarded := recvToolEnd(t, ch)
	v, ok := forwarded.Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.ToolCallEndValue", forwarded.Value)
	}
	if v.ToolCallID != "tc-1" {
		t.Fatalf("forwarded call id = %q, want tc-1", v.ToolCallID)
	}
	if !assertNoSessionSend(ch) {
		t.Fatal("result without ToolCallID was forwarded and cannot be paired on the wire")
	}
}

func TestToolResultForwarder_NameFallsBackToMessageField(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	f := NewToolResultForwarder(ch, nil)

	result := textResult("tc-9", "out")
	result.Name = "explicit_tool"
	ls := forwarderState(state.DuplexSession, result)

	if err := f.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	v, ok := (<-ch).Value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.ToolCallEndValue", v)
	}
	if v.Name != "explicit_tool" {
		t.Fatalf("name = %q, want explicit_tool", v.Name)
	}
}
