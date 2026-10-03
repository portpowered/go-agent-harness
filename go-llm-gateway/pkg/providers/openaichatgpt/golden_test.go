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
			request, _, err := buildRequest(tt.req, tt.opts)
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

func translateFixture(body io.Reader, keep replayTarget) <-chan messages.StreamMessage {
	ch := make(chan messages.StreamMessage, providers.StreamMessageBuffer)
	go func() {
		defer close(ch)
		translateStream(body, func() error { return nil }, ch, 0, keep)
	}()
	return ch
}

// TestReasoningReplayMatchesGolden follows one tool step: the reasoning item
// that preceded a function call goes back before that call in the next
// request with its summary and encrypted_content, but without its id,
// status or plain content, as OpenClaw's ChatGPT path replays it
// (replayResponsesItemIds:false,
// openai-responses-replay-messages-internal.ts:460-462). A reasoning item
// without encrypted_content is dropped, as OpenClaw drops bare rs_ ids
// (openai-responses-replay-messages-internal.ts:520-532).
func TestReasoningReplayMatchesGolden(t *testing.T) {
	replay := newReasoningReplay()
	for range translateFixture(bytes.NewReader(readTestdata(t, "stream_reasoning_tool_call.sse")), replayTarget{store: replay}) {
	}
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
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

// TestInterleavedReasoningKeepsItsOrder replays a response in which each
// function call has its own reasoning: the next request must keep the
// original order reasoning1, callA, reasoning2, callB, with no item ids.
func TestInterleavedReasoningKeepsItsOrder(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_a","summary":[],"encrypted_content":"enc-a"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_a","call_id":"call_a","name":"first","arguments":"{}"}}`,
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"reasoning","id":"rs_b","summary":[],"encrypted_content":"enc-b"}}`,
		`data: {"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_b","call_id":"call_b","name":"second","arguments":"{}"}}`,
		`data: {"type":"response.completed","response":{}}`,
	}, "\n\n") + "\n\n"
	replay := newReasoningReplay()
	for range translateFixture(strings.NewReader(stream), replayTarget{store: replay}) {
	}
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
		messages.NewTextMessage(models.RoleUser, "go"),
		{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: "call_a", Name: "first", Arguments: "{}"}, {ID: "call_b", Name: "second", Arguments: "{}"}}},
		{Role: models.RoleTool, ToolCallID: "call_a", ContentParts: []models.ContentPart{models.TextPart{Text: "a"}}},
		{Role: models.RoleTool, ToolCallID: "call_b", ContentParts: []models.ContentPart{models.TextPart{Text: "b"}}},
	}}, requestOptions{model: "gpt-test", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	encoded, err := json.Marshal(request.Input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var input []map[string]any
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	var order []string
	for _, item := range input {
		label := fmt.Sprint(item["type"])
		switch item["type"] {
		case "reasoning":
			if _, hasID := item["id"]; hasID {
				t.Fatalf("replayed reasoning %v carries an id", item)
			}
			label += ":" + fmt.Sprint(item["encrypted_content"])
		case "function_call", "function_call_output":
			label += ":" + fmt.Sprint(item["call_id"])
		}
		order = append(order, label)
	}
	want := "message reasoning:enc-a function_call:call_a reasoning:enc-b function_call:call_b function_call_output:call_a function_call_output:call_b"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("input order = %s\nwant %s", got, want)
	}
}

// answerTurn sends msgs as one request through replay and translates fixture
// as the backend's answer, returning the assistant message the loop
// reconstructs from the stream.
func answerTurn(t *testing.T, replay *reasoningReplay, msgs []models.Message, fixture io.Reader) models.Message {
	t.Helper()
	_, prefix, err := buildRequest(providers.InferenceRequest{Messages: msgs}, requestOptions{model: "gpt-test", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var deltas []messages.StreamMessage
	for msg := range translateFixture(fixture, replayTarget{store: replay, prefix: prefix}) {
		deltas = append(deltas, msg)
	}
	return messages.ReconstructModelMessageFromDeltas(deltas)
}

// TestReasoningReplaysInItsOriginalOrder follows three turns, the way
// OpenClaw replays an assistant turn's blocks in stream order
// (openai-responses-replay-messages-internal.ts, the assistant branch of
// convertResponsesMessagesWithStyle). A response shaped reasoning, message,
// function_call goes back in that order, and the reasoning before a final
// text-only answer goes back before that answer in the next user turn. No
// reasoning carries an id.
func TestReasoningReplaysInItsOriginalOrder(t *testing.T) {
	replay := newReasoningReplay()
	system := messages.NewTextMessage(models.RoleSystem, "Be brief.")
	user := messages.NewTextMessage(models.RoleUser, "weather in Paris?")
	toolStep := answerTurn(t, replay, []models.Message{system, user}, bytes.NewReader(readTestdata(t, "stream_reasoning_text_tool_call.sse")))
	if toolStep.TextContent() != "Let me check." || len(toolStep.ToolCalls) != 1 {
		t.Fatalf("tool step = %+v, want text and one call", toolStep)
	}
	result := models.Message{Role: models.RoleTool, ToolCallID: "call_1", ContentParts: []models.ContentPart{models.TextPart{Text: "sunny, 21C"}}}
	answer := answerTurn(t, replay, []models.Message{system, user, toolStep, result}, bytes.NewReader(readTestdata(t, "stream_reasoning_final_answer.sse")))
	if answer.TextContent() != "It is sunny." {
		t.Fatalf("answer = %q", answer.TextContent())
	}
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
		system, user, toolStep, result, answer,
		messages.NewTextMessage(models.RoleUser, "thanks"),
	}}, requestOptions{model: "gpt-test", sessionID: "session-fixed", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	got, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	assertJSONEqual(t, got, readTestdata(t, "request_reasoning_ordered_replay.json"))
}

// inputLabels renders a request's input as one label per item.
func inputLabels(t *testing.T, input []any) string {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(encoded, &items); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	labels := make([]string, 0, len(items))
	for _, item := range items {
		label := fmt.Sprint(item["type"])
		switch item["type"] {
		case "reasoning":
			label += ":" + fmt.Sprint(item["encrypted_content"])
		case "message":
			label += ":" + fmt.Sprint(item["role"])
		case "function_call", "function_call_output":
			label += ":" + fmt.Sprint(item["call_id"])
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, " ")
}

func textAnswer(encrypted, text string) io.Reader {
	return strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[],"encrypted_content":"` + encrypted + `"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}}`,
		`data: {"type":"response.completed","response":{}}`,
	}, "\n\n") + "\n\n")
}

// TestSameAnswerTwiceKeepsEachTurnsReasoning: two text-only turns that say
// the same words are told apart by the conversation before them.
func TestSameAnswerTwiceKeepsEachTurnsReasoning(t *testing.T) {
	replay := newReasoningReplay()
	first := messages.NewTextMessage(models.RoleUser, "ready?")
	firstAnswer := answerTurn(t, replay, []models.Message{first}, textAnswer("enc-first", "OK."))
	second := messages.NewTextMessage(models.RoleUser, "still ready?")
	secondAnswer := answerTurn(t, replay, []models.Message{first, firstAnswer, second}, textAnswer("enc-second", "OK."))
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
		first, firstAnswer, second, secondAnswer, messages.NewTextMessage(models.RoleUser, "go"),
	}}, requestOptions{model: "gpt-test", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	want := "message:user reasoning:enc-first message:assistant message:user reasoning:enc-second message:assistant message:user"
	if got := inputLabels(t, request.Input); got != want {
		t.Fatalf("input = %s\nwant %s", got, want)
	}
}

// TestConversationsThatDifferOnlyInWhatTheModelSawDoNotShareReasoning: one
// Provider serves several conversations at once (each delegation of a
// session runs its own on the session's backend Provider). Two
// conversations that give the same answer text after different images or
// different instructions must each replay only their own reasoning.
func TestConversationsThatDifferOnlyInWhatTheModelSawDoNotShareReasoning(t *testing.T) {
	ask := func(parts ...models.ContentPart) models.Message {
		return models.Message{Role: models.RoleUser, ContentParts: append([]models.ContentPart{models.TextPart{Text: "what is this?"}}, parts...)}
	}
	system := func(text string) models.Message { return messages.NewTextMessage(models.RoleSystem, text) }
	tests := []struct {
		name          string
		first, second []models.Message
	}{
		{
			name:   "image bytes",
			first:  []models.Message{ask(models.ImagePart{Bytes: []byte{1, 2, 3}, MediaType: "image/png"})},
			second: []models.Message{ask(models.ImagePart{Bytes: []byte{4, 5, 6}, MediaType: "image/png"})},
		},
		{
			name:   "image url",
			first:  []models.Message{ask(models.ImagePart{URL: "https://example.test/cat.jpg"})},
			second: []models.Message{ask(models.ImagePart{URL: "https://example.test/dog.jpg"})},
		},
		{
			name:   "image present or not",
			first:  []models.Message{ask(models.ImagePart{URL: "https://example.test/cat.jpg"})},
			second: []models.Message{ask()},
		},
		{
			name:   "system prompt",
			first:  []models.Message{system("You are a vet."), ask()},
			second: []models.Message{system("You are a zoologist."), ask()},
		},
		{
			name:   "system prompt present or not",
			first:  []models.Message{system("You are a vet."), ask()},
			second: []models.Message{ask()},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replay := newReasoningReplay()
			firstAnswer := answerTurn(t, replay, tt.first, textAnswer("enc-first", "A cat."))
			secondAnswer := answerTurn(t, replay, tt.second, textAnswer("enc-second", "A cat."))
			for _, conversation := range []struct {
				msgs   []models.Message
				answer models.Message
				want   string
			}{
				{tt.first, firstAnswer, "enc-first"},
				{tt.second, secondAnswer, "enc-second"},
			} {
				msgs := append(append([]models.Message(nil), conversation.msgs...), conversation.answer, messages.NewTextMessage(models.RoleUser, "sure?"))
				request, _, err := buildRequest(providers.InferenceRequest{Messages: msgs}, requestOptions{model: "gpt-test", replay: replay})
				if err != nil {
					t.Fatalf("build request: %v", err)
				}
				want := "message:user reasoning:" + conversation.want + " message:assistant message:user"
				if got := inputLabels(t, request.Input); got != want {
					t.Fatalf("input = %s\nwant %s", got, want)
				}
			}
		})
	}
}

// TestChangedHistoryFallsBackToPerCallReasoning: when the history no longer
// matches a kept turn (here its text was edited), a text-only turn goes back
// without reasoning, and a tool turn keeps the reasoning of each call before
// that call, after the text.
func TestChangedHistoryFallsBackToPerCallReasoning(t *testing.T) {
	replay := newReasoningReplay()
	user := messages.NewTextMessage(models.RoleUser, "weather in Paris?")
	toolStep := answerTurn(t, replay, []models.Message{user}, bytes.NewReader(readTestdata(t, "stream_reasoning_text_tool_call.sse")))
	result := models.Message{Role: models.RoleTool, ToolCallID: "call_1", ContentParts: []models.ContentPart{models.TextPart{Text: "sunny, 21C"}}}
	answer := answerTurn(t, replay, []models.Message{user, toolStep, result}, textAnswer("enc-answer", "It is sunny."))
	toolStep.ContentParts = []models.ContentPart{models.TextPart{Text: "Checking."}}
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{user, toolStep, result, answer}}, requestOptions{model: "gpt-test", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	want := "message:user message:assistant reasoning:gAAAAencrypted-1 function_call:call_1 function_call_output:call_1 message:assistant message:user"
	if got := inputLabels(t, request.Input); got != want {
		t.Fatalf("input = %s\nwant %s", got, want)
	}
}

// TestTrailingReasoningStillEndsOnAUserTurn: a turn whose reasoning came
// after its text replays in that order, and the request still gets the
// continuation user message.
func TestTrailingReasoningStillEndsOnAUserTurn(t *testing.T) {
	replay := newReasoningReplay()
	user := messages.NewTextMessage(models.RoleUser, "hi")
	answer := answerTurn(t, replay, []models.Message{user}, strings.NewReader(strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","summary":[],"encrypted_content":"enc-late"}}`,
		`data: {"type":"response.completed","response":{}}`,
	}, "\n\n")+"\n\n"))
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{user, answer}}, requestOptions{model: "gpt-test", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	want := "message:user message:assistant reasoning:enc-late message:user"
	if got := inputLabels(t, request.Input); got != want {
		t.Fatalf("input = %s\nwant %s", got, want)
	}
}

// TestFailedResponseKeepsNoReasoning: only a completed response is kept.
func TestFailedResponseKeepsNoReasoning(t *testing.T) {
	replay := newReasoningReplay()
	user := messages.NewTextMessage(models.RoleUser, "hi")
	answer := answerTurn(t, replay, []models.Message{user}, strings.NewReader(
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[],"encrypted_content":"enc-x"}}`+"\n\n"+
			`data: {"type":"response.output_text.delta","delta":"Hel"}`+"\n\n"))
	request, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{user, answer}}, requestOptions{model: "gpt-test", replay: replay})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got := inputLabels(t, request.Input); got != "message:user message:assistant message:user" {
		t.Fatalf("input = %s, want no reasoning from a stream that never completed", got)
	}
}

func TestReasoningReplayIsBounded(t *testing.T) {
	replay := newReasoningReplay()
	reasoning := replayBlock{kind: replayReasoning, reasoning: json.RawMessage(`{}`)}
	turn := func(i int) (turnLayout, models.Message) {
		id := fmt.Sprintf("call_%d", i)
		return turnLayout{reasoning, {kind: replayCall, callID: id}}, models.Message{Role: models.RoleAssistant, ToolCalls: []models.ToolCall{{ID: id}}}
	}
	for i := range maxReplayedTurns + 1 {
		layout, _ := turn(i)
		replay.remember("", layout)
	}
	_, oldest := turn(0)
	_, newest := turn(maxReplayedTurns)
	if replay.lookup("", oldest) != nil || replay.lookup("", newest) == nil {
		t.Fatal("replay should drop the oldest turn and keep the newest")
	}
	replay.remember("", turnLayout{{kind: replayText, text: "no reasoning"}})
	if replay.lookup("", messages.NewTextMessage(models.RoleAssistant, "no reasoning")) != nil {
		t.Fatal("a turn without reasoning should not be kept")
	}
}

func TestToolResultWithoutCallIDIsRejected(t *testing.T) {
	_, _, err := buildRequest(providers.InferenceRequest{Messages: []models.Message{
		{Role: models.RoleTool, ContentParts: []models.ContentPart{models.TextPart{Text: "orphan"}}},
	}}, requestOptions{model: "gpt-test"})
	if !errors.Is(err, ErrToolResultWithoutCallID) || !errors.Is(err, providers.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrToolResultWithoutCallID and ErrInvalidRequest", err)
	}
}

func TestStreamTranslationMatchesGoldens(t *testing.T) {
	for _, name := range []string{"stream_text", "stream_deltas", "stream_tool_call", "stream_failed_usage_not_included"} {
		t.Run(name, func(t *testing.T) {
			got := renderStream(translateFixture(bytes.NewReader(readTestdata(t, name+".sse")), replayTarget{}))
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
