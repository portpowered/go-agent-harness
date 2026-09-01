package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// mockInferencer returns a single canned text response (no tool calls).
type mockInferencer struct {
	response string
}

func (m *mockInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, m.response),
	}, nil
}

func (m *mockInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 8)
	result, _ := m.Infer(ctx, req)
	text := result.Message.TextContent()
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
	if text != "" {
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(text)}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(ch)
	return ch, nil
}

// mockToolExecutor returns empty content for any tool (used when no tool calls are expected).
type mockToolExecutor struct{}

func (m *mockToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: ""}, nil
}

// TestAskWithFakeResponse runs the ask command with a mocked inferencer and executor
// and asserts the output matches the fake response.
func TestAskWithFakeResponse(t *testing.T) {
	fakeResponse := "Fake agent response for testing."

	inf := &mockInferencer{response: fakeResponse}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "hello"})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	actual := strings.TrimSpace(testWriter.StdoutString())
	if actual != fakeResponse {
		t.Errorf("expected output %q, got %q", fakeResponse, actual)
	}
}

// TestAskWithToolCall runs ask with a mock that first requests a tool call, then returns a final message.
func TestAskWithToolCall(t *testing.T) {
	inf := &mockInferencerWithToolCall{
		responses: []messages.InferenceResult{
			{
				Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`}}},
				ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`}},
			},
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "The weather in London is sunny."),
			},
		},
	}
	exec := &mockToolExecutorWithResults{results: map[string]string{"get_weather": "sunny, 22C"}}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "What is the weather in London?"})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	actual := strings.TrimSpace(testWriter.StdoutString())
	expected := "The weather in London is sunny."
	if actual != expected {
		t.Errorf("expected output %q, got %q", expected, actual)
	}
}

type mockInferencerWithToolCall struct {
	responses []messages.InferenceResult
	callCount int
}

func (m *mockInferencerWithToolCall) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	idx := m.callCount
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	m.callCount++
	return m.responses[idx], nil
}

func (m *mockInferencerWithToolCall) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 16)
	result, err := m.Infer(ctx, req)
	if err != nil {
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, ActorProvidedIndex: 0, Value: messages.NewErrorValue(err.Error())}
		close(ch)
		return ch, nil
	}
	// Emit MESSAGE.START so the stream matches the delta protocol (model runner emits it for non-streaming).
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ActorProvidedIndex: 0, Value: messages.NewMessageStartValue()}
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

type mockToolExecutorWithResults struct {
	results map[string]string
}

func (m *mockToolExecutorWithResults) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	content := m.results[call.Name]
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: content}, nil
}

// recordingInferencer records the messages passed to Infer so tests can assert on them.
type recordingInferencer struct {
	response string
	recorded [][]messages.Message
}

func (r *recordingInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	r.recorded = append(r.recorded, append([]messages.Message(nil), req.Messages...))
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, r.response),
	}, nil
}

func (r *recordingInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	result, _ := r.Infer(ctx, req)
	ch := make(chan messages.StreamMessage, 8)
	text := result.Message.TextContent()
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
	if text != "" {
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(text)}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(ch)
	return ch, nil
}

// containsSystemPrompt returns true if any recorded Infer call had a message containing the given substring.
func (r *recordingInferencer) containsSystemPrompt(substring string) bool {
	for _, msgs := range r.recorded {
		for _, m := range msgs {
			if strings.Contains(m.TextContent(), substring) {
				return true
			}
		}
	}
	return false
}

// TestAskLoadsAGENTSMDFromConfigDir runs ask with a temporary config directory that contains
// AGENTS.md and a valid config.yaml, and asserts that the inference request includes the AGENTS.md content.
func TestAskLoadsAGENTSMDFromConfigDir(t *testing.T) {
	const agentsContent = "AGENTS_MD_TEST_MARKER_UNIQUE_STRING_42"
	tmpDir := t.TempDir()

	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	configYAML := `model:
  provider: openrouter
  openrouter:
    model: z-ai/glm-4.7
    api_key: test-key-not-real
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	rec := &recordingInferencer{response: "ok"}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeAgentCLIWithInferencerOverride(exec, rec)
	if err != nil {
		t.Fatalf("failed to initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"ask", "--config-dir", tmpDir, "--workdir", tmpDir, "hello"})

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute ask: %v", err)
	}

	if !rec.containsSystemPrompt(agentsContent) {
		t.Errorf("inference request should contain AGENTS.md content %q; recorded messages did not", agentsContent)
	}
}
