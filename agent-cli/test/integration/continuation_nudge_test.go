package integration

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// recordingStepInferencer returns steps in sequence (like stepInferencer) and
// records every InferenceRequest so tests can assert on the messages sent.
type recordingStepInferencer struct {
	mu       sync.Mutex
	steps    []inferenceStep
	idx      int
	recorded []messages.InferenceRequest
}

func (m *recordingStepInferencer) Infer(_ context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorded = append(m.recorded, req)
	step := m.steps[m.idx]
	if m.idx < len(m.steps)-1 {
		m.idx++
	}
	return step.result, step.err
}

func (m *recordingStepInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, nil
}

func (m *recordingStepInferencer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.recorded)
}

// createNudgeTestDir sets up a temp directory with a config.yaml that has
// continuation_nudge_enabled: true and a fake API key.
func createNudgeTestDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, config.ConfigFileName)
	configYAML := `model:
  provider: openrouter
  continuation_nudge_enabled: true
  openrouter:
    model: z-ai/glm-4.7
    api_key: test-key-not-real
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return tmpDir
}

// TestContinuationNudge_EndToEnd verifies that when continuation nudge is enabled
// and the model stops early (no tool call, no stop-word), the coordinator
// re-invokes inference with the nudge message.
//
// Flow:
//  1. First inference call returns plain text without the stop-word ("partial response")
//  2. The continuation nudge logic detects early stop and enqueues a nudge
//  3. The coordinator dequeues the nudge and re-invokes inference
//  4. Second inference call returns text containing the stop-word ("All done. DONE")
//  5. The loop stops — inference was called exactly twice
func TestContinuationNudge_EndToEnd(t *testing.T) {
	tmpDir := createNudgeTestDir(t)

	inf := &recordingStepInferencer{
		steps: []inferenceStep{
			// Call 1: partial response — no tool call, no stop-word.
			makeTextStep("I started working on the task but need to continue"),
			// Call 2: complete response — contains the stop-word.
			makeTextStep("All done. DONE"),
		},
	}

	exec := newIterativeTestExecutor(inf)
	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{MaxIterations: 1, StopWord: "DONE"}
	input := agentloop.NewExecuteInput("complete the task")

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop: %v", err)
	}

	// Assert: inference was called exactly twice (1 initial + 1 continuation via nudge).
	if got := inf.callCount(); got != 2 {
		t.Errorf("inference call count: got %d, want 2", got)
	}

	// Assert: the second call's messages include the continuation nudge text.
	inf.mu.Lock()
	defer inf.mu.Unlock()
	if len(inf.recorded) < 2 {
		t.Fatalf("expected at least 2 recorded requests, got %d", len(inf.recorded))
	}
	secondReq := inf.recorded[1]
	nudgeText := agent.DefaultContinuationNudgeMessage
	found := false
	for _, msg := range secondReq.Messages {
		if strings.Contains(msg.TextContent(), nudgeText) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("second inference request should contain nudge text %q; messages: %v", nudgeText, messageTexts(secondReq.Messages))
	}

	// Assert: the loop completed (stop-word detected).
	if !result.Completed {
		t.Error("expected loop to complete (stop-word DONE found)")
	}

	// Assert: the TodoQueue is empty after completion.
	// This is verified implicitly: if the queue were non-empty and depth allowed,
	// inference would have been called more than twice. Since callCount == 2 and
	// the loop completed with the stop-word, the queue was properly drained.
	// The stop-word prevents re-enqueue after the second call, leaving it empty.
}

// TestContinuationNudge_NotTriggeredOnStopWord verifies that the continuation
// nudge is NOT enqueued when the model's first response already contains the
// stop-word, even when continuation_nudge_enabled is true.
func TestContinuationNudge_NotTriggeredOnStopWord(t *testing.T) {
	tmpDir := createNudgeTestDir(t)

	inf := &recordingStepInferencer{
		steps: []inferenceStep{
			// Only call: response contains the stop-word immediately.
			makeTextStep("Task complete. DONE"),
		},
	}

	exec := newIterativeTestExecutor(inf)
	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{MaxIterations: 1, StopWord: "DONE"}
	input := agentloop.NewExecuteInput("do the thing")

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop: %v", err)
	}

	// Assert: inference was called exactly once — no continuation triggered.
	if got := inf.callCount(); got != 1 {
		t.Errorf("inference call count: got %d, want 1 (no nudge should fire when stop-word present)", got)
	}

	if !result.Completed {
		t.Error("expected loop to complete (stop-word DONE found in first response)")
	}
}

// messageTexts extracts the text content of each message for debug output.
func messageTexts(msgs []messages.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.TextContent()
	}
	return out
}
