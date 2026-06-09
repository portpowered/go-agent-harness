package gemini

import (
	"fmt"
	"testing"

	"google.golang.org/genai"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// mockIter builds a streamIterator from a slice of responses and an optional final error.
func mockIter(responses []*genai.GenerateContentResponse, err error) streamIterator {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, resp := range responses {
			if !yield(resp, nil) {
				return
			}
		}
		if err != nil {
			yield(nil, err)
		}
	}
}

// collectStream drains the channel and returns all messages.
func collectStream(ch <-chan messages.StreamMessage) []messages.StreamMessage {
	var out []messages.StreamMessage
	for m := range ch {
		out = append(out, m)
	}
	return out
}

// types extracts StreamMessageTypes from a slice of StreamMessages.
func types(msgs []messages.StreamMessage) []messages.StreamMessageType {
	out := make([]messages.StreamMessageType, len(msgs))
	for i, m := range msgs {
		out[i] = m.Type
	}
	return out
}

// assertTypes checks that the given types match expectations.
func assertTypes(t *testing.T, got, want []messages.StreamMessageType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("at index %d: got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestStreamGeminiToGateway_TextStream(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello"}}}}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: " world"}}}}}},
		{
			Candidates:    []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "!"}}}}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5},
		},
	}, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertTypes(t, gotTypes, wantTypes)

	// Verify text delta content.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if text != "Hello world!" {
		t.Errorf("expected accumulated text 'Hello world!', got %q", text)
	}

	// Verify usage.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.MessageEndValue); ok {
			if v.Usage.PromptTokens != 10 || v.Usage.CompletionTokens != 5 || v.Usage.TotalTokens != 15 {
				t.Errorf("expected usage 10/5/15, got %d/%d/%d", v.Usage.PromptTokens, v.Usage.CompletionTokens, v.Usage.TotalTokens)
			}
		}
	}
}

func TestStreamGeminiToGateway_ToolCallStream(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{
			Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{
					ID:   "call_1",
					Name: "get_weather",
					Args: map[string]any{"city": "NYC"},
				}},
			}}}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 20, CandidatesTokenCount: 8},
		},
	}, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertTypes(t, gotTypes, wantTypes)

	// Verify tool call start.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallStartValue); ok {
			if v.ToolCallID != "call_1" || v.Name != "get_weather" {
				t.Errorf("expected tool call call_1/get_weather, got %s/%s", v.ToolCallID, v.Name)
			}
		}
	}

	// Verify tool call end has full arguments.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallEndValue); ok {
			if v.ToolCallID != "call_1" || v.Name != "get_weather" || v.Arguments != `{"city":"NYC"}` {
				t.Errorf("expected tool call end call_1/get_weather with args, got %+v", v)
			}
		}
	}
}

func TestStreamGeminiToGateway_TextThenToolCall(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "Let me check"}}}}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "tc1", Name: "search", Args: map[string]any{"q": "test"}}},
		}}}}},
	}, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd, // text block ends before tool call
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
	}
	assertTypes(t, gotTypes, wantTypes)
}

func TestStreamGeminiToGateway_MultipleToolCalls(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "tc1", Name: "tool_a", Args: map[string]any{"x": float64(1)}}},
			{FunctionCall: &genai.FunctionCall{ID: "tc2", Name: "tool_b", Args: map[string]any{"y": float64(2)}}},
		}}}}},
	}, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
	}
	assertTypes(t, gotTypes, wantTypes)

	// Verify indices increment.
	var toolStarts []int
	for _, m := range msgs {
		if m.Type == messages.StreamTypeToolCallStart {
			toolStarts = append(toolStarts, m.ActorProvidedIndex)
		}
	}
	if len(toolStarts) != 2 || toolStarts[0] != 0 || toolStarts[1] != 1 {
		t.Errorf("expected tool call indices [0, 1], got %v", toolStarts)
	}
}

func TestStreamGeminiToGateway_ErrorBeforeMessageEnd(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "partial"}}}}}},
	}, fmt.Errorf("connection reset"))

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	// ERROR must appear before MESSAGE.END.
	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeError,
		messages.StreamTypeMessageEnd,
	}
	assertTypes(t, gotTypes, wantTypes)

	// Verify error message.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ErrorValue); ok {
			if v.Message != "connection reset" {
				t.Errorf("expected error 'connection reset', got %q", v.Message)
			}
		}
	}
}

func TestStreamGeminiToGateway_ProviderErrorClassification(t *testing.T) {
	providerErr := providers.NewProviderHTTPError("gemini", 429, "quota exhausted")
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "partial"}}}}}},
	}, providerErr)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	for _, m := range msgs {
		if m.Type != messages.StreamTypeError {
			continue
		}
		v, ok := m.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("ERROR value = %T, want *messages.ErrorValue", m.Value)
		}
		if v.Message != providerErr.Error() {
			t.Fatalf("ERROR message = %q, want %q", v.Message, providerErr.Error())
		}
		if v.Classification != providers.ErrorClassRateLimited {
			t.Fatalf("ERROR classification = %q, want %q", v.Classification, providers.ErrorClassRateLimited)
		}
		return
	}
	t.Fatal("expected ERROR event")
}

func TestStreamGeminiToGateway_EmptyStream(t *testing.T) {
	iter := mockIter(nil, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	// Even an empty stream should produce MESSAGE.START and MESSAGE.END.
	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeMessageEnd,
	}
	assertTypes(t, gotTypes, wantTypes)
}

func TestStreamGeminiToGateway_EmptyCandidates(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: nil},
		{Candidates: []*genai.Candidate{{Content: nil}}},
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "hi"}}}}}},
	}, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	}
	assertTypes(t, gotTypes, wantTypes)
}

func TestStreamGeminiToGateway_ToolCallWithNilArgs(t *testing.T) {
	iter := mockIter([]*genai.GenerateContentResponse{
		{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "tc1", Name: "no_args", Args: nil}},
		}}}}},
	}, nil)

	ch := make(chan messages.StreamMessage, 32)
	streamGeminiToGateway(iter, ch)

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	// nil args marshals to "null" which is skipped, so no TOOLCALL.DELTA.
	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
	}
	assertTypes(t, gotTypes, wantTypes)

	// Verify tool call end has "null" args.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallEndValue); ok {
			if v.Arguments != "null" {
				t.Errorf("expected arguments 'null', got %q", v.Arguments)
			}
		}
	}
}
