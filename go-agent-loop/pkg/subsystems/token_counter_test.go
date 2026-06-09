package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// fakeTokenCounter counts each message as having a fixed number of tokens.
type fakeTokenCounter struct {
	tokensPerMessage int
}

func (f *fakeTokenCounter) Count(msgs []messages.Message) int {
	return len(msgs) * f.tokensPerMessage
}

func makeTextMsg(role messages.Role, text string) messages.Message {
	return messages.NewTextMessage(role, text)
}

func TestTokenCounter_TruncatesOldestFirst(t *testing.T) {
	// 5 messages at 10 tokens each = 50 tokens. Limit 30 means we need to remove 2 oldest.
	counter := &fakeTokenCounter{tokensPerMessage: 10}
	tc := NewTokenCounter(counter, 30)

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = []messages.Message{
		makeTextMsg(messages.RoleUser, "msg1"),
		makeTextMsg(messages.RoleAssistant, "msg2"),
		makeTextMsg(messages.RoleUser, "msg3"),
		makeTextMsg(messages.RoleAssistant, "msg4"),
		makeTextMsg(messages.RoleUser, "msg5"),
	}

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ls.History.ConversationBuffer) != 3 {
		t.Fatalf("expected 3 messages remaining, got %d", len(ls.History.ConversationBuffer))
	}

	// The oldest messages (msg1, msg2) should be removed, keeping msg3, msg4, msg5.
	if ls.History.ConversationBuffer[0].TextContent() != "msg3" {
		t.Errorf("first remaining message: got %q, want %q", ls.History.ConversationBuffer[0].TextContent(), "msg3")
	}
	if ls.History.ConversationBuffer[1].TextContent() != "msg4" {
		t.Errorf("second remaining message: got %q, want %q", ls.History.ConversationBuffer[1].TextContent(), "msg4")
	}
	if ls.History.ConversationBuffer[2].TextContent() != "msg5" {
		t.Errorf("third remaining message: got %q, want %q", ls.History.ConversationBuffer[2].TextContent(), "msg5")
	}
}

func TestTokenCounter_PreservesSystemMessages(t *testing.T) {
	// System message + 3 user/assistant messages. Limit forces removal of oldest non-system.
	counter := &fakeTokenCounter{tokensPerMessage: 10}
	tc := NewTokenCounter(counter, 20) // 40 tokens total, need to remove 2

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = []messages.Message{
		makeTextMsg(messages.RoleSystem, "system prompt"),
		makeTextMsg(messages.RoleUser, "msg1"),
		makeTextMsg(messages.RoleAssistant, "msg2"),
		makeTextMsg(messages.RoleUser, "msg3"),
	}

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ls.History.ConversationBuffer) != 2 {
		t.Fatalf("expected 2 messages remaining, got %d", len(ls.History.ConversationBuffer))
	}

	// System message must be preserved.
	if ls.History.ConversationBuffer[0].Role != messages.RoleSystem {
		t.Errorf("first message should be system, got %s", ls.History.ConversationBuffer[0].Role)
	}
	if ls.History.ConversationBuffer[0].TextContent() != "system prompt" {
		t.Errorf("system message text: got %q, want %q", ls.History.ConversationBuffer[0].TextContent(), "system prompt")
	}
	// msg3 should remain (msg1 and msg2 removed as oldest non-system).
	if ls.History.ConversationBuffer[1].TextContent() != "msg3" {
		t.Errorf("second message: got %q, want %q", ls.History.ConversationBuffer[1].TextContent(), "msg3")
	}
}

func TestTokenCounter_ExactLimit(t *testing.T) {
	// Exactly at the limit: no truncation should occur.
	counter := &fakeTokenCounter{tokensPerMessage: 10}
	tc := NewTokenCounter(counter, 30)

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = []messages.Message{
		makeTextMsg(messages.RoleUser, "msg1"),
		makeTextMsg(messages.RoleAssistant, "msg2"),
		makeTextMsg(messages.RoleUser, "msg3"),
	}

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ls.History.ConversationBuffer) != 3 {
		t.Fatalf("expected 3 messages (at limit), got %d", len(ls.History.ConversationBuffer))
	}
}

func TestTokenCounter_ZeroMessages(t *testing.T) {
	counter := &fakeTokenCounter{tokensPerMessage: 10}
	tc := NewTokenCounter(counter, 30)

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = nil

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ls.History.ConversationBuffer) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(ls.History.ConversationBuffer))
	}
}

func TestTokenCounter_AllSystemMessages(t *testing.T) {
	// All messages are system messages: none should be removed even if over limit.
	counter := &fakeTokenCounter{tokensPerMessage: 10}
	tc := NewTokenCounter(counter, 10) // 30 tokens > 10 limit, but all are system

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = []messages.Message{
		makeTextMsg(messages.RoleSystem, "sys1"),
		makeTextMsg(messages.RoleSystem, "sys2"),
		makeTextMsg(messages.RoleSystem, "sys3"),
	}

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ls.History.ConversationBuffer) != 3 {
		t.Fatalf("expected all 3 system messages preserved, got %d", len(ls.History.ConversationBuffer))
	}
}

func TestTokenCounter_NilCounter(t *testing.T) {
	tc := NewTokenCounter(nil, 100)

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = []messages.Message{
		makeTextMsg(messages.RoleUser, "msg1"),
	}

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a no-op when counter is nil.
	if len(ls.History.ConversationBuffer) != 1 {
		t.Fatalf("expected 1 message (no-op), got %d", len(ls.History.ConversationBuffer))
	}
}

func TestTokenCounter_ZeroMaxTokens(t *testing.T) {
	counter := &fakeTokenCounter{tokensPerMessage: 10}
	tc := NewTokenCounter(counter, 0)

	ls := &state.LoopState{}
	ls.History.ConversationBuffer = []messages.Message{
		makeTextMsg(messages.RoleUser, "msg1"),
	}

	if err := tc.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a no-op when maxTokens is 0.
	if len(ls.History.ConversationBuffer) != 1 {
		t.Fatalf("expected 1 message (no-op), got %d", len(ls.History.ConversationBuffer))
	}
}

func TestTokenCounter_TickGroup(t *testing.T) {
	tc := NewTokenCounter(nil, 0)
	if tc.TickGroup() != TickGroupTokenCounter {
		t.Errorf("TickGroup: got %d, want %d", tc.TickGroup(), TickGroupTokenCounter)
	}
}
