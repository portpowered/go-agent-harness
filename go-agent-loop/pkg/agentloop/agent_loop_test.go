package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/test/test_logging"
)

// streamTextFromEventStream drains stream and returns concatenated TEXT.DELTA and REASONING.DELTA content.
func streamTextFromEventStream(stream Stream) string {
	var buf strings.Builder
	for stream.HasNext() {
		evt := stream.Response()
		switch evt.Type {
		case messages.StreamTypeTextDelta:
			if v, ok := evt.Value.(*messages.TextDeltaValue); ok {
				buf.WriteString(v.Content)
			}
		case messages.StreamTypeReasoningDelta:
			if v, ok := evt.Value.(*messages.ReasoningDeltaValue); ok {
				buf.WriteString(v.Content)
			}
		}
	}
	return buf.String()
}

// mockInferencer is a test double that returns canned responses.
type mockInferencer struct {
	responses []messages.InferenceResult
	callCount int
}

func (m *mockInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	idx := m.callCount
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	m.callCount++
	return m.responses[idx], nil
}

func (m *mockInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 16)
	result, err := m.Infer(ctx, req)
	if err != nil {
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, ActorProvidedIndex: 0, Value: messages.NewErrorValue(err.Error())}
		close(ch)
		return ch, nil
	}
	if len(result.ToolCalls) > 0 {
		for i, tc := range result.ToolCalls {
			ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: i, Value: messages.NewToolCallStartValue(tc.ID, tc.Name)}
			if tc.Arguments != "" {
				ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: i, Value: messages.NewToolCallDeltaValue(tc.Arguments)}
			}
			ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: i, Value: messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments)}
		}
	} else {
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
		if text := result.Message.TextContent(); text != "" {
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(text)}
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(ch)
	return ch, nil
}

// mockToolExecutor returns canned tool results.
type mockToolExecutor struct {
	results map[string]string
}

func (m *mockToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	content := m.results[call.Name]
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Content:    content,
	}, nil
}

// failingInferencer is a test double that emits only an ERROR stream message,
// causing the model runner to write InferenceResponse{Error: ...} and RunHotLoop to return that error.
type failingInferencer struct {
	message string
}

func (f *failingInferencer) Infer(ctx context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, errors.New(f.message)
}

func (f *failingInferencer) InferStream(ctx context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 1)
	ch <- messages.StreamMessage{Type: messages.StreamTypeError, ActorProvidedIndex: 0, Value: messages.NewErrorValue(f.message)}
	close(ch)
	return ch, nil
}

func TestExecute_HotLoopError(t *testing.T) {
	wantErr := "inference failed"
	inf := &failingInferencer{message: wantErr}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	result, err := loop.Execute(context.Background(), NewExecuteInput("hi"))
	if err == nil {
		t.Fatal("Execute expected an error when hot loop fails")
	}
	if err.Error() != wantErr {
		t.Errorf("Execute error: got %q, want %q", err.Error(), wantErr)
	}
	if result.Err == nil || result.Err.Error() != wantErr {
		t.Fatalf("ExecuteResult.Err = %v, want %q", result.Err, wantErr)
	}
	final := result.FinalText()
	if final.Status != FinalTextFailed {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextFailed)
	}
}

func TestNew_WithToolsRequiresExplicitToolCapability(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "ok")},
		},
	}

	_, err := New(
		WithInferencer(inf),
		WithTools([]messages.ToolDefinition{{Name: "get_weather", Description: "Get weather"}}),
	)
	if err == nil {
		t.Fatal("expected constructor error when tools are configured without an explicit tool capability")
	}
	if got, want := err.Error(), "tool definitions require WithToolExecutor or WithToolExecutionDisabled"; got != want {
		t.Fatalf("constructor error = %q, want %q", got, want)
	}
}

func TestNew_WithToolExecutionDisabledDropsConfiguredTools(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "ok")},
		},
	}

	loop, err := New(
		WithInferencer(inf),
		WithTools([]messages.ToolDefinition{{Name: "get_weather", Description: "Get weather"}}),
		WithToolExecutionDisabled(),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	if got := loop.engine.State().LoopState.Tools; len(got) != 0 {
		t.Fatalf("loop tools = %d, want 0 when tool execution is explicitly disabled", len(got))
	}
}

func TestExecuteStreaming_HotLoopErrorInStream(t *testing.T) {
	wantErr := "inference failed"
	inf := &failingInferencer{message: wantErr}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("hi"))
	if err != nil {
		t.Fatalf("ExecuteStreaming should not return error: %v", err)
	}

	var gotErr *messages.ErrorValue
	for result.EventStream.HasNext() {
		evt := result.EventStream.Response()
		if evt.Type == messages.StreamTypeError {
			if v, ok := evt.Value.(*messages.ErrorValue); ok {
				gotErr = v
			}
		}
	}
	if gotErr == nil {
		t.Fatal("expected ERROR event in stream when hot loop fails")
	}
	if gotErr.Message != wantErr {
		t.Errorf("stream error message: got %q, want %q", gotErr.Message, wantErr)
	}
	outcome := result.EventStream.Outcome()
	if outcome.Status != StreamFailed {
		t.Fatalf("stream outcome status = %q, want %q", outcome.Status, StreamFailed)
	}
	if outcome.Err == nil || outcome.Err.Error() != wantErr {
		t.Fatalf("stream outcome err = %v, want %q", outcome.Err, wantErr)
	}
}

func TestExecute_SimpleResponse(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "Hello! How can I help?"),
			},
		},
	}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	result, err := loop.Execute(context.Background(), NewExecuteInput("hi"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Text() != "Hello! How can I help?" {
		t.Errorf("expected 'Hello! How can I help?', got %q", result.Text())
	}
}

func TestExecute_EmptyFinalTextResult(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, ""),
			},
		},
	}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	result, err := loop.Execute(context.Background(), NewExecuteInput("hi"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	final := result.FinalText()
	if final.Status != FinalTextEmptySuccess {
		t.Fatalf("FinalText status = %q, want %q", final.Status, FinalTextEmptySuccess)
	}
	if final.Text != "" {
		t.Fatalf("FinalText text = %q, want empty", final.Text)
	}
}

func TestExecute_WithToolCall(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			// First call: model requests a tool call
			{
				Message: messages.Message{
					Role:         messages.RoleAssistant,
					ContentParts: nil,
					ToolCalls: []messages.ToolCall{
						{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`},
					},
				},
				ToolCalls: []messages.ToolCall{
					{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`},
				},
			},
			// Second call: model produces final response with tool results
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "The weather in London is sunny."),
			},
		},
	}

	toolExec := &mockToolExecutor{
		results: map[string]string{
			"get_weather": "sunny, 22C",
		},
	}

	loop, err := New(
		WithInferencer(inf),
		WithToolExecutor(toolExec),
		WithTools([]messages.ToolDefinition{
			{Name: "get_weather", Description: "Get weather for a city"},
		}),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	result, err := loop.Execute(context.Background(), NewExecuteInput("What's the weather in London?"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Text() != "The weather in London is sunny." {
		t.Errorf("unexpected result: %q", result.Text())
	}

	if inf.callCount != 2 {
		t.Errorf("expected 2 inference calls, got %d", inf.callCount)
	}
}

func TestExecute_WithSystemPrompt(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "I am a helpful assistant."),
			},
		},
	}

	loop, err := New(
		WithInferencer(inf),
		WithSystemPrompt("You are a helpful assistant."),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	_, err = loop.Execute(context.Background(), NewExecuteInput("who are you?"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Verify system prompt was added to conversation as the first message.
	msgs := loop.engine.State().LoopState.History.ConversationBuffer
	if len(msgs) < 1 || msgs[0].Role != messages.RoleSystem {
		t.Error("expected first message to be system prompt")
	}

	// Verify the system prompt appears exactly once (not duplicated on repeat calls).
	systemCount := 0
	for _, m := range msgs {
		if m.Role == messages.RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Errorf("expected exactly 1 system message, got %d", systemCount)
	}
}

// TestExecuteStreaming_FullMessagePath verifies that ExecuteStreaming returns an event
// stream that produces the model's response text (non-streaming Infer path used when
// InferStream is not called).
func TestExecuteStreaming_FullMessagePath(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "Streamed response"),
			},
		},
	}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("hello"))
	if err != nil {
		t.Fatalf("ExecuteStreaming failed: %v", err)
	}

	text := streamTextFromEventStream(result.EventStream)
	if text != "Streamed response" {
		t.Errorf("got %q, want %q", text, "Streamed response")
	}
}

// TestExecuteStreaming_EndToEndDeltas verifies the full streaming path end-to-end:
// the model emits multiple TEXT.DELTA chunks via InferStream; we drain the event
// stream and then assert on the final assembled message from Messages() (kernel
// delivery order of deltas vs. message channel can be racy, so we rely on the
// completed turn's messages for the authoritative text).
func TestExecuteStreaming_EndToEndDeltas(t *testing.T) {
	chunks := []string{"The answer", " is ", "42."}

	inf := &chunkInferencer{chunks: chunks}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("what is the answer?"))
	if err != nil {
		t.Fatalf("ExecuteStreaming failed: %v", err)
	}

	// Drain event stream so the turn completes.
	streamText := streamTextFromEventStream(result.EventStream)
	want := strings.Join(chunks, "")
	if streamText != want {
		t.Errorf("got %q, want %q", streamText, want)
	}
}

// chunkInferencer is a test double that emits multiple TEXT.DELTA chunks via InferStream.
type chunkInferencer struct {
	chunks []string
}

func (c *chunkInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, strings.Join(c.chunks, "")),
	}, nil
}

func (c *chunkInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, len(c.chunks)+4)
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
	for i, chunk := range c.chunks {
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: i, Value: messages.NewTextDeltaValue(chunk)}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(ch)
	return ch, nil
}

// TestExecute_MultiTurn verifies that Execute can be called in sequence and that:
//   - The conversation history from the first turn is carried into the second turn.
//   - Each turn correctly handles user request → tool call → tool result → text response.
//   - The system prompt (if any) is not duplicated across turns.
func TestExecute_MultiTurn(t *testing.T) {
	tc1 := messages.ToolCall{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`}
	tc2 := messages.ToolCall{ID: "tc2", Name: "get_weather", Arguments: `{"city":"Paris"}`}

	inf := &capturingInferencer{
		responses: []messages.InferenceResult{
			// Turn 1, call 1: model requests tool call
			{
				Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{tc1}},
				ToolCalls: []messages.ToolCall{tc1},
			},
			// Turn 1, call 2: model produces final response after tool result
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "The weather in London is sunny."),
			},
			// Turn 2, call 1: model requests tool call
			{
				Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{tc2}},
				ToolCalls: []messages.ToolCall{tc2},
			},
			// Turn 2, call 2: model produces final response after tool result
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "The weather in Paris is cloudy."),
			},
		},
	}

	toolExec := &mockToolExecutor{
		results: map[string]string{
			"get_weather": "sunny, 22C",
		},
	}

	loop, err := New(
		WithInferencer(inf),
		WithToolExecutor(toolExec),
		WithTools([]messages.ToolDefinition{
			{Name: "get_weather", Description: "Get weather for a city"},
		}),
		WithSystemPrompt("You are a weather assistant."),
		WithLogger(test_logging.NewPrintLogger()),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// --- First turn ---
	result1, err := loop.Execute(ctx, NewExecuteInput("What's the weather in London?"))
	if err != nil {
		t.Fatalf("first Execute failed: %v", err)
	}
	if result1.Text() != "The weather in London is sunny." {
		t.Errorf("first turn: got %q, want %q", result1.Text(), "The weather in London is sunny.")
	}
	if inf.callCount != 2 {
		t.Errorf("first turn: expected 2 inference calls, got %d", inf.callCount)
	}

	// --- Second turn ---
	result2, err := loop.Execute(ctx, NewExecuteInput("What's the weather in Paris?"))
	if err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
	if result2.Text() != "The weather in Paris is cloudy." {
		t.Errorf("second turn: got %q, want %q", result2.Text(), "The weather in Paris is cloudy.")
	}
	if inf.callCount != 4 {
		t.Errorf("second turn: expected 4 total inference calls, got %d", inf.callCount)
	}

	// The third inference call (first call of turn 2) must include the assistant
	// text response from turn 1 — confirming history is carried forward.
	if len(inf.capturedMessages) < 3 {
		t.Fatalf("expected at least 3 captured message slices, got %d", len(inf.capturedMessages))
	}
	turn2FirstCallMsgs := inf.capturedMessages[2]
	hasTurn1Result := false
	for _, msg := range turn2FirstCallMsgs {
		if msg.Role == messages.RoleAssistant && msg.TextContent() == "The weather in London is sunny." {
			hasTurn1Result = true
			break
		}
	}
	if !hasTurn1Result {
		t.Error("second turn: expected conversation history to include first turn's assistant response")
	}

	// The system prompt must appear exactly once across the full conversation buffer.
	systemCount := 0
	for _, msg := range loop.engine.State().LoopState.History.ConversationBuffer {
		if msg.Role == messages.RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Errorf("expected exactly 1 system message across both turns, got %d", systemCount)
	}
}

func TestNew_RequiresInferencer(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Error("expected error when no inferencer provided")
	}
}

// capturingInferencer records all message slices passed to each inference call
// so tests can assert on what the inferencer received.
type capturingInferencer struct {
	responses        []messages.InferenceResult
	callCount        int
	capturedMessages [][]messages.Message
}

func (c *capturingInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	snapshot := make([]messages.Message, len(req.Messages))
	copy(snapshot, req.Messages)
	c.capturedMessages = append(c.capturedMessages, snapshot)
	idx := c.callCount
	if idx >= len(c.responses) {
		idx = len(c.responses) - 1
	}
	c.callCount++
	return c.responses[idx], nil
}

func (c *capturingInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 32)
	result, err := c.Infer(ctx, req)
	if err != nil {
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(err.Error())}
		close(ch)
		return ch, nil
	}
	// Emit tool calls if present.
	for i, tc := range result.ToolCalls {
		ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: i, Value: messages.NewToolCallStartValue(tc.ID, tc.Name)}
		if tc.Arguments != "" {
			ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: i, Value: messages.NewToolCallDeltaValue(tc.Arguments)}
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: i, Value: messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments)}
	}
	// Emit content parts: text (only when no tool calls), audio (always).
	for _, part := range result.Message.ContentParts {
		switch p := part.(type) {
		case messages.TextPart:
			if len(result.ToolCalls) == 0 && p.Text != "" {
				ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
				ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(p.Text)}
				ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
			}
		case messages.AudioPart:
			if len(p.Bytes) > 0 {
				ch <- messages.StreamMessage{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue()}
				ch <- messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(p.Bytes)}
				ch <- messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()}
			}
		}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(ch)
	return ch, nil
}

// imageToolExecutor returns a single ImagePart as the tool result.
type imageToolExecutor struct {
	imageData []byte
	mediaType string
}

func (e *imageToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{
		ToolCallID:   call.ID,
		ContentParts: []messages.ContentPart{messages.ImagePart{Bytes: e.imageData, MediaType: e.mediaType}},
	}, nil
}

// TestExecute_ToolResultWithImage verifies that when a tool executor returns an
// ImagePart, the engine builds a RoleTool message whose ContentParts contain the
// raw binary image data (not a base64 string), and that message is forwarded to
// the inferencer unchanged for the follow-up call.
func TestExecute_ToolResultWithImage(t *testing.T) {
	// Minimal PNG magic bytes as stand-in for real image data.
	imageData := []byte("\x89PNG\r\n\x1a\n")
	mediaType := "image/png"

	toolCall := messages.ToolCall{ID: "tc1", Name: "screenshot", Arguments: `{}`}
	capturer := &capturingInferencer{
		responses: []messages.InferenceResult{
			// First call: model requests a screenshot.
			{
				Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{toolCall}},
				ToolCalls: []messages.ToolCall{toolCall},
			},
			// Second call: model receives the image result and responds with text.
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "The screenshot shows a desktop."),
			},
		},
	}

	loop, err := New(
		WithInferencer(capturer),
		WithToolExecutor(&imageToolExecutor{imageData: imageData, mediaType: mediaType}),
		WithTools([]messages.ToolDefinition{{Name: "screenshot", Description: "Take a screenshot"}}),
	)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := loop.Execute(ctx, NewExecuteInput("take a screenshot"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.Text() != "The screenshot shows a desktop." {
		t.Errorf("unexpected result: %q", result.Text())
	}

	if len(capturer.capturedMessages) < 2 {
		t.Fatalf("expected at least 2 inference calls, got %d", len(capturer.capturedMessages))
	}

	// The second call must include the tool result message carrying an ImagePart.
	var toolMsg *messages.Message
	for i := range capturer.capturedMessages[1] {
		if capturer.capturedMessages[1][i].Role == messages.RoleTool {
			toolMsg = &capturer.capturedMessages[1][i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("expected a RoleTool message in the second inference call")
	}
	if len(toolMsg.ContentParts) != 1 {
		t.Fatalf("expected 1 content part in tool message, got %d", len(toolMsg.ContentParts))
	}
	imgPart, ok := toolMsg.ContentParts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("expected messages.ImagePart, got %T", toolMsg.ContentParts[0])
	}
	if string(imgPart.Bytes) != string(imageData) {
		t.Errorf("image bytes mismatch: got %v, want %v", imgPart.Bytes, imageData)
	}
	if imgPart.MediaType != mediaType {
		t.Errorf("media type mismatch: got %q, want %q", imgPart.MediaType, mediaType)
	}
}

// TestExecute_ImageInCustomerMessage verifies that a user message pre-seeded with
// an ImagePart (raw bytes + MIME type) is forwarded to the inferencer intact.
// This covers the case where a client embeds image context before calling Execute.
func TestExecute_ImageInCustomerMessage(t *testing.T) {
	imageData := []byte("\x89PNG\r\n\x1a\n")
	mediaType := "image/png"

	capturer := &capturingInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "I see a PNG image.")},
		},
	}

	loop, err := New(WithInferencer(capturer))
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	// Pre-seed the conversation with a user message that carries an inline image.
	loop.engine.State().LoopState.History.ConversationBuffer = append(
		loop.engine.State().LoopState.History.ConversationBuffer,
		messages.Message{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ImagePart{Bytes: imageData, MediaType: mediaType},
				messages.NewTextPart("What do you see in this image?"),
			},
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = loop.Execute(ctx, NewExecuteInput("describe it in one sentence"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(capturer.capturedMessages) == 0 {
		t.Fatal("inferencer was never called")
	}

	// The first inference call must contain a user message with an ImagePart
	// whose binary content matches what was seeded (no encoding applied in memory).
	var foundImg *messages.ImagePart
	for _, msg := range capturer.capturedMessages[0] {
		if msg.Role != messages.RoleUser {
			continue
		}
		for _, part := range msg.ContentParts {
			if ip, ok := part.(messages.ImagePart); ok {
				foundImg = &ip
				break
			}
		}
		if foundImg != nil {
			break
		}
	}
	if foundImg == nil {
		t.Fatal("expected an ImagePart in a RoleUser message passed to the inferencer")
	}
	if string(foundImg.Bytes) != string(imageData) {
		t.Errorf("image bytes mismatch: got %v, want %v", foundImg.Bytes, imageData)
	}
	if foundImg.MediaType != mediaType {
		t.Errorf("media type mismatch: got %q, want %q", foundImg.MediaType, mediaType)
	}
}
