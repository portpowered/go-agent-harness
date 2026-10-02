package openaichatgpt

// Goldens for the literal request and response shapes of the ChatGPT Codex
// backend, taken from the two reference clients (Codex at 1e6185e522,
// OpenClaw at 4324af41):
//
// Request body (testdata/request_*.json):
//   - codex-rs/core/src/client.rs:927 include ["reasoning.encrypted_content"];
//     :946-962 ResponsesApiRequest with tool_choice "auto", store false,
//     stream true; codex-rs/codex-api/src/common.rs:252-275 field names.
//   - openclaw packages/ai/src/providers/openai-chatgpt-responses.ts:706-745
//     (store false, stream true, instructions with the "You are a helpful
//     assistant." fallback, include, prompt_cache_key; tools with strict false,
//     tool_choice "auto" and parallel_tool_calls true; reasoning
//     {effort, summary: "auto"}).
//   - openclaw packages/ai/src/transports/openai-responses-replay-messages-internal.ts:244-256
//     (input message items), :406 (input_text), :489-497 (assistant
//     output_text with annotations and status completed), :512-514
//     (function_call with call_id), :553-554 (function_call_output).
//   - openclaw packages/ai/src/providers/openai-responses-tools.ts:66-76
//     (function tool shape).
//
// Headers: openai-chatgpt-responses.ts:1586-1610 (Authorization,
// chatgpt-account-id, originator, User-Agent, OpenAI-Beta:
// responses=experimental, accept, content-type, session id and
// x-client-request-id) and codex-rs/codex-api/src/requests/headers.rs:5-13
// (session-id).
//
// Response stream (testdata/stream_*.sse):
//   - stream_text.sse is the fixture of codex-rs/codex-api/src/sse/responses.rs:800-823
//     (two output_item.done messages and response.completed) with the usage
//     object of :866-875.
//   - stream_deltas.sse and stream_tool_call.sse use the event shapes Codex
//     handles at sse/responses.rs:352-506 and OpenClaw at
//     packages/ai/src/transports/openai-responses-stream-internal.ts:346-506
//     (output_item.added, output_text.delta, reasoning_summary_text.delta,
//     function_call_arguments.delta, output_item.done).
//   - stream_failed_usage_not_included.sse is response.failed with the code
//     Codex maps at sse/responses.rs:408-420 and :688-690.
//
// Status mapping: only a 401 means "sign in again"; Codex maps a 403 to
// misalignment_policy_violation or a Cloudflare block
// (codex-rs/codex-api/src/api_bridge.rs:76-82, 207-214) and
// server_is_overloaded/slow_down to ServerOverloaded (:63-73).
//
// Errors: error_usage_limit_reached.json is the body of
// codex-rs/codex-api/src/endpoint/responses_websocket.rs:991-998, read the
// way codex-rs/codex-api/src/api_bridge.rs:118-147 and OpenClaw
// openai-chatgpt-responses.ts:1506-1530 read it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return data
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func weatherTool() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        "get_weather",
		Description: "Look up the weather",
		Parameters:  []messages.ToolParameter{{Name: "city", Type: "string", Description: "City name", Required: true}},
	}
}

func TestRequestBodyMatchesGoldens(t *testing.T) {
	tests := []struct {
		name   string
		golden string
		req    providers.InferenceRequest
		opts   requestOptions
	}{
		{
			name:   "text prompt with system instructions",
			golden: "request_text.json",
			req: providers.InferenceRequest{Messages: []models.Message{
				messages.NewTextMessage(models.RoleSystem, "Be brief."),
				messages.NewTextMessage(models.RoleUser, "hi"),
			}},
			opts: requestOptions{model: "gpt-test", sessionID: "session-fixed"},
		},
		{
			name:   "tool round trip with reasoning effort",
			golden: "request_tool_round_trip.json",
			req: providers.InferenceRequest{
				Messages: []models.Message{
					messages.NewTextMessage(models.RoleUser, "weather in Paris?"),
					{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
					{Role: models.RoleTool, ToolCallID: "call_1", ContentParts: []models.ContentPart{models.TextPart{Text: "sunny, 21C"}}},
					messages.NewTextMessage(models.RoleAssistant, "It is sunny."),
					messages.NewTextMessage(models.RoleUser, "thanks"),
				},
				Tools: []models.ToolDefinition{weatherTool()},
			},
			opts: requestOptions{model: "gpt-test", sessionID: "session-fixed", reasoningEffort: "medium"},
		},
		{
			name:   "image parts and a complete tool schema",
			golden: "request_image_schema.json",
			req: providers.InferenceRequest{
				Messages: []models.Message{{Role: models.RoleUser, ContentParts: []models.ContentPart{
					models.TextPart{Text: "what is this?"},
					models.ImagePart{Bytes: []byte{1, 2, 3}},
					models.ImagePart{URL: "https://example.test/cat.jpg"},
					models.ImagePart{},
				}}},
				Tools: []models.ToolDefinition{
					{Name: "lookup", Description: "Look up a term", ParameterSchema: json.RawMessage(`{"type":"object","properties":{"term":{"type":"string","minLength":1}},"required":["term"]}`)},
					{Name: "noop", Description: "No arguments", ParametersClosed: true, ParameterSchema: json.RawMessage(`{"type":"object"} trailing`)},
				},
			},
			opts: requestOptions{model: "gpt-test", sessionID: "session-fixed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := buildRequest(tt.req, tt.opts)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			got, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			assertJSONEqual(t, got, readTestdata(t, tt.golden))
		})
	}
}

// renderStream prints a stream as one line per message for golden
// comparison.
func renderStream(stream <-chan messages.StreamMessage) string {
	var b strings.Builder
	for msg := range stream {
		b.WriteString(renderMessage(msg))
		b.WriteByte('\n')
	}
	return b.String()
}

func renderMessage(msg messages.StreamMessage) string {
	switch v := msg.Value.(type) {
	case *messages.TextDeltaValue:
		return fmt.Sprintf("%s %q", msg.Type, v.Content)
	case *messages.ReasoningDeltaValue:
		return fmt.Sprintf("%s %q", msg.Type, v.Content)
	case *messages.ToolCallStartValue:
		return fmt.Sprintf("%s[%d] id=%q name=%q", msg.Type, msg.ActorProvidedIndex, v.ToolCallID, v.Name)
	case *messages.ToolCallDeltaValue:
		return fmt.Sprintf("%s[%d] %q", msg.Type, msg.ActorProvidedIndex, v.PartialJSON)
	case *messages.ToolCallEndValue:
		return fmt.Sprintf("%s[%d] id=%q name=%q args=%q", msg.Type, msg.ActorProvidedIndex, v.ToolCallID, v.Name, v.Arguments)
	case *messages.MessageEndValue:
		return fmt.Sprintf("%s %s", msg.Type, renderUsage(v.Usage))
	case *messages.UsageInfoValue:
		return fmt.Sprintf("%s %s", msg.Type, renderUsage(v.Usage))
	case *messages.ErrorValue:
		return fmt.Sprintf("%s %s %q", msg.Type, v.Classification, v.Message)
	case *messages.RefusalValue:
		return fmt.Sprintf("%s %q", msg.Type, v.Message)
	default:
		return string(msg.Type)
	}
}

func renderUsage(usage messages.TokenUsage) string {
	return fmt.Sprintf("prompt=%d completion=%d total=%d reasoning=%d", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.ReasoningTokens)
}

func translateFixture(body io.Reader, replay ...*reasoningReplay) <-chan messages.StreamMessage {
	var keep *reasoningReplay
	if len(replay) > 0 {
		keep = replay[0]
	}
	ch := make(chan messages.StreamMessage, providers.StreamMessageBuffer)
	go func() {
		defer close(ch)
		translateStream(body, func() error { return nil }, ch, 0, keep)
	}()
	return ch
}

// TestReasoningReplayMatchesGolden follows one tool step: the reasoning item
// that preceded a function call (with its encrypted_content, without status
// or plain content, as Codex replays ResponseItem::Reasoning) goes back
// before that call in the next request; a reasoning item without
// encrypted_content is dropped, as OpenClaw drops bare rs_ ids
// (openai-responses-replay-messages-internal.ts:520-532).
func TestReasoningReplayMatchesGolden(t *testing.T) {
	replay := newReasoningReplay()
	for range translateFixture(bytes.NewReader(readTestdata(t, "stream_reasoning_tool_call.sse")), replay) {
	}
	request, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
		messages.NewTextMessage(models.RoleUser, "weather in Paris?"),
		{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
		{Role: models.RoleTool, ToolCallID: "call_1", ContentParts: []models.ContentPart{models.TextPart{Text: "sunny, 21C"}}},
	}}, requestOptions{model: "gpt-test", sessionID: "session-fixed", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	got, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	assertJSONEqual(t, got, readTestdata(t, "request_reasoning_replay.json"))
}

func TestReasoningReplayIsBounded(t *testing.T) {
	replay := newReasoningReplay()
	for i := range maxReplayedCalls + 1 {
		replay.remember(fmt.Sprintf("call_%d", i), []json.RawMessage{json.RawMessage(`{}`)})
	}
	if replay.before("call_0") != nil || replay.before(fmt.Sprintf("call_%d", maxReplayedCalls)) == nil {
		t.Fatal("replay should drop the oldest call and keep the newest")
	}
}

func TestToolResultWithoutCallIDIsRejected(t *testing.T) {
	_, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
		{Role: models.RoleTool, ContentParts: []models.ContentPart{models.TextPart{Text: "orphan"}}},
	}}, requestOptions{model: "gpt-test"})
	if !errors.Is(err, ErrToolResultWithoutCallID) || !errors.Is(err, providers.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrToolResultWithoutCallID and ErrInvalidRequest", err)
	}
}

func TestStreamTranslationMatchesGoldens(t *testing.T) {
	for _, name := range []string{"stream_text", "stream_deltas", "stream_tool_call", "stream_failed_usage_not_included"} {
		t.Run(name, func(t *testing.T) {
			got := renderStream(translateFixture(bytes.NewReader(readTestdata(t, name+".sse"))))
			if want := string(readTestdata(t, name+".golden")); got != want {
				t.Fatalf("stream mismatch\n got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestStatusErrorReadsUsageLimitGolden(t *testing.T) {
	code := readErrorCode(bytes.NewReader(readTestdata(t, "error_usage_limit_reached.json")))
	err := statusError(http.StatusTooManyRequests, code)
	if !errors.Is(err, ErrUsageLimitReached) || !errors.Is(err, providers.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrUsageLimitReached and ErrRateLimited", err)
	}
	if !providers.IsRetryable(err) {
		t.Fatalf("usage limit should be retryable later: %v", err)
	}
	if strings.Contains(err.Error(), "The usage limit has been reached") {
		t.Fatalf("error %q carries response body text", err)
	}
}

func TestDefaultModelFollowsCodexPickerRule(t *testing.T) {
	var body struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(readTestdata(t, "models.json"), &body); err != nil {
		t.Fatalf("decode models golden: %v", err)
	}
	tests := []struct {
		name   string
		models []Model
		want   string
		wantOK bool
	}{
		{name: "lowest-priority listed model", models: body.Models, want: "gpt-test", wantOK: true},
		{name: "no listed model falls back to first", models: []Model{{Slug: "b", Visibility: "hide", Priority: 3}, {Slug: "a", Visibility: "hide", Priority: 1}}, want: "a", wantOK: true},
		{name: "empty list", models: nil, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DefaultModel(tt.models)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DefaultModel = %q, %t; want %q, %t", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
