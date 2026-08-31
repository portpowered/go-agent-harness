package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// --- shared helpers for lifecycle tests ---

// inferenceStep is a single canned response (or error) for stepInferencer.
type inferenceStep struct {
	result messages.InferenceResult
	err    error
}

// stepInferencer returns steps in sequence. After the last step is consumed it
// repeats the last step indefinitely. Thread-safe via mutex.
type stepInferencer struct {
	mu    sync.Mutex
	steps []inferenceStep
	idx   int
}

func (m *stepInferencer) Infer(_ context.Context, _ messages.InferenceRequest) (messages.InferenceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	step := m.steps[m.idx]
	if m.idx < len(m.steps)-1 {
		m.idx++
	}
	return step.result, step.err
}

// InferStream returns (nil, nil) so the model runner falls back to Infer.
func (m *stepInferencer) InferStream(_ context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, nil
}

// makeTextStep returns an inferenceStep with a plain-text assistant message.
func makeTextStep(text string) inferenceStep {
	return inferenceStep{
		result: messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, text),
		},
	}
}

// makeToolCallStep returns an inferenceStep that asks the agent loop to call one tool.
func makeToolCallStep(id, name, argsJSON string) inferenceStep {
	tc := messages.ToolCall{ID: id, Name: name, Arguments: argsJSON}
	return inferenceStep{
		result: messages.InferenceResult{
			Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{tc}},
			ToolCalls: []messages.ToolCall{tc},
		},
	}
}

// makeErrStep returns an inferenceStep whose Infer call returns a non-nil error.
func makeErrStep(msg string) inferenceStep {
	return inferenceStep{err: fmt.Errorf("%s", msg)}
}

// jsonArgs marshals a map to a JSON string suitable for ToolCall.Arguments.
func jsonArgs(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("jsonArgs: %v", err))
	}
	return string(b)
}

// --- tests ---

// TestFullLoopLifecycle_HappyPath exercises the complete loop lifecycle:
//   - 3 iterations with fresh context windows
//   - Iteration 1: write_file tool writes a progress file
//   - Iteration 2: dispatch_agent spawns a sub-agent (child uses same mock inferencer)
//   - Iteration 3: agent returns stop word → loop ends early
//
// Verifies: iteration count, stop word detection, sub-agent dispatch, progress
// file written, and trace record created with correct lineage.
func TestFullLoopLifecycle_HappyPath(t *testing.T) {
	tmpDir := createIterativeTestDir(t)
	progressPath := filepath.Join(tmpDir, "progress.txt")

	writeFileArgs := jsonArgs(map[string]string{
		"path":    progressPath,
		"content": "iteration 1 progress",
	})
	dispatchArgs := jsonArgs(map[string]string{
		"system_prompt": "You are a helper agent.",
		"task":          "Count to 3 and return.",
	})

	inf := &stepInferencer{
		steps: []inferenceStep{
			// Iter 1, call 1: write progress file.
			makeToolCallStep("w1", "write_file", writeFileArgs),
			// Iter 1, call 2: respond after tool result.
			makeTextStep("Iteration 1 done"),
			// Iter 2, call 3: dispatch a sub-agent.
			makeToolCallStep("d1", "dispatch_agent", dispatchArgs),
			// Child agent (call 4): sub-agent response.
			makeTextStep("1, 2, 3"),
			// Iter 2, call 5: respond after sub-agent result.
			makeTextStep("Iteration 2 done"),
			// Iter 3, call 6: respond with stop word.
			makeTextStep("All iterations complete. COMPLETE"),
		},
	}

	exec := newIterativeTestExecutor(inf)
	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		WorkDir:             tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{MaxIterations: 3, StopWord: "COMPLETE"}
	input := agentloop.NewExecuteInput("run the full lifecycle")

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop: %v", err)
	}

	// Verify iteration count and stop word detection.
	if len(result.Iterations) != 3 {
		t.Errorf("iteration count: got %d, want 3", len(result.Iterations))
	}
	if !result.Completed {
		t.Error("expected loop to complete (stop word COMPLETE found in iteration 3)")
	}

	// Verify progress file was written by the write_file tool in iteration 1.
	data, readErr := os.ReadFile(progressPath)
	if readErr != nil {
		t.Fatalf("progress file not written: %v", readErr)
	}
	if got := string(data); got != "iteration 1 progress" {
		t.Errorf("progress file content: got %q, want %q", got, "iteration 1 progress")
	}

	// Verify trace record has correct lineage.
	st := session.NewStorage(tmpDir)
	trace, loadErr := st.LoadTrace(result.TraceID)
	if loadErr != nil {
		t.Fatalf("LoadTrace: %v", loadErr)
	}
	if trace == nil {
		t.Fatal("expected trace record on disk, got nil")
	}
	if trace.Status != session.TraceStatusCompleted {
		t.Errorf("trace status: got %q, want %q", trace.Status, session.TraceStatusCompleted)
	}
	if len(trace.Iterations) != 3 {
		t.Fatalf("trace iteration count: got %d, want 3", len(trace.Iterations))
	}
	for i, iter := range trace.Iterations {
		wantNum := i + 1
		if iter.Iteration != wantNum {
			t.Errorf("trace.Iterations[%d].Iteration: got %d, want %d", i, iter.Iteration, wantNum)
		}
		if iter.Status != session.IterationStatusCompleted {
			t.Errorf("trace.Iterations[%d].Status: got %q, want completed", i, iter.Status)
		}
	}
}

// TestFullLoopLifecycle_SubAgentFails verifies that when a dispatched sub-agent
// fails (its inferencer returns an error), the parent receives the failure as a
// tool result message rather than a fatal error, and the loop continues normally.
func TestFullLoopLifecycle_SubAgentFails(t *testing.T) {
	tmpDir := createIterativeTestDir(t)

	dispatchArgs := jsonArgs(map[string]string{
		"system_prompt": "You are a helper.",
		"task":          "Do some work.",
	})

	inf := &stepInferencer{
		steps: []inferenceStep{
			// Iter 1, call 1: dispatch a sub-agent.
			makeToolCallStep("d1", "dispatch_agent", dispatchArgs),
			// Child agent (call 2): inference fails.
			makeErrStep("child inference failed"),
			// Iter 1, call 3: parent receives the error as a tool result and finishes.
			makeTextStep("Sub-agent error handled. COMPLETE"),
		},
	}

	exec := newIterativeTestExecutor(inf)
	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{MaxIterations: 3, StopWord: "COMPLETE"}
	input := agentloop.NewExecuteInput("dispatch and handle failure")

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop returned fatal error: %v", err)
	}

	// Stop word should have been detected — loop completed in iteration 1.
	if !result.Completed {
		t.Error("expected loop to complete (stop word COMPLETE found after sub-agent error)")
	}
	if len(result.Iterations) < 1 {
		t.Fatal("expected at least 1 result iteration")
	}
	// The iteration itself must not carry an error — the sub-agent failure was
	// delivered as a tool message, not a fatal error.
	if result.Iterations[0].Err != nil {
		t.Errorf("expected no iteration error (sub-agent error returned as tool result), got: %v", result.Iterations[0].Err)
	}
}

// TestFullLoopLifecycle_IterationFails verifies that when iteration 1 fails due
// to an inference error, the error is recorded in IterativeRunResult and the loop
// continues to iteration 2, which completes successfully.
func TestFullLoopLifecycle_IterationFails(t *testing.T) {
	tmpDir := createIterativeTestDir(t)

	inf := &stepInferencer{
		steps: []inferenceStep{
			// Iteration 1: inference fails.
			makeErrStep("iteration 1 inference failure"),
			// Iteration 2: recovers with stop word.
			makeTextStep("Recovered successfully. COMPLETE"),
		},
	}

	exec := newIterativeTestExecutor(inf)
	cfg := &agent.Config{
		ConfigDir:           tmpDir,
		NoSystemInformation: true,
		SystemPrompt:        "none",
	}
	loopCfg := agent.IterativeLoopConfig{MaxIterations: 3, StopWord: "COMPLETE"}
	input := agentloop.NewExecuteInput("task with failure recovery")

	result, err := exec.RunIterativeLoop(context.Background(), cfg, loopCfg, input, io.Discard)
	if err != nil {
		t.Fatalf("RunIterativeLoop returned fatal error: %v", err)
	}

	// Expect 2 iterations: one failed and one that succeeded.
	if len(result.Iterations) != 2 {
		t.Fatalf("expected 2 iterations (1 failed + 1 succeeded), got %d", len(result.Iterations))
	}

	// Iteration 1 must carry a non-nil error.
	if result.Iterations[0].Err == nil {
		t.Error("expected iteration 1 to have an error, got nil")
	}

	// Iteration 2 must succeed and trigger completion.
	if result.Iterations[1].Err != nil {
		t.Errorf("expected iteration 2 to succeed, got error: %v", result.Iterations[1].Err)
	}
	if !result.Completed {
		t.Error("expected loop to complete (stop word COMPLETE found in iteration 2)")
	}
}
