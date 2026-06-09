package anthropic

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// roundTripFunc allows using a function as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// loadFixture reads a fixture file from the testdata directory.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	data, err := os.ReadFile(filepath.Join(dir, "testdata", name))
	if err != nil {
		t.Fatalf("loadFixture(%q): %v", name, err)
	}
	return data
}

// newFixtureTransport creates a RoundTripper that returns the given body and status.
func newFixtureTransport(statusCode int, contentType string, body []byte) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: statusCode,
			Status:     http.StatusText(statusCode),
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})
}

// newTestProvider creates an Anthropic provider with a fixture-based mock transport.
func newTestProvider(transport http.RoundTripper) *AnthropicProvider {
	return New(
		WithAPIKey("test-key"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
}

// collectStream reads all messages from a stream channel.
func collectStream(ch <-chan messages.StreamMessage) []messages.StreamMessage {
	var msgs []messages.StreamMessage
	for m := range ch {
		msgs = append(msgs, m)
	}
	return msgs
}

// streamTypes extracts the types from a list of stream messages.
func streamTypes(msgs []messages.StreamMessage) []messages.StreamMessageType {
	types := make([]messages.StreamMessageType, len(msgs))
	for i, m := range msgs {
		types[i] = m.Type
	}
	return types
}

// assertStreamTypes verifies that stream message types match expected.
func assertStreamTypes(t *testing.T, got, want []messages.StreamMessageType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("stream type count: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("stream type[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// --- Synchronous Infer tests ---

func TestReplay_SimpleTextResponse(t *testing.T) {
	body := loadFixture(t, "simple_text.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Hello, who are you?"),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	if resp.Message.Role != models.RoleAssistant {
		t.Errorf("expected role %q, got %q", models.RoleAssistant, resp.Message.Role)
	}

	text := resp.Message.TextContent()
	if !strings.Contains(text, "Claude") {
		t.Errorf("expected response to contain 'Claude', got %q", text)
	}

	if resp.Usage.PromptTokens != 12 {
		t.Errorf("expected 12 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 24 {
		t.Errorf("expected 24 completion tokens, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 36 {
		t.Errorf("expected 36 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestReplay_MultiTurnConversation(t *testing.T) {
	body := loadFixture(t, "multi_turn.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "My name is Alice."),
			models.NewTextMessage(models.RoleAssistant, "Nice to meet you, Alice!"),
			models.NewTextMessage(models.RoleUser, "What is my name?"),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	text := resp.Message.TextContent()
	if !strings.Contains(text, "Alice") {
		t.Errorf("expected response to reference 'Alice', got %q", text)
	}

	if resp.Usage.PromptTokens != 28 {
		t.Errorf("expected 28 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 47 {
		t.Errorf("expected 47 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestReplay_ToolCall(t *testing.T) {
	body := loadFixture(t, "tool_call.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "What's the weather in New York?"),
		},
		Tools: []models.ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get weather for a city",
				Parameters: []models.ToolParameter{
					{Name: "city", Type: "string", Description: "City name", Required: true},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}

	tc := resp.Message.ToolCalls[0]
	if tc.Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got %q", tc.Name)
	}
	if !strings.Contains(tc.Arguments, "New York") {
		t.Errorf("expected arguments to contain 'New York', got %q", tc.Arguments)
	}

	if resp.Usage.PromptTokens != 25 {
		t.Errorf("expected 25 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
}

func TestReplay_ToolCallAndResult(t *testing.T) {
	body := loadFixture(t, "tool_result.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "What's the weather in New York?"),
			{
				Role:      models.RoleAssistant,
				ToolCalls: []models.ToolCall{{ID: "toolu_01ABC123", Name: "get_weather", Arguments: `{"city":"New York"}`}},
			},
			{
				Role: models.RoleTool,
				Name: "get_weather",
				ContentParts: []models.ContentPart{
					models.TextPart{Text: `{"temperature": 22, "condition": "partly cloudy"}`},
				},
			},
		},
		Tools: []models.ToolDefinition{
			{Name: "get_weather", Description: "Get weather for a city"},
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	text := resp.Message.TextContent()
	if !strings.Contains(text, "22") {
		t.Errorf("expected response to contain temperature '22', got %q", text)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls in final response, got %d", len(resp.Message.ToolCalls))
	}

	if resp.Usage.TotalTokens != 68 {
		t.Errorf("expected 68 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestReplay_LargeResponse(t *testing.T) {
	body := loadFixture(t, "large_response.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Write a long essay about AI history."),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	text := resp.Message.TextContent()
	if len(text) < 500 {
		t.Errorf("expected large response (>500 chars), got %d chars", len(text))
	}
	if resp.Usage.CompletionTokens < 100 {
		t.Errorf("expected >100 completion tokens, got %d", resp.Usage.CompletionTokens)
	}
}

// --- Streaming InferStream tests ---

func TestReplay_StreamingText(t *testing.T) {
	body := loadFixture(t, "stream_text.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Say hello."),
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

	msgs := collectStream(ch)
	gotTypes := streamTypes(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertStreamTypes(t, gotTypes, wantTypes)

	// Verify accumulated text content.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if text != "Hello there! How can I help you today?" {
		t.Errorf("unexpected accumulated text: %q", text)
	}

	// Verify usage from MESSAGE.END.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.MessageEndValue); ok {
			if v.Usage.CompletionTokens != 12 {
				t.Errorf("expected 12 completion tokens, got %d", v.Usage.CompletionTokens)
			}
		}
	}
}

func TestReplay_StreamingToolCalls(t *testing.T) {
	body := loadFixture(t, "stream_tool_calls.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "What's the weather?"),
		},
		Tools: []models.ToolDefinition{
			{Name: "get_weather", Description: "Get weather"},
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

	msgs := collectStream(ch)
	gotTypes := streamTypes(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertStreamTypes(t, gotTypes, wantTypes)

	// Verify text content.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if text != "Let me check the weather for you." {
		t.Errorf("unexpected text: %q", text)
	}

	// Verify tool call name.
	var toolNames []string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallStartValue); ok {
			toolNames = append(toolNames, v.Name)
		}
	}
	if len(toolNames) != 1 || toolNames[0] != "get_weather" {
		t.Errorf("expected tool call [get_weather], got %v", toolNames)
	}

	// Verify accumulated tool call arguments.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallEndValue); ok {
			if !strings.Contains(v.Arguments, "New York") {
				t.Errorf("expected tool arguments to contain 'New York', got %q", v.Arguments)
			}
		}
	}
}

func TestReplay_StreamingWithThinking(t *testing.T) {
	body := loadFixture(t, "stream_thinking.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Think about this carefully."),
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

	msgs := collectStream(ch)
	gotTypes := streamTypes(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertStreamTypes(t, gotTypes, wantTypes)

	// Verify reasoning content was captured.
	var reasoning string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ReasoningDeltaValue); ok {
			reasoning += v.Content
		}
	}
	if !strings.Contains(reasoning, "analyze") {
		t.Errorf("expected reasoning to contain 'analyze', got %q", reasoning)
	}

	// Verify text content after thinking.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if !strings.Contains(text, "After careful consideration") {
		t.Errorf("expected text to contain 'After careful consideration', got %q", text)
	}
}

// --- Error response tests ---

func TestReplay_Error400_BadRequest(t *testing.T) {
	body := loadFixture(t, "error_400.json")
	p := newTestProvider(newFixtureTransport(400, "application/json", body))

	_, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "test"),
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to contain status code 400, got: %v", err)
	}
}

func TestReplay_Error401_Unauthorized(t *testing.T) {
	body := loadFixture(t, "error_401.json")
	p := newTestProvider(newFixtureTransport(401, "application/json", body))

	_, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "test"),
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to contain status code 401, got: %v", err)
	}
}

func TestReplay_Error429_RateLimit(t *testing.T) {
	body := loadFixture(t, "error_429.json")
	p := newTestProvider(newFixtureTransport(429, "application/json", body))

	_, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "test"),
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected error to contain status code 429, got: %v", err)
	}
}

func TestReplay_Error500_InternalServer(t *testing.T) {
	body := loadFixture(t, "error_500.json")
	p := newTestProvider(newFixtureTransport(500, "application/json", body))

	_, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "test"),
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to contain status code 500, got: %v", err)
	}
}
