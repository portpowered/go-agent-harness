package anthropic

import (
	"fmt"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// mockStream implements messageStream for testing.
type mockStream struct {
	events []anthropic.MessageStreamEventUnion
	pos    int
	err    error
}

func (m *mockStream) Next() bool {
	if m.pos >= len(m.events) {
		return false
	}
	m.pos++
	return m.pos <= len(m.events)
}

func (m *mockStream) Current() anthropic.MessageStreamEventUnion {
	return m.events[m.pos-1]
}

func (m *mockStream) Err() error {
	return m.err
}

func TestStreamAnthropicToGateway_EmitsMessageStartAndTextEvents(t *testing.T) {
	stream := &mockStream{
		events: []anthropic.MessageStreamEventUnion{
			{Type: "message_start", Message: anthropic.Message{}},
			{Type: "content_block_start", Index: 0, ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{Type: "text"}},
			{Type: "content_block_delta", Index: 0, Delta: anthropic.MessageStreamEventUnionDelta{Type: "text_delta", Text: "Hello"}},
			{Type: "content_block_delta", Index: 0, Delta: anthropic.MessageStreamEventUnionDelta{Type: "text_delta", Text: " world"}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_stop"},
		},
	}
	ch := make(chan messages.StreamMessage, 16)
	streamAnthropicToGateway(stream, ch)

	var types []messages.StreamMessageType
	for m := range ch {
		types = append(types, m.Type)
	}

	expect := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	}
	if len(types) != len(expect) {
		t.Fatalf("got %d events %v, want %d %v", len(types), types, len(expect), expect)
	}
	for i := range expect {
		if types[i] != expect[i] {
			t.Errorf("at index %d: got %s, want %s", i, types[i], expect[i])
		}
	}
}

func TestStreamAnthropicToGateway_EmitsToolCallEvents(t *testing.T) {
	stream := &mockStream{
		events: []anthropic.MessageStreamEventUnion{
			{Type: "message_start", Message: anthropic.Message{}},
			{Type: "content_block_start", Index: 0, ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{Type: "tool_use", ID: "call_1", Name: "get_weather"}},
			{Type: "content_block_delta", Index: 0, Delta: anthropic.MessageStreamEventUnionDelta{Type: "input_json_delta", PartialJSON: `{"city":"NYC"}`}},
			{Type: "content_block_stop", Index: 0},
			{Type: "message_delta", Usage: anthropic.MessageDeltaUsage{InputTokens: 10, OutputTokens: 5}},
			{Type: "message_stop"},
		},
	}
	ch := make(chan messages.StreamMessage, 16)
	streamAnthropicToGateway(stream, ch)

	var types []messages.StreamMessageType
	for m := range ch {
		types = append(types, m.Type)
	}

	expect := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo, // token usage for this response
	}
	if len(types) != len(expect) {
		t.Fatalf("got %d events %v, want %d %v", len(types), types, len(expect), expect)
	}
	for i := range expect {
		if types[i] != expect[i] {
			t.Errorf("at index %d: got %s, want %s", i, types[i], expect[i])
		}
	}
}

func TestStreamAnthropicToGateway_EmitsReasoningEvents(t *testing.T) {
	stream := &mockStream{
		events: []anthropic.MessageStreamEventUnion{
			{Type: "message_start", Message: anthropic.Message{}},
			{Type: "content_block_start", Index: 0, ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{Type: "thinking"}},
			{Type: "content_block_delta", Index: 0, Delta: anthropic.MessageStreamEventUnionDelta{Type: "thinking_delta", Thinking: "Let me think"}},
			{Type: "content_block_stop", Index: 0},
			{Type: "content_block_start", Index: 1, ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{Type: "text"}},
			{Type: "content_block_delta", Index: 1, Delta: anthropic.MessageStreamEventUnionDelta{Type: "text_delta", Text: "Done."}},
			{Type: "content_block_stop", Index: 1},
			{Type: "message_stop"},
		},
	}
	ch := make(chan messages.StreamMessage, 24)
	streamAnthropicToGateway(stream, ch)

	var types []messages.StreamMessageType
	for m := range ch {
		types = append(types, m.Type)
	}

	expect := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	}
	if len(types) != len(expect) {
		t.Fatalf("got %d events %v, want %d %v", len(types), types, len(expect), expect)
	}
	for i := range expect {
		if types[i] != expect[i] {
			t.Errorf("at index %d: got %s, want %s", i, types[i], expect[i])
		}
	}
}

func TestStreamAnthropicToGateway_ErrorBeforeMessageEnd(t *testing.T) {
	stream := &mockStream{
		events: []anthropic.MessageStreamEventUnion{
			{Type: "message_start", Message: anthropic.Message{}},
			{Type: "content_block_start", Index: 0, ContentBlock: anthropic.ContentBlockStartEventContentBlockUnion{Type: "text"}},
			{Type: "content_block_delta", Index: 0, Delta: anthropic.MessageStreamEventUnionDelta{Type: "text_delta", Text: "partial"}},
			// Stream ends abruptly here (no message_stop) — simulates a mid-stream error.
		},
		err: fmt.Errorf("connection reset"),
	}
	ch := make(chan messages.StreamMessage, 16)
	streamAnthropicToGateway(stream, ch)

	var types []messages.StreamMessageType
	for m := range ch {
		types = append(types, m.Type)
	}

	// ERROR must appear before MESSAGE.END so consumers can handle partial responses.
	expect := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeError,
		messages.StreamTypeMessageEnd,
	}
	if len(types) != len(expect) {
		t.Fatalf("got %d events %v, want %d %v", len(types), types, len(expect), expect)
	}
	for i := range expect {
		if types[i] != expect[i] {
			t.Errorf("at index %d: got %s, want %s", i, types[i], expect[i])
		}
	}
}

func TestAccumulateStreamToMessage(t *testing.T) {
	ch := make(chan messages.StreamMessage, 8)
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("Hi")}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(" there")}
	ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("tc1", "foo", `{"x":1}`)}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(ch)

	msg := AccumulateStreamToMessage(ch)
	if msg.TextContent() != "Hi there" {
		t.Errorf("expected content 'Hi there', got %q", msg.TextContent())
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "tc1" || msg.ToolCalls[0].Name != "foo" || msg.ToolCalls[0].Arguments != `{"x":1}` {
		t.Errorf("expected tool call tc1/foo, got %+v", msg.ToolCalls[0])
	}
}
