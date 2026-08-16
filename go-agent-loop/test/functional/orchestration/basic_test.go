package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// Basic request / response
// ---------------------------------------------------------------------------

// TestBasic_SimpleRequestResponse validates the canonical "ask once, get answer"
// flow described in the README quick-start example:
//
//	loop.Execute("what is two plus two?") → "2+2 = 4"
//
// Assertions:
//   - The final text matches the mock response.
//   - The turn produces exactly one message (the assistant reply).
//   - The full conversation history contains the user message followed by the
//     assistant message.
//   - GetConversationDeltas includes MESSAGE.START / TEXT.DELTA events for the
//     user message (verifying the system/user input stream fix), followed by
//     TEXT.DELTA from the model response.
//   - The inferencer was called exactly once.
func TestBasic_SimpleRequestResponse(t *testing.T) {
	const userMessage = "what is two plus two?"
	const modelResponse = "2+2 = 4"

	inf := new(MockInferencer).AddTextResponse(modelResponse)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	result := s.Execute(userMessage)

	// --- final text ---
	if result.Text() != modelResponse {
		t.Errorf("Text(): got %q, want %q", result.Text(), modelResponse)
	}

	// --- turn messages (user message is NOT included in ExecuteResult) ---
	AssertMessages(t, result.Messages, []ExpectedMessage{
		{Role: messages.RoleUser, Text: userMessage},
		{Role: messages.RoleAssistant, Text: modelResponse},
	})

	// --- full conversation history ---
	AssertMessages(t, s.History(), []ExpectedMessage{
		{Role: messages.RoleUser, Text: userMessage},
		{Role: messages.RoleAssistant, Text: modelResponse},
	})

	// --- delta stream (subsequence assertion) ---
	// After the system/user input stream fix, the user message must produce
	// MESSAGE.START → TEXT.DELTA → MESSAGE.END events before the model's
	// TEXT.DELTA arrives.
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, TextContent: userMessage},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser},
		// Model response TEXT.DELTA follows (role=assistant set by MockInferencer).
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: modelResponse},
	})

	// --- inference call count ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestBasic_SimpleRequestResponseWithSystemPrompt validates that a system prompt
// is included in the conversation buffer exactly once, and that its delta events
// appear before the user message events in GetConversationDeltas.
func TestBasic_SimpleRequestResponseWithSystemPrompt(t *testing.T) {
	const systemPrompt = "You are a helpful math assistant."
	const userMessage = "what is two plus two?"
	const modelResponse = "2+2 = 4"

	inf := new(MockInferencer).AddTextResponse(modelResponse)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool, agentloop.WithSystemPrompt(systemPrompt))
	result := s.Execute(userMessage)

	if result.Text() != modelResponse {
		t.Errorf("Text(): got %q, want %q", result.Text(), modelResponse)
	}

	// History: system → user → assistant
	history := s.History()
	AssertMessages(t, history, []ExpectedMessage{
		{Role: messages.RoleSystem, Text: systemPrompt},
		{Role: messages.RoleUser, Text: userMessage},
		{Role: messages.RoleAssistant, Text: modelResponse},
	})

	// System prompt must appear exactly once.
	systemCount := 0
	for _, m := range history {
		if m.Role == messages.RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Errorf("system message count in history: got %d, want 1", systemCount)
	}

	// Delta buffer: system events → user events → model delta.
	// This is the stream ordering described in the README delta-stream diagram.
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleSystem},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleSystem, TextContent: systemPrompt},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleSystem},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, TextContent: userMessage},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: modelResponse},
	})
}

// ---------------------------------------------------------------------------
// Streaming request / response
// ---------------------------------------------------------------------------

// TestBasic_StreamingRequestResponse validates the streaming API described in
// the README streaming-delta example:
//
//	result, _ := loop.ExecuteStreaming(ctx, agentloop.NewExecuteInput("hello"))
//	io.Copy(&buf, result.Stream)
//
// Assertions:
//   - The stream reader delivers the model's full text to EOF.
//   - Messages() (available after stream EOF) contains the assistant message.
//   - The full conversation history contains the user and assistant messages.
//   - GetConversationDeltas has user MESSAGE.START before the model TEXT.DELTA.
//   - The inferencer was called exactly once.
func TestBasic_StreamingRequestResponse(t *testing.T) {
	const userMessage = "hello"
	const modelResponse = "Hello! How can I help?"

	inf := new(MockInferencer).AddTextResponse(modelResponse)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	streamText := s.ExecuteStreamingText(userMessage)

	// --- stream text ---
	if streamText != modelResponse {
		t.Errorf("stream text: got %q, want %q", streamText, modelResponse)
	}

	// --- full conversation history ---
	AssertMessages(t, s.History(), []ExpectedMessage{
		{Role: messages.RoleUser, Text: userMessage},
		{Role: messages.RoleAssistant, Text: modelResponse},
	})

	// --- delta stream ordering ---
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, TextContent: userMessage},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: modelResponse},
	})

	// --- inference call count ---
	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestBasic_StreamingWithChunks validates that the streaming API correctly
// assembles multiple TEXT.DELTA chunks from the inferencer into a single
// contiguous stream, matching the README delta-stream flow where a model
// response arrives as a series of incremental deltas.
func TestBasic_StreamingWithChunks(t *testing.T) {
	const userMessage = "what is the answer?"

	chunks := []string{"The answer", " is ", "42."}
	inf := new(MockInferencer).AddChunkedTextResponse(chunks)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)
	streamText := s.ExecuteStreamingText(userMessage)

	const wantText = "The answer is 42."
	// Stream text may be a prefix of wantText due to kernel delivery order; the
	// authoritative final text is in the collected messages.
	// AssertMessages(t, msgs, []ExpectedMessage{
	// 	{Role: messages.RoleAssistant, Text: wantText},
	// })
	if streamText != wantText {
		t.Errorf("stream text: got %q, want %q", streamText, wantText)
	}

	// All three chunks must appear in the delta buffer in order, preceded by
	// the user message events.
	AssertDeltaContains(t, s.Deltas(), []ExpectedDelta{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleUser},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, TextContent: userMessage},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: "The answer"},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: " is "},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, TextContent: "42."},
	})
}

// ---------------------------------------------------------------------------
// Multi-turn (sequential Execute calls)
// ---------------------------------------------------------------------------

// TestBasic_MultiTurn validates that a second Execute call picks up the
// conversation history from the first turn, so the model receives prior context.
func TestBasic_MultiTurn(t *testing.T) {
	const userMsg1 = "My name is Alice."
	const modelResp1 = "Nice to meet you, Alice!"
	const userMsg2 = "What is my name?"
	const modelResp2 = "Your name is Alice."

	inf := new(MockInferencer).
		AddTextResponse(modelResp1).
		AddTextResponse(modelResp2)
	tool := NewMockToolExecutor()

	s := NewScenario(t, inf, tool)

	r1 := s.Execute(userMsg1)
	if r1.Text() != modelResp1 {
		t.Errorf("turn 1 text: got %q, want %q", r1.Text(), modelResp1)
	}

	r2 := s.Execute(userMsg2)
	if r2.Text() != modelResp2 {
		t.Errorf("turn 2 text: got %q, want %q", r2.Text(), modelResp2)
	}

	// The second inference call must have received the full history including
	// the first turn's assistant response.
	if len(inf.CapturedMessages) < 2 {
		t.Fatalf("expected ≥2 captured message slices, got %d", len(inf.CapturedMessages))
	}
	hasTurn1Response := false
	for _, m := range inf.CapturedMessages[1] {
		if m.Role == messages.RoleAssistant && m.TextContent() == modelResp1 {
			hasTurn1Response = true
			break
		}
	}
	if !hasTurn1Response {
		t.Error("turn 2 inference did not receive turn 1 assistant response in history")
	}

	// Full history: user1 → assistant1 → user2 → assistant2
	AssertMessages(t, s.History(), []ExpectedMessage{
		{Role: messages.RoleUser, Text: userMsg1},
		{Role: messages.RoleAssistant, Text: modelResp1},
		{Role: messages.RoleUser, Text: userMsg2},
		{Role: messages.RoleAssistant, Text: modelResp2},
	})

	if inf.CallCount() != 2 {
		t.Errorf("inference calls: got %d, want 2", inf.CallCount())
	}
}

// ---------------------------------------------------------------------------
// History construction bugs (empty message, duplicate system, tool image content)
// ---------------------------------------------------------------------------

// TestBasic_NoEmptyMessageOnFirstExecution ensures that on first execution we do not
// append an empty message to history. Previously ToMessage() injected a synthetic
// TextPart{Text: ""} when the user sent no content, which showed up as an empty message.
func TestBasic_NoEmptyMessageOnFirstExecution(t *testing.T) {
	const userMessage = "hello"
	const modelResponse = "Hi there."

	inf := new(MockInferencer).AddTextResponse(modelResponse)
	tool := NewMockToolExecutor()
	s := NewScenario(t, inf, tool)
	s.Execute(userMessage)

	history := s.History()
	if len(history) != 2 {
		t.Fatalf("history length: got %d, want 2 (user + assistant)", len(history))
	}
	// No message should be "empty" (single empty text part).
	for i, m := range history {
		if len(m.ContentParts) == 1 {
			if tpart, ok := m.ContentParts[0].(messages.TextPart); ok && tpart.Text == "" {
				t.Errorf("history[%d]: unexpected empty message (single empty TextPart)", i)
			}
		}
	}
}

// TestBasic_EmptyUserInputNoSyntheticEmptyPart ensures that when the user sends
// empty input, we do not store a synthetic empty text part; the user message
// has zero ContentParts instead of one empty TextPart.
func TestBasic_EmptyUserInputNoSyntheticEmptyPart(t *testing.T) {
	const modelResponse = "You said nothing."

	inf := new(MockInferencer).AddTextResponse(modelResponse)
	tool := NewMockToolExecutor()
	s := NewScenario(t, inf, tool)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s.Loop.Execute(ctx, agentloop.ExecuteInput{Message: ""})
	if err != nil {
		t.Fatalf("Execute(empty): %v", err)
	}

	history := s.History()
	// Should have at least user + assistant. User message may have no content.
	var userMsg *messages.Message
	for i := range history {
		if history[i].Role == messages.RoleUser {
			userMsg = &history[i]
			break
		}
	}
	if userMsg != nil && len(userMsg.ContentParts) == 1 {
		if tpart, ok := userMsg.ContentParts[0].(messages.TextPart); ok && tpart.Text == "" {
			t.Error("user message should not contain a synthetic empty TextPart; want zero ContentParts")
		}
	}
}

// TestBasic_ReasoningMessageRecordedInHistory ensures that when the model emits
// REASONING.START / REASONING.DELTA (chunked) / REASONING.END, the reasoning
// content is assembled and recorded in the conversation history (as an assistant
// message with ReasoningPart).
func TestBasic_ReasoningMessageRecordedInHistory(t *testing.T) {
	reasoningChunks := []string{"Let me ", "think step ", "by step."}
	const reasoningContent = "Let me think step by step."
	const textContent = "The answer is 42."

	inf := new(MockInferencer).AddChunkedReasoningThenTextResponse(reasoningChunks, textContent)
	tool := NewMockToolExecutor()
	s := NewScenario(t, inf, tool)
	s.Execute("What is 6*7?")

	history := s.History()
	var foundReasoning bool
	for _, m := range history {
		if m.Role != messages.RoleAssistant {
			continue
		}
		if r := m.ReasoningContent(); r != "" {
			foundReasoning = true
			// Ordering layer wraps with <thinking>\n...\n</thinking>
			if r != "" && !contains(r, reasoningContent) {
				t.Errorf("reasoning message should contain %q, got %q", reasoningContent, r)
			}
			break
		}
	}
	if !foundReasoning {
		t.Error("conversation history should contain an assistant message with reasoning content when model emits REASONING deltas")
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}

// TestBasic_InitialHistoryWithSystemPromptNoDuplicateSystem ensures that when we
// create a loop with InitialHistory that already starts with a system message,
// we do not add the system prompt again (no duplicate system message).
func TestBasic_InitialHistoryWithSystemPromptNoDuplicateSystem(t *testing.T) {
	const systemPrompt = "You are a helpful assistant."
	const userMsg1 = "Say one word."
	const modelResp1 = "OK."

	inf := new(MockInferencer).AddTextResponse(modelResp1)
	tool := NewMockToolExecutor()

	// Initial history already contains the system message (e.g. from a saved session).
	initialHistory := []messages.Message{
		messages.NewTextMessage(messages.RoleSystem, systemPrompt),
		messages.NewTextMessage(messages.RoleUser, userMsg1),
		messages.NewTextMessage(messages.RoleAssistant, modelResp1),
	}

	s := NewScenario(t, inf, tool,
		agentloop.WithSystemPrompt(systemPrompt),
		agentloop.WithInitialHistory(initialHistory),
	)
	s.Execute("Say another word.")

	history := s.History()
	systemCount := 0
	for _, m := range history {
		if m.Role == messages.RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Errorf("system message count: got %d, want 1 (no duplicate when InitialHistory already has system)", systemCount)
	}
}
