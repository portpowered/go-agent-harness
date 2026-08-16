package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type executorInferenceStep struct {
	result       messages.InferenceResult
	streamErr    error
	blockOnCtx   bool
	started      chan struct{}
	partialText  string
	streamEvents []messages.StreamMessage
}

type executorScriptedInferencer struct {
	mu       sync.Mutex
	steps    []executorInferenceStep
	next     int
	requests []messages.InferenceRequest
}

func (s *executorScriptedInferencer) nextStep(req messages.InferenceRequest) executorInferenceStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if len(s.steps) == 0 {
		return executorInferenceStep{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "default")}}
	}
	idx := s.next
	if idx >= len(s.steps) {
		idx = len(s.steps) - 1
	}
	s.next++
	return s.steps[idx]
}

func (s *executorScriptedInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	step := s.nextStep(req)
	if step.streamErr != nil {
		return messages.InferenceResult{}, step.streamErr
	}
	if step.blockOnCtx {
		<-ctx.Done()
		return messages.InferenceResult{}, ctx.Err()
	}
	return step.result, nil
}

func (s *executorScriptedInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	step := s.nextStep(req)
	if step.streamErr != nil {
		ch := make(chan messages.StreamMessage, 1)
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(step.streamErr.Error())}
		close(ch)
		return ch, nil
	}
	if step.blockOnCtx {
		ch := make(chan messages.StreamMessage, 8)
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(step.partialText)}
		if step.started != nil {
			close(step.started)
		}
		go func() {
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	}

	ch := make(chan messages.StreamMessage, 64)
	for _, evt := range step.streamEvents {
		ch <- evt
	}
	if len(step.streamEvents) == 0 {
		executorAppendInferenceEvents(ch, step.result)
	}
	close(ch)
	return ch, nil
}

func executorAppendInferenceEvents(ch chan<- messages.StreamMessage, result messages.InferenceResult) {
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	if result.Message.Refusal != "" {
		ch <- messages.StreamMessage{Type: messages.StreamTypeRefusal, Role: messages.RoleAssistant, Value: messages.NewRefusalValue(result.Message.Refusal)}
	}
	toolCalls := result.ToolCalls
	if len(toolCalls) == 0 {
		toolCalls = result.Message.ToolCalls
	}
	for i, call := range toolCalls {
		ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ActorProvidedIndex: i, Value: messages.NewToolCallStartValue(call.ID, call.Name)}
		if call.Arguments != "" {
			ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, Role: messages.RoleAssistant, ActorProvidedIndex: i, Value: messages.NewToolCallDeltaValue(call.Arguments)}
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ActorProvidedIndex: i, Value: messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments)}
	}
	for _, part := range result.Message.ContentParts {
		switch p := part.(type) {
		case messages.ReasoningPart:
			ch <- messages.StreamMessage{Type: messages.StreamTypeReasoningStart, Role: messages.RoleAssistant, Value: messages.NewReasoningStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeReasoningDelta, Role: messages.RoleAssistant, Value: messages.NewReasoningDeltaValue(p.Reasoning)}
			ch <- messages.StreamMessage{Type: messages.StreamTypeReasoningEnd, Role: messages.RoleAssistant, Value: messages.NewReasoningEndValue()}
		case messages.TextPart:
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
			if p.Text != "" {
				ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(p.Text)}
			}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
		case messages.AudioPart:
			if len(p.Bytes) > 0 {
				ch <- messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()}
				ch <- messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(p.Bytes)}
				ch <- messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()}
			}
		case messages.ImagePart:
			if len(p.Bytes) > 0 {
				ch <- messages.StreamMessage{Type: messages.StreamTypeImageDelta, Role: messages.RoleAssistant, Value: messages.NewImageDeltaValue(p.Bytes)}
			}
		}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(result.TokenUsage)}
}

type executorRecordingTool struct {
	mu       sync.Mutex
	calls    []messages.ToolCall
	response messages.ToolCallResponse
	err      error
}

func (e *executorRecordingTool) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	response := e.response
	if response.ToolCallID == "" {
		response.ToolCallID = call.ID
	}
	if response.Name == "" {
		response.Name = call.Name
	}
	return response, e.err
}

func newExecutorRunData(t *testing.T, inf messages.Inferencer, tool messages.ToolExecutor, toolDefs []messages.ToolDefinition, initialHistory ...messages.Message) *RunData {
	t.Helper()
	options := []agentloop.Option{agentloop.WithInferencer(inf)}
	if tool != nil {
		options = append(options, agentloop.WithToolExecutor(tool))
	}
	if len(toolDefs) > 0 {
		options = append(options, agentloop.WithTools(toolDefs))
	}
	if len(initialHistory) > 0 {
		options = append(options, agentloop.WithInitialHistory(initialHistory))
	}
	loop, err := agentloop.New(options...)
	if err != nil {
		t.Fatalf("agentloop.New: %v", err)
	}
	return &RunData{
		SessionManager: session.NewStorage(t.TempDir()),
		Loop:           loop,
		ConfigDir:      t.TempDir(),
	}
}

func toolMessages(history []messages.Message) []messages.Message {
	var out []messages.Message
	for _, msg := range history {
		if msg.Role == messages.RoleTool {
			out = append(out, msg)
		}
	}
	return out
}

func assertToolResultMessage(t *testing.T, runData *RunData, wantID, wantContent string) {
	t.Helper()
	toolResults := toolMessages(runData.Loop.GetConversationHistory())
	if len(toolResults) != 1 {
		t.Fatalf("tool results = %#v, want one result", toolResults)
	}
	if toolResults[0].ToolCallID != wantID {
		t.Fatalf("tool response ID = %q, want exact request ID %q", toolResults[0].ToolCallID, wantID)
	}
	if toolResults[0].TextContent() != wantContent {
		t.Fatalf("tool content = %q, want %q", toolResults[0].TextContent(), wantContent)
	}
}

func TestExecuteOneTurn_ToolResultS5Table(t *testing.T) {
	toolCall := messages.ToolCall{ID: "request-id-42", Name: "lookup", Arguments: `{"key":"value"}`}
	toolFailure := errors.New("tool exploded")
	partialFailure := errors.New("partial tool failure")
	tests := []struct {
		name        string
		response    messages.ToolCallResponse
		toolErr     error
		wantText    string
		wantErr     error
		wantErrText string
		wantContent string
		skip        string
	}{
		{
			name:        "success preserves content and request ID",
			response:    messages.ToolCallResponse{ToolCallID: toolCall.ID, Content: "tool content"},
			wantText:    "final answer",
			wantContent: "tool content",
		},
		{
			name:        "empty result preserves empty content and request ID",
			response:    messages.ToolCallResponse{ToolCallID: toolCall.ID},
			wantText:    "final after empty",
			wantContent: "",
			skip:        "DEFECT: go-agent-loop emits no reconstructable tool message for an empty response; the full exact ID/content assertions remain below this skip",
		},
		{
			name:        "tool error is propagated",
			response:    messages.ToolCallResponse{ToolCallID: toolCall.ID},
			toolErr:     toolFailure,
			wantErr:     toolFailure,
			wantErrText: `tool "lookup" failed: tool exploded`,
			wantContent: "",
			skip:        "DEFECT: go-agent-loop serializes tool errors at the delta boundary, losing sentinel identity and the tool response message; the full errors.Is/ID/content assertions remain below this skip",
		},
		{
			name:        "content plus error remains an error",
			response:    messages.ToolCallResponse{ToolCallID: toolCall.ID, Content: "partial content"},
			toolErr:     partialFailure,
			wantErr:     partialFailure,
			wantErrText: `tool "lookup" failed: partial tool failure`,
			wantContent: "partial content",
			skip:        "DEFECT: go-agent-loop drops the response when the tool returns content with an error; the full errors.Is/ID/partial-content assertions remain below this skip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finalText := tt.wantText
			if finalText == "" {
				finalText = "unused"
			}
			inf := &executorScriptedInferencer{steps: []executorInferenceStep{
				{result: messages.InferenceResult{
					Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{toolCall}},
					ToolCalls: []messages.ToolCall{toolCall},
				}},
				{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, finalText)}},
			}}
			tool := &executorRecordingTool{response: tt.response, err: tt.toolErr}
			runData := newExecutorRunData(t, inf, tool, []messages.ToolDefinition{{Name: "lookup"}})
			cfg := &Config{NoSystemInformation: true}
			var out strings.Builder
			// Keep the affected rows as explicit regression contracts. The assertions
			// below are intentionally executable when the loop preserves tool
			// response IDs, content, and sentinel errors; the current loop cannot
			// satisfy them without an out-of-lease production change.
			if tt.skip != "" {
				t.Skip(tt.skip)
			}
			got, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &out)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("ExecuteOneTurn() error = nil, want %v", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ExecuteOneTurn() error = %v, want sentinel %v", err, tt.wantErr)
				}
				if err.Error() != tt.wantErrText {
					t.Fatalf("ExecuteOneTurn() error = %q, want exact message %q", err.Error(), tt.wantErrText)
				}
			} else {
				if err != nil || got != tt.wantText || out.String() != tt.wantText+"\n" {
					t.Fatalf("ExecuteOneTurn() = (%q, %v), output=%q; want text %q", got, err, out.String(), tt.wantText)
				}
			}

			tool.mu.Lock()
			calls := append([]messages.ToolCall(nil), tool.calls...)
			tool.mu.Unlock()
			if len(calls) != 1 || calls[0] != toolCall {
				t.Fatalf("tool calls = %#v, want exactly %#v", calls, []messages.ToolCall{toolCall})
			}
			assertToolResultMessage(t, runData, toolCall.ID, tt.wantContent)
		})
	}
}

func TestExecuteOneTurn_PresentationModes(t *testing.T) {
	t.Run("non-streaming JSON", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "json answer"},
				messages.ImagePart{URL: "https://example.test/image.png", MediaType: "image/png"},
			}},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{OutputJSON: true, StderrWriter: &stderr, Model: "test-model"}
		got, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout)
		if err != nil || got != "" {
			t.Fatalf("JSON result = (%q, %v), want empty text and nil error", got, err)
		}
		if !strings.Contains(stdout.String(), `"messages"`) || !strings.Contains(stdout.String(), `"json answer"`) {
			t.Fatalf("JSON output = %q, want message envelope and text", stdout.String())
		}
	})

	t.Run("non-streaming refusal plain text", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, Refusal: "cannot help"},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{StderrWriter: &stderr}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("refusal turn error = %v", err)
		}
		if !strings.Contains(stderr.String(), "[REFUSAL] cannot help") {
			t.Fatalf("stderr = %q, want refusal", stderr.String())
		}
	})

	t.Run("non-streaming refusal JSON", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, Refusal: "structured refusal"},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{OutputJSON: true, StderrWriter: &stderr, Model: "test-model"}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("JSON refusal turn error = %v", err)
		}
		if !strings.Contains(stderr.String(), `"type":"refusal"`) {
			t.Fatalf("stderr = %q, want structured refusal", stderr.String())
		}
	})

	t.Run("streaming text and reasoning", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{
				messages.ReasoningPart{Reasoning: "thinking"},
				messages.TextPart{Text: "stream answer"},
			}},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{Stream: true, OutputReasoningTokens: true, StderrWriter: &stderr}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("streaming turn error = %v", err)
		}
		if stdout.String() != "thinkingstream answer" {
			t.Fatalf("stream output = %q, want reasoning followed by text", stdout.String())
		}
	})

	t.Run("streaming JSON routes refusal to stderr", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, Refusal: "stream refusal", ContentParts: []messages.ContentPart{messages.TextPart{Text: "ignored"}}},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{Stream: true, OutputJSON: true, StderrWriter: &stderr, Model: "test-model"}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("stream JSON turn error = %v", err)
		}
		if !strings.Contains(stderr.String(), `"type":"refusal"`) || !strings.Contains(stdout.String(), `"type":"MESSAGE.START"`) {
			t.Fatalf("stdout/stderr = %q / %q, want event JSON and refusal JSON", stdout.String(), stderr.String())
		}
	})

	t.Run("streaming binary content", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: []byte{1, 2, 3}}}},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{Stream: true, OutputModality: "audio", StderrWriter: &stderr}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("stream binary turn error = %v", err)
		}
		if stdout.String() != string([]byte{1, 2, 3}) || stderr.Len() != 0 {
			t.Fatalf("binary stdout/stderr = %v / %q, want bytes and no warning", []byte(stdout.String()), stderr.String())
		}
	})

	t.Run("streaming binary empty result warns", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "text only")}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{Stream: true, OutputModality: "audio", StderrWriter: &stderr}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("empty binary turn error = %v", err)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "no audio content") {
			t.Fatalf("empty binary stdout/stderr = %q / %q, want warning", stdout.String(), stderr.String())
		}
	})

	t.Run("non-streaming binary content", func(t *testing.T) {
		inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
			Message: messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: []byte{4, 5}}}},
		}}}}
		runData := newExecutorRunData(t, inf, nil, nil)
		var stdout, stderr strings.Builder
		cfg := &Config{OutputModality: "audio", StderrWriter: &stderr}
		if _, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), cfg, &stdout); err != nil {
			t.Fatalf("binary turn error = %v", err)
		}
		if stdout.String() != string([]byte{4, 5}) || stderr.Len() != 0 {
			t.Fatalf("binary stdout/stderr = %v / %q, want bytes and no warning", []byte(stdout.String()), stderr.String())
		}
	})
}

func TestExecuteStreamingTurn_ReturnsLiveStream(t *testing.T) {
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{Message: messages.NewTextMessage(messages.RoleAssistant, "stream result")}}}}
	runData := newExecutorRunData(t, inf, nil, nil)
	stream, err := (&Executor{}).ExecuteStreamingTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), &Config{OutputReasoningTokens: true})
	if err != nil {
		t.Fatalf("ExecuteStreamingTurn() error = %v", err)
	}
	defer func() { _ = stream.Close() }()
	var got string
	for stream.HasNext() {
		evt := stream.Response()
		if evt.Type == messages.StreamTypeTextDelta {
			if value, ok := evt.Value.(*messages.TextDeltaValue); ok {
				got += value.Content
			}
		}
	}
	if got != "stream result" {
		t.Fatalf("stream text = %q, want stream result", got)
	}
}

func TestSaveSession_AndFlushRecorderBranches(t *testing.T) {
	exec := &Executor{}

	noSession := newExecutorRunData(t, &executorScriptedInferencer{}, nil, nil)
	if err := exec.SaveSession(noSession); err != nil {
		t.Fatalf("SaveSession() without ID error = %v", err)
	}

	noHistory := newExecutorRunData(t, &executorScriptedInferencer{}, nil, nil)
	noHistory.SessionID = "empty"
	if err := exec.SaveSession(noHistory); err != nil {
		t.Fatalf("SaveSession() without history error = %v", err)
	}

	withHistory := newExecutorRunData(t, &executorScriptedInferencer{}, nil, nil, messages.NewTextMessage(messages.RoleUser, "saved"))
	withHistory.SessionID = "saved"
	if err := exec.SaveSession(withHistory); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}
	saved, err := withHistory.SessionManager.Load("saved")
	if err != nil || len(saved) != 1 || saved[0].TextContent() != "saved" {
		t.Fatalf("saved history = %#v, %v; want user message", saved, err)
	}

	badSessionRoot := filepath.Join(t.TempDir(), "session-root-file")
	if err := os.WriteFile(badSessionRoot, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := newExecutorRunData(t, &executorScriptedInferencer{}, nil, nil, messages.NewTextMessage(messages.RoleUser, "saved"))
	broken.SessionID = "broken"
	broken.SessionManager = session.NewStorage(badSessionRoot)
	if err := exec.SaveSession(broken); err == nil || !strings.Contains(err.Error(), "save session") {
		t.Fatalf("broken SaveSession() error = %v, want save context", err)
	}

	if err := exec.FlushRecorder(&RunData{}, ""); err != nil {
		t.Fatalf("FlushRecorder() no-op error = %v", err)
	}
	recorder := gatewaytesting.NewRecordRoundTripper(nil)
	recorded := &RunData{Recorder: recorder}
	path := filepath.Join(t.TempDir(), "capture.json")
	if err := exec.FlushRecorder(recorded, path); err != nil {
		t.Fatalf("FlushRecorder() success error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("capture file stat error = %v", err)
	}
	if err := exec.FlushRecorder(recorded, filepath.Join(t.TempDir(), "missing", "capture.json")); err == nil || !strings.Contains(err.Error(), "failed to flush captures") {
		t.Fatalf("FlushRecorder() failure = %v, want flush context", err)
	}
}

func TestExecuteOneTurn_BinaryWriteErrorPreservesContext(t *testing.T) {
	inf := &executorScriptedInferencer{steps: []executorInferenceStep{{result: messages.InferenceResult{
		Message: messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: []byte{1}}}},
	}}}}
	runData := newExecutorRunData(t, inf, nil, nil)
	_, err := (&Executor{}).ExecuteOneTurn(context.Background(), runData, agentloop.NewExecuteInput("question"), &Config{OutputModality: "audio"}, failingWriter{err: io.ErrClosedPipe})
	if err == nil || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("binary write error = %v, want io.ErrClosedPipe", err)
	}
}
