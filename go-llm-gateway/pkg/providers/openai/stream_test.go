package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

type streamErrReader struct {
	body []byte
	err  error
}

func (r *streamErrReader) Read(p []byte) (int, error) {
	if len(r.body) > 0 {
		n := copy(p, r.body)
		r.body = r.body[n:]
		return n, nil
	}
	return 0, r.err
}

type streamMockTransport struct {
	statusCode int
	body       string
}

func (t *streamMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// sseData builds an SSE body from a slice of JSON chunk strings.
// Each chunk is written as "data: <json>\n\n"; the final line is "data: [DONE]\n".
func sseData(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n")
	return b.String()
}

// runStream runs streamSSEToGateway on the given SSE body and returns all emitted messages.
func runStream(sse string) []messages.StreamMessage {
	ch := make(chan messages.StreamMessage, 64)
	streamSSEToGateway(strings.NewReader(sse), ch)
	close(ch)
	return collectOpenAIStream(ch)
}

func collectOpenAIStream(ch <-chan messages.StreamMessage) []messages.StreamMessage {
	var out []messages.StreamMessage
	for m := range ch {
		out = append(out, m)
	}
	return out
}

func types(msgs []messages.StreamMessage) []messages.StreamMessageType {
	t := make([]messages.StreamMessageType, len(msgs))
	for i, m := range msgs {
		t[i] = m.Type
	}
	return t
}

func assertTypes(t *testing.T, got []messages.StreamMessage, want []messages.StreamMessageType) {
	t.Helper()
	gt := types(got)
	if len(gt) != len(want) {
		t.Fatalf("got %d events %v, want %d %v", len(gt), gt, len(want), want)
	}
	for i := range want {
		if gt[i] != want[i] {
			t.Errorf("at index %d: got %s, want %s", i, gt[i], want[i])
		}
	}
}

func TestStreamSSEToGateway_EmitsMessageStartAndTextEvents(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	})
}

func TestStreamSSEToGateway_ReaderErrorClassification(t *testing.T) {
	reader := &streamErrReader{
		body: []byte(`data: {"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":""}]}` + "\n\n"),
		err:  errors.New("reader failed"),
	}
	ch := make(chan messages.StreamMessage, 64)
	streamSSEToGateway(reader, ch)
	close(ch)

	msgs := collectOpenAIStream(ch)
	for _, m := range msgs {
		if m.Type != messages.StreamTypeError {
			continue
		}
		v, ok := m.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("ERROR value = %T, want *messages.ErrorValue", m.Value)
		}
		if v.Message != "reader failed" {
			t.Fatalf("ERROR message = %q, want reader failed", v.Message)
		}
		if v.Classification != providers.ErrorClassTransport {
			t.Fatalf("ERROR classification = %q, want %q", v.Classification, providers.ErrorClassTransport)
		}
		return
	}
	t.Fatal("expected ERROR event")
}

func TestOpenAIProvider_InferStream_HTTPErrorClassification(t *testing.T) {
	transport := &streamMockTransport{
		statusCode: http.StatusUnauthorized,
		body:       `{"error":{"message":"missing key"}}`,
	}
	p := New(WithBaseURL("https://mock.openai.test/v1"), WithHTTPClient(&http.Client{Transport: transport}))

	_, err := p.InferStream(context.Background(), providers.InferenceRequest{
		Messages: []models.Message{{Role: models.RoleUser, ContentParts: []models.ContentPart{models.TextPart{Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("InferStream() expected error")
	}
	if !errors.Is(err, providers.ErrProviderRejected) {
		t.Fatalf("InferStream() error = %v, want ErrProviderRejected", err)
	}
	if !errors.Is(err, providers.ErrAuthentication) {
		t.Fatalf("InferStream() error = %v, want ErrAuthentication", err)
	}
}

func TestStreamSSEToGateway_EmitsToolCallEvents(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
	})
}

func TestStreamSSEToGateway_EmitsAudioEvents(t *testing.T) {
	audioB64 := base64.StdEncoding.EncodeToString([]byte("pcm-chunk"))
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-audio","choices":[{"index":0,"delta":{"audio":{"data":"`+audioB64+`"}},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-audio","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeMessageEnd,
	})

	var audioDeltaContent [][]byte
	for _, m := range msgs {
		if m.Type == messages.StreamTypeAudioDelta {
			if v, ok := m.Value.(*messages.AudioDeltaValue); ok {
				audioDeltaContent = append(audioDeltaContent, v.Content)
			}
		}
	}
	if len(audioDeltaContent) != 1 || string(audioDeltaContent[0]) != "pcm-chunk" {
		t.Errorf("expected one AUDIO.DELTA with content 'pcm-chunk', got %v", audioDeltaContent)
	}
}

func TestStreamSSEToGateway_EmitsReasoningEvents(t *testing.T) {
	// delta.reasoning carries thinking tokens (OpenRouter/DeepInfra)
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"glm-4.7","choices":[{"index":0,"delta":{"content":"","reasoning":"The user is "},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"glm-4.7","choices":[{"index":0,"delta":{"content":"4","reasoning":""},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	// REASONING.END is emitted when transitioning to text, not deferred to MESSAGE.END
	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	})

	var reasoningContent []string
	for _, m := range msgs {
		if m.Type == messages.StreamTypeReasoningDelta {
			if v, ok := m.Value.(*messages.ReasoningDeltaValue); ok {
				reasoningContent = append(reasoningContent, v.Content)
			}
		}
	}
	if len(reasoningContent) != 1 || reasoningContent[0] != "The user is " {
		t.Errorf("expected reasoning content [\"The user is \"], got %v", reasoningContent)
	}
}

func TestStreamSSEToGateway_ReasoningToTextTransition(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"glm","choices":[{"index":0,"delta":{"reasoning":"Let me think"},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"glm","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"id":"c3","object":"chat.completion.chunk","created":0,"model":"glm","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	})
}

func TestStreamSSEToGateway_TextToToolCallTransition(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"do_thing","arguments":""}}]},"finish_reason":""}]}`,
		`{"id":"c3","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
	})
}

func TestStreamSSEToGateway_ToolCallToTextTransition(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"do_thing","arguments":""}}]},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":""}]}`,
		`{"id":"c3","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
	})
}

func TestStreamSSEToGateway_ToolCallToReasoningTransition(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"do_thing","arguments":""}}]},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":""}]}`,
		`{"id":"c3","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"","reasoning":"Let me think"},"finish_reason":null}]}`,
		`{"id":"c4","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeMessageEnd,
	})
}

func TestStreamSSEToGateway_UsageInLastChunk(t *testing.T) {
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	)
	// The second chunk (usage-only, no choices) arrives after [DONE] in some implementations,
	// but here we put it before [DONE] to verify it is captured.
	msgs := runStream(sse)

	var usageMsg *messages.StreamMessage
	for i := range msgs {
		if msgs[i].Type == messages.StreamTypeUsageInfo {
			usageMsg = &msgs[i]
			break
		}
	}
	if usageMsg == nil {
		t.Fatal("expected USAGE_INFO event")
	}
	v, ok := usageMsg.Value.(*messages.UsageInfoValue)
	if !ok {
		t.Fatalf("expected *UsageInfoValue, got %T", usageMsg.Value)
	}
	if v.Usage.PromptTokens != 10 || v.Usage.CompletionTokens != 5 || v.Usage.TotalTokens != 15 {
		t.Errorf("unexpected usage: %+v", v.Usage)
	}
}

func TestStreamSSEToGateway_LargeToolCallArguments(t *testing.T) {
	// Build a JSON argument payload >64KB to verify the increased scanner buffer handles it.
	bigValue := strings.Repeat("x", 70*1024) // 70KB string
	argsJSON, _ := json.Marshal(map[string]string{"data": bigValue})
	argsStr := string(argsJSON)

	// Split the arguments across two SSE chunks to mimic real streaming.
	half := len(argsStr) / 2
	firstHalf := argsStr[:half]
	secondHalf := argsStr[half:]

	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_big","type":"function","function":{"name":"big_tool","arguments":"`+escapeJSON(firstHalf)+`"}}]},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"`+escapeJSON(secondHalf)+`"}}]},"finish_reason":"tool_calls"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeMessageEnd,
	})

	// Verify the accumulated arguments match the full payload.
	for _, m := range msgs {
		if m.Type == messages.StreamTypeToolCallEnd {
			v, ok := m.Value.(*messages.ToolCallEndValue)
			if !ok {
				t.Fatalf("expected *ToolCallEndValue, got %T", m.Value)
			}
			if v.Arguments != argsStr {
				t.Errorf("arguments length: got %d, want %d", len(v.Arguments), len(argsStr))
			}
			if v.Name != "big_tool" {
				t.Errorf("tool name: got %q, want %q", v.Name, "big_tool")
			}
			break
		}
	}
}

func TestStreamSSEToGateway_LargeBase64AudioChunk(t *testing.T) {
	// Build a base64-encoded audio payload >64KB to verify the scanner handles large SSE lines.
	rawAudio := make([]byte, 70*1024) // 70KB of audio data
	for i := range rawAudio {
		rawAudio[i] = byte(i % 256)
	}
	audioB64 := base64.StdEncoding.EncodeToString(rawAudio)

	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-audio","choices":[{"index":0,"delta":{"audio":{"data":"`+audioB64+`"}},"finish_reason":""}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-audio","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeMessageEnd,
	})

	// Verify the decoded audio data matches the original.
	for _, m := range msgs {
		if m.Type == messages.StreamTypeAudioDelta {
			v, ok := m.Value.(*messages.AudioDeltaValue)
			if !ok {
				t.Fatalf("expected *AudioDeltaValue, got %T", m.Value)
			}
			if len(v.Content) != len(rawAudio) {
				t.Errorf("audio length: got %d, want %d", len(v.Content), len(rawAudio))
			}
			for i := range rawAudio {
				if v.Content[i] != rawAudio[i] {
					t.Errorf("audio mismatch at byte %d: got %d, want %d", i, v.Content[i], rawAudio[i])
					break
				}
			}
			break
		}
	}
}

func TestStreamSSEToGateway_AccumulatesRefusalDeltas(t *testing.T) {
	// Simulate a refusal delivered across multiple streaming chunks.
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"refusal":"I cannot "},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"refusal":"assist with that."},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	// REFUSAL event is emitted once (accumulated) before MESSAGE.END.
	assertTypes(t, msgs, []messages.StreamMessageType{
		messages.StreamTypeMessageStart,
		messages.StreamTypeRefusal,
		messages.StreamTypeMessageEnd,
	})

	// Verify the refusal text is the full accumulated string.
	var refusalText string
	for _, m := range msgs {
		if m.Type == messages.StreamTypeRefusal {
			v, ok := m.Value.(*messages.RefusalValue)
			if !ok {
				t.Fatalf("expected *RefusalValue, got %T", m.Value)
			}
			refusalText = v.Message
		}
	}
	if refusalText != "I cannot assist with that." {
		t.Errorf("expected refusal %q, got %q", "I cannot assist with that.", refusalText)
	}
}

func TestStreamSSEToGateway_NoRefusalEventWhenEmpty(t *testing.T) {
	// Normal text response should not emit any REFUSAL event.
	sse := sseData(
		`{"id":"c1","object":"chat.completion.chunk","created":0,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":"stop"}]}`,
	)
	msgs := runStream(sse)

	for _, m := range msgs {
		if m.Type == messages.StreamTypeRefusal {
			t.Error("unexpected REFUSAL event in normal text response")
		}
	}
}

// escapeJSON escapes a string for embedding inside a JSON string value.
func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// json.Marshal returns "\"...\"", strip the outer quotes.
	return string(b[1 : len(b)-1])
}
