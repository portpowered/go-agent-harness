package engine

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// fullModelTextDeltas returns the delta sequence for a complete assistant text message.
func fullModelTextDeltas(text string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
}

// partialModelTextDeltas returns an interrupted stream: TEXT.START + some TEXT.DELTA, no MESSAGE.END.
func partialModelTextDeltas(parts ...string) []messages.StreamMessage {
	out := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
	}
	for _, p := range parts {
		out = append(out, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue(p),
		})
	}
	return out
}

func TestReconstructModelMessageFromDeltas_PartialInterrupted(t *testing.T) {
	// Partial message: only TEXT.START + a few TEXT.DELTA, no MESSAGE.END (interrupted).
	deltas := partialModelTextDeltas("Hello", " ", "world")
	msg := ReconstructModelMessageFromDeltas(deltas)
	if msg.Role != messages.RoleAssistant {
		t.Errorf("role: got %s", msg.Role)
	}
	text := msg.TextContent()
	if text != "Hello world" {
		t.Errorf("partial text: got %q", text)
	}
	if len(msg.ToolCalls) != 0 {
		t.Errorf("partial: expected no tool calls, got %d", len(msg.ToolCalls))
	}
}

func TestReconstructModelMessageFromDeltas_FullMessage(t *testing.T) {
	const fullText = "Final answer."
	deltas := fullModelTextDeltas(fullText)
	msg := ReconstructModelMessageFromDeltas(deltas)
	if msg.Role != messages.RoleAssistant {
		t.Errorf("role: got %s", msg.Role)
	}
	if msg.TextContent() != fullText {
		t.Errorf("full text: got %q", msg.TextContent())
	}
	if len(msg.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(msg.ToolCalls))
	}
}

func TestReconstructModelMessageFromDeltas_ReasoningAndText(t *testing.T) {
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeReasoningStart, Role: messages.RoleAssistant, Value: messages.NewReasoningStartValue()},
		{Type: messages.StreamTypeReasoningDelta, Role: messages.RoleAssistant, Value: messages.NewReasoningDeltaValue("think")},
		{Type: messages.StreamTypeReasoningEnd, Role: messages.RoleAssistant, Value: messages.NewReasoningEndValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("done")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	msg := ReconstructModelMessageFromDeltas(deltas)
	if msg.TextContent() != "done" {
		t.Errorf("text: got %q", msg.TextContent())
	}
	reasoning := msg.ReasoningContent()
	if reasoning != "<thinking>\nthink\n</thinking>" {
		t.Errorf("reasoning: got %q", reasoning)
	}
}

func TestReconstructModelMessageFromDeltas_ToolCalls(t *testing.T) {
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-1", "get_weather")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-1", "get_weather", `{"city":"NYC"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	msg := ReconstructModelMessageFromDeltas(deltas)
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls: got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call-1" || tc.Name != "get_weather" || tc.Arguments != `{"city":"NYC"}` {
		t.Errorf("tool call: id=%q name=%q args=%q", tc.ID, tc.Name, tc.Arguments)
	}
}

func TestReconstructToolMessagesFromDeltas_FullBatch(t *testing.T) {
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, ToolCallId: "t1", Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, ToolCallId: "t1", Value: messages.NewTextDeltaValue("result1")},
		{Type: messages.StreamTypeTextEnd, ToolCallId: "t1", Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeTextStart, ToolCallId: "t2", Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, ToolCallId: "t2", Value: messages.NewTextDeltaValue("result2")},
		{Type: messages.StreamTypeTextEnd, ToolCallId: "t2", Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	msgs := ReconstructToolMessagesFromDeltas(deltas)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "t1" || msgs[0].TextContent() != "result1" {
		t.Errorf("first: ToolCallID=%q text=%q", msgs[0].ToolCallID, msgs[0].TextContent())
	}
	if msgs[1].ToolCallID != "t2" || msgs[1].TextContent() != "result2" {
		t.Errorf("second: ToolCallID=%q text=%q", msgs[1].ToolCallID, msgs[1].TextContent())
	}
}

func TestReconstructToolMessagesFromDeltas_PartialInterrupted(t *testing.T) {
	// No MESSAGE.END: partial tool results.
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, ToolCallId: "t1", Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, ToolCallId: "t1", Value: messages.NewTextDeltaValue("partial")},
	}
	msgs := ReconstructToolMessagesFromDeltas(deltas)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool message (partial), got %d", len(msgs))
	}
	if msgs[0].TextContent() != "partial" {
		t.Errorf("partial text: got %q", msgs[0].TextContent())
	}
}

func TestReconstructToolMessagesFromDeltas_EmptyDeltas(t *testing.T) {
	msgs := ReconstructToolMessagesFromDeltas(nil)
	if len(msgs) != 0 {
		t.Errorf("nil deltas: got %d messages", len(msgs))
	}
	msgs = ReconstructToolMessagesFromDeltas([]messages.StreamMessage{})
	if len(msgs) != 0 {
		t.Errorf("empty deltas: got %d messages", len(msgs))
	}
}

func TestReconstructModelMessageFromDeltas_EmptyDeltas(t *testing.T) {
	msg := ReconstructModelMessageFromDeltas(nil)
	if msg.Role != messages.RoleAssistant {
		t.Errorf("nil: role %s", msg.Role)
	}
	if msg.TextContent() != "" || len(msg.ToolCalls) != 0 {
		t.Errorf("nil: unexpected content")
	}
	msg = ReconstructModelMessageFromDeltas([]messages.StreamMessage{})
	if msg.TextContent() != "" || len(msg.ToolCalls) != 0 {
		t.Errorf("empty: unexpected content")
	}
}
