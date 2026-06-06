package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/subsystems"
)

// iterTestTokenCounter is a test double that returns a fixed token count.
type iterTestTokenCounter struct {
	count int
}

func (c *iterTestTokenCounter) Count(_ []messages.Message) int {
	return c.count
}

// Compile-time check that iterTestTokenCounter satisfies subsystems.TokenCounter.
var _ subsystems.TokenCounter = (*iterTestTokenCounter)(nil)

// TestIterativeLoop_NormalIteration verifies the loop runs MaxIterations times
// when no stop word is configured.
func TestIterativeLoop_NormalIteration(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response 1")},
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response 2")},
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response 3")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 3},
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Iterations) != 3 {
		t.Errorf("expected 3 iterations, got %d", len(result.Iterations))
	}
	if result.Completed {
		t.Error("expected Completed=false when no stop word configured")
	}
	for i, iter := range result.Iterations {
		if iter.Iteration != i+1 {
			t.Errorf("iteration %d: expected Iteration=%d, got %d", i, i+1, iter.Iteration)
		}
		if iter.Err != nil {
			t.Errorf("iteration %d: unexpected error: %v", i+1, iter.Err)
		}
	}
}

// TestIterativeLoop_MaxIterationReached verifies the loop stops at MaxIterations
// when the stop word is never found.
func TestIterativeLoop_MaxIterationReached(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "no stop word here")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 2, StopWord: "DONE"},
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Iterations) != 2 {
		t.Errorf("expected 2 iterations, got %d", len(result.Iterations))
	}
	if result.Completed {
		t.Error("expected Completed=false when max iterations reached without stop word")
	}
}

// TestIterativeLoop_CompletionOnFirstIteration verifies the loop stops on iteration 1
// when the stop word is found immediately.
func TestIterativeLoop_CompletionOnFirstIteration(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "task done COMPLETE")},
			// This response should never be used.
			{Message: messages.NewTextMessage(messages.RoleAssistant, "second response")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 5, StopWord: "COMPLETE"},
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Iterations) != 1 {
		t.Errorf("expected 1 iteration, got %d", len(result.Iterations))
	}
	if !result.Completed {
		t.Error("expected Completed=true when stop word found on first iteration")
	}
}

// TestIterativeLoop_CompletionMidLoop verifies the loop stops mid-way when the
// stop word is found on a non-first iteration.
func TestIterativeLoop_CompletionMidLoop(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "still working")},
			{Message: messages.NewTextMessage(messages.RoleAssistant, "all done COMPLETE")},
			// This response should never be used.
			{Message: messages.NewTextMessage(messages.RoleAssistant, "third response")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 5, StopWord: "COMPLETE"},
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Iterations) != 2 {
		t.Errorf("expected 2 iterations, got %d", len(result.Iterations))
	}
	if !result.Completed {
		t.Error("expected Completed=true when stop word found mid-loop")
	}
	if result.Iterations[1].Text != "all done COMPLETE" {
		t.Errorf("expected second iteration text 'all done COMPLETE', got %q", result.Iterations[1].Text)
	}
}

// TestIterativeLoop_DefaultMaxIterations verifies MaxIterations defaults to 5
// when set to 0 (zero value).
func TestIterativeLoop_DefaultMaxIterations(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{}, // MaxIterations=0 → defaults to 5
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Iterations) != 5 {
		t.Errorf("expected 5 iterations (default), got %d", len(result.Iterations))
	}
}

// TestIterativeLoop_SystemPromptAugmentation verifies the system prompt received by
// the inferencer contains the iteration annotation and stop word instruction.
func TestIterativeLoop_SystemPromptAugmentation(t *testing.T) {
	capturer := &capturingInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response 1")},
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response 2")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 2, StopWord: "DONE"},
		WithInferencer(capturer),
		WithSystemPrompt("base prompt"),
	)

	_, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(capturer.capturedMessages) < 2 {
		t.Fatalf("expected at least 2 captured message slices, got %d", len(capturer.capturedMessages))
	}

	// First iteration: system message must contain "iteration 1 of 2" and stop word instruction.
	sysMsg1 := getIterTestSystemMessage(capturer.capturedMessages[0])
	if sysMsg1 == "" {
		t.Error("first iteration: expected system message in conversation")
	}
	if !strings.Contains(sysMsg1, "iteration 1 of 2") {
		t.Errorf("first iteration: expected 'iteration 1 of 2' in system prompt, got: %q", sysMsg1)
	}
	if !strings.Contains(sysMsg1, "When you are finished, end your response with DONE") {
		t.Errorf("first iteration: expected stop word instruction in system prompt, got: %q", sysMsg1)
	}
	if !strings.Contains(sysMsg1, "base prompt") {
		t.Errorf("first iteration: expected base prompt in system prompt, got: %q", sysMsg1)
	}

	// Second iteration: system message must contain "iteration 2 of 2".
	sysMsg2 := getIterTestSystemMessage(capturer.capturedMessages[1])
	if !strings.Contains(sysMsg2, "iteration 2 of 2") {
		t.Errorf("second iteration: expected 'iteration 2 of 2' in system prompt, got: %q", sysMsg2)
	}
}

// TestIterativeLoop_FreshContextPerIteration verifies each iteration has no history
// from previous iterations — each starts with a clean slate.
func TestIterativeLoop_FreshContextPerIteration(t *testing.T) {
	capturer := &capturingInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "first answer")},
			{Message: messages.NewTextMessage(messages.RoleAssistant, "second answer")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 2},
		WithInferencer(capturer),
	)

	_, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(capturer.capturedMessages) < 2 {
		t.Fatalf("expected 2 captured message slices, got %d", len(capturer.capturedMessages))
	}

	// The second iteration must NOT contain the first iteration's assistant response.
	for _, msg := range capturer.capturedMessages[1] {
		if msg.Role == messages.RoleAssistant && msg.TextContent() == "first answer" {
			t.Error("second iteration should NOT contain first iteration's assistant response (fresh context expected)")
		}
	}
}

// TestIterativeLoop_ContextPressureNotifierWired verifies that ContextPressureNotifier
// is wired without error when TokenCounter and PressureThreshold are configured.
func TestIterativeLoop_ContextPressureNotifierWired(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "response")},
		},
	}
	counter := &iterTestTokenCounter{count: 10} // well below threshold

	loop := NewIterativeLoop(
		IterativeConfig{
			MaxIterations:     1,
			PressureThreshold: 0.8,
			PressureMessage:   "save your work",
		},
		WithInferencer(inf),
		WithTokenCounter(counter, 1000),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Iterations) != 1 {
		t.Errorf("expected 1 iteration, got %d", len(result.Iterations))
	}
	if result.Iterations[0].Err != nil {
		t.Errorf("unexpected iteration error: %v", result.Iterations[0].Err)
	}
}

// TestIterativeLoop_StopWordInMiddleOfResponse verifies that the stop word is
// detected even when it appears in the middle (not at the end) of the response.
func TestIterativeLoop_StopWordInMiddleOfResponse(t *testing.T) {
	inf := &mockInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "done COMPLETE keep going")},
			// Should never be used.
			{Message: messages.NewTextMessage(messages.RoleAssistant, "second response")},
		},
	}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 5, StopWord: "COMPLETE"},
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Iterations) != 1 {
		t.Errorf("expected 1 iteration when stop word found mid-text, got %d", len(result.Iterations))
	}
	if !result.Completed {
		t.Error("expected Completed=true when stop word found in middle of response")
	}
	if result.Iterations[0].Text != "done COMPLETE keep going" {
		t.Errorf("expected 'done COMPLETE keep going', got %q", result.Iterations[0].Text)
	}
}

// TestIterativeLoop_IterationFailureRecordedAndLoopContinues verifies that when an
// iteration fails, the error is recorded in IterationResult.Err and the loop
// continues running subsequent iterations rather than aborting.
func TestIterativeLoop_IterationFailureRecordedAndLoopContinues(t *testing.T) {
	inf := &failingInferencer{message: "inference failed"}

	loop := NewIterativeLoop(
		IterativeConfig{MaxIterations: 2},
		WithInferencer(inf),
	)

	result, err := loop.Execute(context.Background(), NewExecuteInput("task"))
	if err != nil {
		t.Fatalf("Execute should not return a top-level error when iterations fail: %v", err)
	}

	if len(result.Iterations) != 2 {
		t.Errorf("expected 2 iteration records even when they fail, got %d", len(result.Iterations))
	}
	for i, iter := range result.Iterations {
		if iter.Err == nil {
			t.Errorf("iteration %d: expected non-nil Err, got nil", i+1)
		}
		if iter.Iteration != i+1 {
			t.Errorf("iteration %d: expected Iteration=%d, got %d", i, i+1, iter.Iteration)
		}
	}
	if result.Completed {
		t.Error("expected Completed=false when all iterations fail")
	}
}

// getIterTestSystemMessage returns the text content of the first system message.
func getIterTestSystemMessage(msgs []messages.Message) string {
	for _, m := range msgs {
		if m.Role == messages.RoleSystem {
			return m.TextContent()
		}
	}
	return ""
}
