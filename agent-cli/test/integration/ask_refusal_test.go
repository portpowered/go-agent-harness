package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// refusalInferencer returns a response with the Refusal field set (no text content).
type refusalInferencer struct {
	refusalText string
}

func (r *refusalInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.Message{
			Role:    messages.RoleAssistant,
			Refusal: r.refusalText,
		},
	}, nil
}

func (r *refusalInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 8)
	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageStart,
		Role:               messages.RoleAssistant,
		ActorProvidedIndex: 0,
		Value:              messages.NewMessageStartValue(),
	}
	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeRefusal,
		ActorProvidedIndex: 0,
		Value:              messages.NewRefusalValue(r.refusalText),
	}
	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageEnd,
		ActorProvidedIndex: 0,
		Value:              messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	close(ch)
	return ch, nil
}

// TestAskRefusal_NonStreaming verifies that a refusal response in non-streaming
// mode writes [REFUSAL] to stderr and nothing to stdout.
func TestAskRefusal_NonStreaming(t *testing.T) {
	inf := &refusalInferencer{refusalText: "I cannot assist with that."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{"ask", "do something bad"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	// Stdout should not contain refusal text.
	stdout := tw.StdoutString()
	if strings.Contains(stdout, "REFUSAL") || strings.Contains(stdout, "cannot assist") {
		t.Errorf("stdout should not contain refusal text, got %q", stdout)
	}

	// Stderr should contain [REFUSAL] prefix and the refusal message.
	stderr := tw.StderrString()
	if !strings.Contains(stderr, "[REFUSAL]") {
		t.Errorf("stderr should contain [REFUSAL] prefix, got %q", stderr)
	}
	if !strings.Contains(stderr, "I cannot assist with that.") {
		t.Errorf("stderr should contain refusal text, got %q", stderr)
	}
}

// TestAskRefusal_Streaming verifies that a refusal response in streaming mode
// writes [REFUSAL] to stderr and no refusal text to stdout.
func TestAskRefusal_Streaming(t *testing.T) {
	inf := &refusalInferencer{refusalText: "I cannot assist with that."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{"ask", "--stream", "do something bad"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	// Stdout should not contain refusal text.
	stdout := tw.StdoutString()
	if strings.Contains(stdout, "REFUSAL") || strings.Contains(stdout, "cannot assist") {
		t.Errorf("stdout should not contain refusal text, got %q", stdout)
	}

	// Stderr should contain [REFUSAL] prefix and the refusal message.
	stderr := tw.StderrString()
	if !strings.Contains(stderr, "[REFUSAL]") {
		t.Errorf("stderr should contain [REFUSAL] prefix, got %q", stderr)
	}
	if !strings.Contains(stderr, "I cannot assist with that.") {
		t.Errorf("stderr should contain refusal text, got %q", stderr)
	}
}

// TestAskRefusal_NonStreamingJSON verifies that a refusal response in non-streaming
// JSON mode emits a structured refusal JSON event to stderr and not to stdout.
func TestAskRefusal_NonStreamingJSON(t *testing.T) {
	inf := &refusalInferencer{refusalText: "I cannot assist with that."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{"ask", "--output-json", "do something bad"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	// Stdout JSON should not contain refusal events.
	stdout := tw.StdoutString()
	if strings.Contains(stdout, `"type":"refusal"`) {
		t.Errorf("stdout JSON should not contain refusal events, got %q", stdout)
	}

	// Stderr should contain a structured JSON refusal event.
	stderr := tw.StderrString()
	if !strings.Contains(stderr, `"type":"refusal"`) {
		t.Errorf("stderr should contain structured refusal JSON event, got %q", stderr)
	}
	if !strings.Contains(stderr, `"message":"I cannot assist with that."`) {
		t.Errorf("stderr should contain refusal message in JSON, got %q", stderr)
	}
}

// TestAskRefusal_ExitCodeZero verifies that a refusal does not produce a non-zero exit code.
func TestAskRefusal_ExitCodeZero(t *testing.T) {
	inf := &refusalInferencer{refusalText: "I cannot assist with that."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{"ask", "do something bad"})

	// ExecuteContext returns nil for exit code 0; non-nil would indicate a failure.
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Errorf("expected nil error (exit code 0) for refusal, got %v", err)
	}
}

// TestAskRefusal_StreamingJSON verifies that a refusal response in streaming JSON
// mode emits a structured refusal JSON event to stderr.
func TestAskRefusal_StreamingJSON(t *testing.T) {
	inf := &refusalInferencer{refusalText: "I cannot assist with that."}
	exec := &mockToolExecutor{}

	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
	if err != nil {
		t.Fatalf("failed to initialize mock CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{"ask", "--stream", "--output-json", "do something bad"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("failed to execute ask: %v", err)
	}

	// Stdout NDJSON stream should not contain refusal events.
	stdout := tw.StdoutString()
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt["type"] == "REFUSAL" {
			t.Error("stdout NDJSON stream should not contain REFUSAL events")
		}
	}

	// Stderr should contain a structured JSON refusal event.
	stderr := tw.StderrString()
	if !strings.Contains(stderr, `"type":"refusal"`) {
		t.Errorf("stderr should contain structured refusal JSON event, got %q", stderr)
	}
	if !strings.Contains(stderr, `"message":"I cannot assist with that."`) {
		t.Errorf("stderr should contain refusal message in JSON, got %q", stderr)
	}

	// Parse the refusal JSON event from stderr.
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt["type"] == "refusal" {
			if _, ok := evt["timestamp"]; !ok {
				t.Error("refusal JSON event should include a timestamp field")
			}
			return
		}
	}
	t.Error("no structured refusal JSON event found in stderr")
}
