package gemini

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

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
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

// newFixtureTransport creates a RoundTripper that returns the given body and status
// for any request matching the Gemini API URL pattern.
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

// newTestProvider creates a Gemini provider with a fixture-based mock transport.
func newTestProvider(transport http.RoundTripper) *GeminiProvider {
	return New(
		WithAPIKey("test-key"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)
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
	if !strings.Contains(text, "Gemini") {
		t.Errorf("expected response to contain 'Gemini', got %q", text)
	}

	if resp.Usage.PromptTokens != 6 {
		t.Errorf("expected 6 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 14 {
		t.Errorf("expected 14 completion tokens, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 20 {
		t.Errorf("expected 20 total tokens, got %d", resp.Usage.TotalTokens)
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
					{Name: "unit", Type: "string", Description: "Temperature unit", Required: false},
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
				ToolCalls: []models.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"New York","unit":"celsius"}`}},
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

func TestReplay_SystemInstruction(t *testing.T) {
	body := loadFixture(t, "system_instruction.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleSystem, "You are a pirate. Always respond in pirate speak."),
			models.NewTextMessage(models.RoleUser, "Hello!"),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	text := resp.Message.TextContent()
	if !strings.Contains(strings.ToLower(text), "pirate") || !strings.Contains(strings.ToLower(text), "matey") {
		t.Errorf("expected pirate-themed response, got %q", text)
	}

	if resp.Usage.PromptTokens != 18 {
		t.Errorf("expected 18 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
}

// --- Streaming InferStream tests ---

func TestReplay_StreamingText(t *testing.T) {
	body := loadFixture(t, "stream_text.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Tell me a sentence."),
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

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

	// Verify accumulated text content.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if text != "The quick brown fox jumps over the lazy dog." {
		t.Errorf("unexpected accumulated text: %q", text)
	}

	// Verify usage from MESSAGE.END.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.MessageEndValue); ok {
			if v.Usage.PromptTokens != 8 || v.Usage.CompletionTokens != 10 {
				t.Errorf("expected usage 8/10, got %d/%d", v.Usage.PromptTokens, v.Usage.CompletionTokens)
			}
		}
	}
}

func TestReplay_StreamingToolCalls(t *testing.T) {
	body := loadFixture(t, "stream_tool_calls.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "What is the weather in London for the next 3 days?"),
		},
		Tools: []models.ToolDefinition{
			{Name: "search_web", Description: "Search the web"},
			{Name: "get_forecast", Description: "Get weather forecast"},
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

	msgs := collectStream(ch)
	gotTypes := types(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		// Text block
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		// First tool call
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		// Second tool call
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		// End
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertTypes(t, gotTypes, wantTypes)

	// Verify text content.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if text != "Let me look that up for you." {
		t.Errorf("unexpected text: %q", text)
	}

	// Verify tool call names.
	var toolNames []string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallStartValue); ok {
			toolNames = append(toolNames, v.Name)
		}
	}
	if len(toolNames) != 2 || toolNames[0] != "search_web" || toolNames[1] != "get_forecast" {
		t.Errorf("expected tool calls [search_web, get_forecast], got %v", toolNames)
	}

	// Verify tool call indices increment.
	var indices []int
	for _, m := range msgs {
		if m.Type == messages.StreamTypeToolCallStart {
			indices = append(indices, m.ActorProvidedIndex)
		}
	}
	if len(indices) != 2 || indices[0] != 0 || indices[1] != 1 {
		t.Errorf("expected tool indices [0, 1], got %v", indices)
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
	if !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Errorf("expected error to contain INVALID_ARGUMENT, got: %v", err)
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
	if !strings.Contains(err.Error(), "UNAUTHENTICATED") {
		t.Errorf("expected error to contain UNAUTHENTICATED, got: %v", err)
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
	if !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
		t.Errorf("expected error to contain RESOURCE_EXHAUSTED, got: %v", err)
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
	if !strings.Contains(err.Error(), "INTERNAL") {
		t.Errorf("expected error to contain INTERNAL, got: %v", err)
	}
}
