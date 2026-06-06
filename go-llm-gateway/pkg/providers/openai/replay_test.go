package openai

import (
	"bytes"
	"context"
	"encoding/base64"
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

// newTestProvider creates an OpenAI provider with a fixture-based mock transport.
func newTestProvider(transport http.RoundTripper) *OpenAIProvider {
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
	if !strings.Contains(text, "AI assistant") {
		t.Errorf("expected response to contain 'AI assistant', got %q", text)
	}

	if resp.Usage.PromptTokens != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 18 {
		t.Errorf("expected 18 completion tokens, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 28 {
		t.Errorf("expected 28 total tokens, got %d", resp.Usage.TotalTokens)
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

	if resp.Usage.PromptTokens != 30 {
		t.Errorf("expected 30 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 42 {
		t.Errorf("expected 42 total tokens, got %d", resp.Usage.TotalTokens)
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
	if tc.ID != "call_abc123" {
		t.Errorf("expected tool call ID 'call_abc123', got %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got %q", tc.Name)
	}
	if !strings.Contains(tc.Arguments, "New York") {
		t.Errorf("expected arguments to contain 'New York', got %q", tc.Arguments)
	}

	if resp.Usage.PromptTokens != 22 {
		t.Errorf("expected 22 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
}

func TestReplay_ToolCallAndResult(t *testing.T) {
	body := loadFixture(t, "tool_result.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "What's the weather in New York?"),
			{
				Role: models.RoleAssistant,
				ToolCalls: []models.ToolCall{
					{ID: "call_abc123", Name: "get_weather", Arguments: `{"city":"New York"}`},
				},
			},
			{
				Role:       models.RoleTool,
				ToolCallID: "call_abc123",
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

	if resp.Usage.TotalTokens != 63 {
		t.Errorf("expected 63 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestReplay_ImageInput(t *testing.T) {
	body := loadFixture(t, "image_input.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			{
				Role: models.RoleUser,
				ContentParts: []models.ContentPart{
					models.TextPart{Text: "What do you see in this image?"},
					models.ImagePart{URL: "https://example.com/sunset.jpg"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	text := resp.Message.TextContent()
	if !strings.Contains(text, "sunset") {
		t.Errorf("expected response to mention 'sunset', got %q", text)
	}

	if resp.Usage.PromptTokens != 200 {
		t.Errorf("expected 200 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
}

func TestReplay_AudioOutput(t *testing.T) {
	body := loadFixture(t, "audio_output.json")
	p := newTestProvider(newFixtureTransport(200, "application/json", body))

	resp, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Say hello in audio."),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	// Verify text content is present.
	text := resp.Message.TextContent()
	if !strings.Contains(text, "audio response") {
		t.Errorf("expected text to contain 'audio response', got %q", text)
	}

	// Verify audio part was decoded from base64.
	var foundAudio bool
	for _, part := range resp.Message.ContentParts {
		if ap, ok := part.(models.AudioPart); ok {
			foundAudio = true
			expected, _ := base64.StdEncoding.DecodeString("SGVsbG8gV29ybGQ=")
			if string(ap.Bytes) != string(expected) {
				t.Errorf("audio bytes mismatch: got %q, want %q", string(ap.Bytes), string(expected))
			}
			if ap.MediaType != "audio/wav" {
				t.Errorf("expected media type 'audio/wav', got %q", ap.MediaType)
			}
		}
	}
	if !foundAudio {
		t.Error("expected an AudioPart in content parts")
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
	if text != "Hello there! How can I help?" {
		t.Errorf("unexpected accumulated text: %q", text)
	}

	// Verify usage from USAGE_INFO.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.UsageInfoValue); ok {
			if v.Usage.PromptTokens != 8 {
				t.Errorf("expected 8 prompt tokens, got %d", v.Usage.PromptTokens)
			}
			if v.Usage.CompletionTokens != 7 {
				t.Errorf("expected 7 completion tokens, got %d", v.Usage.CompletionTokens)
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
	if text != "Let me check." {
		t.Errorf("unexpected text: %q", text)
	}

	// Verify tool call details.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ToolCallStartValue); ok {
			if v.Name != "get_weather" {
				t.Errorf("expected tool name 'get_weather', got %q", v.Name)
			}
			if v.ToolCallID != "call_weather1" {
				t.Errorf("expected tool call ID 'call_weather1', got %q", v.ToolCallID)
			}
		}
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

func TestReplay_StreamingWithReasoning(t *testing.T) {
	body := loadFixture(t, "stream_reasoning.txt")
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

	// Verify reasoning content.
	var reasoning string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.ReasoningDeltaValue); ok {
			reasoning += v.Content
		}
	}
	if !strings.Contains(reasoning, "analyze") {
		t.Errorf("expected reasoning to contain 'analyze', got %q", reasoning)
	}

	// Verify text content after reasoning.
	var text string
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.TextDeltaValue); ok {
			text += v.Content
		}
	}
	if text != "The answer is 42." {
		t.Errorf("expected text 'The answer is 42.', got %q", text)
	}

	// Verify reasoning tokens in usage.
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.UsageInfoValue); ok {
			if v.Usage.ReasoningTokens != 8 {
				t.Errorf("expected 8 reasoning tokens, got %d", v.Usage.ReasoningTokens)
			}
		}
	}
}

func TestReplay_StreamingAudio(t *testing.T) {
	body := loadFixture(t, "stream_audio.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Say hello in audio."),
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

	msgs := collectStream(ch)
	gotTypes := streamTypes(msgs)

	wantTypes := []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeUsageInfo,
	}
	assertStreamTypes(t, gotTypes, wantTypes)

	// Verify decoded audio chunks.
	var audioData []byte
	for _, m := range msgs {
		if v, ok := m.Value.(*messages.AudioDeltaValue); ok {
			audioData = append(audioData, v.Content...)
		}
	}
	// "SGVsbG8=" decodes to "Hello", "V29ybGQ=" decodes to "World"
	if string(audioData) != "HelloWorld" {
		t.Errorf("expected audio data 'HelloWorld', got %q", string(audioData))
	}
}

// --- SSE edge case tests ---

func TestReplay_StreamingEmptyChoicesChunk(t *testing.T) {
	// Verify that usage-only chunks with empty choices (no content) are handled correctly.
	body := loadFixture(t, "stream_text.txt")
	p := newTestProvider(newFixtureTransport(200, "text/event-stream", body))

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "test"),
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}

	msgs := collectStream(ch)

	// Verify that USAGE_INFO is emitted from the empty-choices usage chunk.
	var hasUsage bool
	for _, m := range msgs {
		if m.Type == messages.StreamTypeUsageInfo {
			hasUsage = true
			v, ok := m.Value.(*messages.UsageInfoValue)
			if !ok {
				t.Fatalf("expected *UsageInfoValue, got %T", m.Value)
			}
			if v.Usage.TotalTokens != 15 {
				t.Errorf("expected 15 total tokens, got %d", v.Usage.TotalTokens)
			}
		}
	}
	if !hasUsage {
		t.Error("expected USAGE_INFO event from empty-choices chunk")
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
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("expected error to contain error type, got: %v", err)
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

func TestReplay_StreamError429_RateLimit(t *testing.T) {
	body := loadFixture(t, "error_429.json")
	p := newTestProvider(newFixtureTransport(429, "application/json", body))

	_, err := p.InferStream(context.Background(), providers.InferenceRequest{
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

// --- No-auth (local provider) tests ---

func TestInfer_NoAPIKey_OmitsAuthorizationHeader(t *testing.T) {
	body := loadFixture(t, "simple_text.json")

	var capturedReq *http.Request
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedReq = req
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})

	p := New(
		WithModel("llama3"),
		WithBaseURL("http://localhost:11434/v1"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)

	_, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Hello"),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected HTTP request to be captured")
	}
	if auth := capturedReq.Header.Get("Authorization"); auth != "" {
		t.Errorf("expected no Authorization header for local provider, got %q", auth)
	}
}

func TestInferStream_NoAPIKey_OmitsAuthorizationHeader(t *testing.T) {
	body := loadFixture(t, "stream_text.txt")

	var capturedReq *http.Request
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedReq = req
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})

	p := New(
		WithModel("llama3"),
		WithBaseURL("http://localhost:11434/v1"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)

	ch, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Hello"),
		},
	})
	if err != nil {
		t.Fatalf("InferStream failed: %v", err)
	}
	// Drain the stream to complete the request
	collectStream(ch)

	if capturedReq == nil {
		t.Fatal("expected HTTP request to be captured")
	}
	if auth := capturedReq.Header.Get("Authorization"); auth != "" {
		t.Errorf("expected no Authorization header for local provider, got %q", auth)
	}
}

func TestInfer_WithAPIKey_IncludesAuthorizationHeader(t *testing.T) {
	body := loadFixture(t, "simple_text.json")

	var capturedReq *http.Request
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedReq = req
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})

	p := New(
		WithAPIKey("sk-test-key"),
		WithModel("gpt-4"),
		WithHTTPClient(&http.Client{Transport: transport}),
	)

	_, err := p.Infer(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "Hello"),
		},
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected HTTP request to be captured")
	}
	if auth := capturedReq.Header.Get("Authorization"); auth != "Bearer sk-test-key" {
		t.Errorf("expected Authorization header 'Bearer sk-test-key', got %q", auth)
	}
}
