package integration

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/config"
	"github.com/portpowered/agent-cli/internal/session"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// createIterativeTestDir sets up a temp directory with a minimal agent-cli config.yaml.
// The config has a fake API key so Validate() passes without live credentials.
func createIterativeTestDir(t *testing.T) string {
	t.Helper()
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
	return tmpDir
}

// newIterativeTestExecutor creates an Executor with the given inferencer override.
// Both executor and toolDefs are nil — BuildLoop creates its own from config.
func newIterativeTestExecutor(inf messages.Inferencer) *agent.Executor {
	return agent.NewExecutor(nil, nil, inf, true)
}

// blockingMockInferencer blocks in Infer until the context is cancelled.
// Once started is signalled (closed), a caller may cancel the context to simulate Ctrl+C.
type blockingMockInferencer struct {
	once     sync.Once
	started  chan struct{} // closed exactly once when Infer is first called
	release  chan struct{} // close to return a normal response without cancellation
	response string
}

func newBlockingMockInferencer(response string) *blockingMockInferencer {
	return &blockingMockInferencer{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: response,
	}
}

func (m *blockingMockInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	m.once.Do(func() { close(m.started) })
	select {
	case <-ctx.Done():
		return messages.InferenceResult{}, ctx.Err()
	case <-m.release:
		return messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, m.response),
		}, nil
	}
}

func (m *blockingMockInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	result, err := m.Infer(ctx, req)
	if err != nil {
		return nil, err
	}
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

// TestIterativeLoop_InterruptMidIteration verifies that cancelling the context during
// an active iteration (simulating Ctrl+C) is detected as an interrupt: the trace record
// is saved with status "interrupted" and the iteration is marked "interrupted".
func TestIterativeLoop_InterruptMidIteration(t *testing.T) {
	tmpDir := createIterativeTestDir(t)

	inf := newBlockingMockInferencer("response")
	exec := newIterativeTestExecutor(inf)

	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{MaxIterations: 3}
	input := agentloop.NewExecuteInput("test task")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var result agent.IterativeRunResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, _ = exec.RunIterativeLoop(ctx, cfg, loopCfg, input, io.Discard)
	}()

	// Wait until Infer is blocking, then simulate Ctrl+C by cancelling the parent context.
	select {
	case <-inf.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for inferencer to start")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for loop to finish")
	}

	// The loop should have exited with a non-empty trace ID.
	if result.TraceID == "" {
		t.Fatal("expected a trace ID in the result")
	}

	// Verify the trace on disk is marked as interrupted.
	st := session.NewStorage(tmpDir)
	trace, err := st.LoadTrace(result.TraceID)
	if err != nil {
		t.Fatalf("LoadTrace: %v", err)
	}
	if trace == nil {
		t.Fatal("expected trace record on disk, got nil")
	}
	if trace.Status != session.TraceStatusInterrupted {
		t.Errorf("trace status: got %q, want %q", trace.Status, session.TraceStatusInterrupted)
	}
	if len(trace.Iterations) != 1 {
		t.Fatalf("expected 1 iteration in trace, got %d", len(trace.Iterations))
	}
	if trace.Iterations[0].Status != session.IterationStatusInterrupted {
		t.Errorf("iteration status: got %q, want %q", trace.Iterations[0].Status, session.IterationStatusInterrupted)
	}

	// The result should have one iteration with a context-canceled error.
	if len(result.Iterations) != 1 {
		t.Fatalf("expected 1 result iteration, got %d", len(result.Iterations))
	}
	if !errors.Is(result.Iterations[0].Err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got: %v", result.Iterations[0].Err)
	}
}

// TestIterativeLoop_ResumeFromInterruptedTrace verifies that when a trace whose last
// iteration was interrupted is resumed, the interrupted iteration is discarded and
// restarted fresh (not skipped to the next one).
func TestIterativeLoop_ResumeFromInterruptedTrace(t *testing.T) {
	tmpDir := createIterativeTestDir(t)

	// Pre-create a trace with iteration 1 completed and iteration 2 interrupted.
	st := session.NewStorage(tmpDir)
	preTrace := session.TraceRecord{
		TraceID: st.NewTraceID(),
		Status:  session.TraceStatusInterrupted,
		Config: session.TraceConfig{
			MaxIterations: 3,
			StopWord:      "",
			Prompt:        "original task",
		},
		CurrentIteration: 2,
		Iterations: []session.IterationTrace{
			{Iteration: 1, Status: session.IterationStatusCompleted},
			{Iteration: 2, Status: session.IterationStatusInterrupted},
		},
	}
	if err := st.SaveTrace(preTrace); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	inf := &mockInferencer{response: "ok"}
	exec := newIterativeTestExecutor(inf)

	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{
		MaxIterations: 3,
		TraceID:       preTrace.TraceID,
	}
	input := agentloop.NewExecuteInput("other task") // should be overridden by trace config

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop: %v", err)
	}

	// Should have run iterations 2 and 3 (2 is retried, 3 follows).
	if len(result.Iterations) != 2 {
		t.Fatalf("expected 2 result iterations (2 and 3), got %d: %+v", len(result.Iterations), result.Iterations)
	}
	if result.Iterations[0].Iteration != 2 {
		t.Errorf("first result iteration: got %d, want 2", result.Iterations[0].Iteration)
	}
	if result.Iterations[1].Iteration != 3 {
		t.Errorf("second result iteration: got %d, want 3", result.Iterations[1].Iteration)
	}

	// The final trace should have all 3 iterations: 1 (preloaded), 2 (retried), 3 (new).
	finalTrace, loadErr := st.LoadTrace(preTrace.TraceID)
	if loadErr != nil {
		t.Fatalf("LoadTrace: %v", loadErr)
	}
	if finalTrace.Status != session.TraceStatusCompleted {
		t.Errorf("final trace status: got %q, want %q", finalTrace.Status, session.TraceStatusCompleted)
	}
	if len(finalTrace.Iterations) != 3 {
		t.Fatalf("expected 3 total iterations in trace, got %d", len(finalTrace.Iterations))
	}
	if finalTrace.Iterations[1].Iteration != 2 || finalTrace.Iterations[1].Status != session.IterationStatusCompleted {
		t.Errorf("iteration 2: got {%d, %s}, want {2, completed}", finalTrace.Iterations[1].Iteration, finalTrace.Iterations[1].Status)
	}
}

// TestIterativeLoop_ResumeAtCorrectIteration verifies that when the last iteration in a
// trace was completed (not interrupted), the loop resumes from the NEXT iteration.
func TestIterativeLoop_ResumeAtCorrectIteration(t *testing.T) {
	tmpDir := createIterativeTestDir(t)

	st := session.NewStorage(tmpDir)
	// Trace with only iteration 1 completed (no interrupted entry).
	preTrace := session.TraceRecord{
		TraceID: st.NewTraceID(),
		Status:  session.TraceStatusRunning,
		Config: session.TraceConfig{
			MaxIterations: 3,
			Prompt:        "task",
		},
		CurrentIteration: 1,
		Iterations: []session.IterationTrace{
			{Iteration: 1, Status: session.IterationStatusCompleted},
		},
	}
	if err := st.SaveTrace(preTrace); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	inf := &mockInferencer{response: "ok"}
	exec := newIterativeTestExecutor(inf)

	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{
		MaxIterations: 3,
		TraceID:       preTrace.TraceID,
	}

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, agentloop.NewExecuteInput("x"), io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop: %v", err)
	}

	// Should have run iterations 2 and 3 only.
	if len(result.Iterations) != 2 {
		t.Fatalf("expected 2 result iterations (2 and 3), got %d", len(result.Iterations))
	}
	if result.Iterations[0].Iteration != 2 {
		t.Errorf("first result iteration: got %d, want 2", result.Iterations[0].Iteration)
	}
	if result.Iterations[1].Iteration != 3 {
		t.Errorf("second result iteration: got %d, want 3", result.Iterations[1].Iteration)
	}
}

// TestIterativeLoop_ConfigRestoredOnResume verifies that maxIterations, stopWord, and
// prompt are all restored from the trace record's Config field when resuming.
func TestIterativeLoop_ConfigRestoredOnResume(t *testing.T) {
	tmpDir := createIterativeTestDir(t)

	st := session.NewStorage(tmpDir)
	// Pre-create a trace with specific config and an interrupted first iteration.
	preTrace := session.TraceRecord{
		TraceID: st.NewTraceID(),
		Status:  session.TraceStatusInterrupted,
		Config: session.TraceConfig{
			MaxIterations: 4,
			StopWord:      "STOP",
			Prompt:        "original-task",
		},
		CurrentIteration: 1,
		Iterations: []session.IterationTrace{
			{Iteration: 1, Status: session.IterationStatusInterrupted},
		},
	}
	if err := st.SaveTrace(preTrace); err != nil {
		t.Fatalf("SaveTrace: %v", err)
	}

	// Recording inferencer returns "STOP" so the stop word (if correctly restored) ends the loop.
	rec := &recordingInferencer{response: "STOP"}
	exec := newIterativeTestExecutor(rec)

	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	// Pass different values — these should be overridden by the trace config.
	loopCfg := agent.IterativeLoopConfig{
		MaxIterations: 2,
		StopWord:      "OTHER",
		TraceID:       preTrace.TraceID,
	}
	input := agentloop.NewExecuteInput("other-task") // should be overridden by trace config

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop: %v", err)
	}

	// Stop word "STOP" from trace config should have matched the response, stopping after 1 iteration.
	if !result.Completed {
		t.Errorf("expected loop to complete (stop word STOP matched), but result.Completed = false")
	}
	if len(result.Iterations) != 1 {
		t.Errorf("expected 1 result iteration (stopped by stop word), got %d", len(result.Iterations))
	}

	// Verify the original prompt was sent to the inferencer (not "other-task").
	if !rec.containsSystemPrompt("original-task") {
		t.Errorf("expected inferencer to receive 'original-task' from trace config, but it was not found in recorded messages")
	}
}
