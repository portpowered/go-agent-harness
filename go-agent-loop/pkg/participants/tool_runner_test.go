package participants

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

type testToolExecutor struct {
	results map[string]string
}

func (t *testToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	content, ok := t.results[call.Name]
	if !ok {
		return messages.ToolCallResponse{}, fmt.Errorf("unknown tool: %s", call.Name)
	}
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Content:    content,
	}, nil
}

// drainToolDeltas reads from DeltaOutbox until MESSAGE.END or ERROR.
// Returns a map of ToolCallId -> accumulated text and whether an error delta arrived.
func drainToolDeltas(t *testing.T, ctx context.Context, runner *ToolRunner) (toolText map[string]string, gotErr bool) {
	t.Helper()
	toolText = make(map[string]string)
	var currentToolID string
	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for tool delta")
		}
		switch v := delta.Value.(type) {
		case *messages.TextStartValue:
			_ = v
			currentToolID = delta.ToolCallId
		case *messages.TextDeltaValue:
			toolText[currentToolID] += v.Content
		case *messages.MessageEndValue:
			_ = v
			return
		case *messages.ErrorValue:
			_ = v
			gotErr = true
			return
		}
	}
}

func TestToolRunner_SingleCall(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{"get_weather": "sunny, 22C"},
	}

	runner := NewToolRunner(exec, 10)
	ap := NewActiveParticipant(messages.Tool, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.ToolBatchRequest{
		Calls: []messages.ToolCall{
			{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`},
		},
	})

	toolText, gotErr := drainToolDeltas(t, ctx, runner)
	if gotErr {
		t.Fatal("unexpected error delta")
	}
	if toolText["tc1"] != "sunny, 22C" {
		t.Errorf("expected 'sunny, 22C', got %q", toolText["tc1"])
	}
}

func TestToolRunner_ParallelExecution(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{
			"tool_a": "result_a",
			"tool_b": "result_b",
			"tool_c": "result_c",
		},
	}

	runner := NewToolRunner(exec, 10)
	ap := NewActiveParticipant(messages.Tool, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.ToolBatchRequest{
		Calls: []messages.ToolCall{
			{ID: "tc1", Name: "tool_a"},
			{ID: "tc2", Name: "tool_b"},
			{ID: "tc3", Name: "tool_c"},
		},
	})

	toolText, gotErr := drainToolDeltas(t, ctx, runner)
	if gotErr {
		t.Fatal("unexpected error delta")
	}

	// Results should be present for all tool call IDs
	expected := map[string]string{"tc1": "result_a", "tc2": "result_b", "tc3": "result_c"}
	for id, exp := range expected {
		if toolText[id] != exp {
			t.Errorf("tool %s: expected %q, got %q", id, exp, toolText[id])
		}
	}
}

func TestToolRunner_ErrorPropagation(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{"tool_a": "ok"},
		// "tool_b" is not in results, will return error
	}

	runner := NewToolRunner(exec, 10)
	ap := NewActiveParticipant(messages.Tool, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.ToolBatchRequest{
		Calls: []messages.ToolCall{
			{ID: "tc1", Name: "tool_a"},
			{ID: "tc2", Name: "tool_b"},
		},
	})

	_, gotErr := drainToolDeltas(t, ctx, runner)
	if !gotErr {
		t.Error("expected error delta for unknown tool")
	}
}

func TestToolRunner_ContextCancellation(t *testing.T) {
	exec := &testToolExecutor{results: map[string]string{}}

	runner := NewToolRunner(exec, 10)
	ap := NewActiveParticipant(messages.Tool, runner)

	ctx, cancel := context.WithCancel(context.Background())
	ap.Start(ctx)

	cancel()
	ap.Stop() // should not hang
}

func TestExecuteBatch_AllSucceed(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{"a": "res_a", "b": "res_b", "c": "res_c"},
	}
	runner := NewToolRunner(exec, 10)

	results, err := runner.executeBatch(context.Background(), []messages.ToolCall{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
		{ID: "3", Name: "c"},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, exp := range []string{"res_a", "res_b", "res_c"} {
		if results[i].Content != exp {
			t.Errorf("result[%d]: expected %q, got %q", i, exp, results[i].Content)
		}
	}
}

func TestExecuteBatch_SingleFailure(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{"a": "ok", "c": "ok"},
		// "b" missing → will fail
	}
	runner := NewToolRunner(exec, 10)

	_, err := runner.executeBatch(context.Background(), []messages.ToolCall{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
		{ID: "3", Name: "c"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `tool "b" failed`) {
		t.Errorf("error should mention tool b, got: %v", err)
	}
}

func TestExecuteBatch_MultipleFailures(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{"a": "ok"},
		// "b" and "c" missing → both will fail
	}
	runner := NewToolRunner(exec, 10)

	_, err := runner.executeBatch(context.Background(), []messages.ToolCall{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
		{ID: "3", Name: "c"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, `tool "b" failed`) {
		t.Errorf("error should mention tool b, got: %v", err)
	}
	if !strings.Contains(errStr, `tool "c" failed`) {
		t.Errorf("error should mention tool c, got: %v", err)
	}
}

func TestExecuteBatch_AllFail(t *testing.T) {
	exec := &testToolExecutor{
		results: map[string]string{}, // no tools known
	}
	runner := NewToolRunner(exec, 10)

	_, err := runner.executeBatch(context.Background(), []messages.ToolCall{
		{ID: "1", Name: "x"},
		{ID: "2", Name: "y"},
		{ID: "3", Name: "z"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	for _, name := range []string{"x", "y", "z"} {
		if !strings.Contains(errStr, fmt.Sprintf("tool %q failed", name)) {
			t.Errorf("error should mention tool %s, got: %v", name, err)
		}
	}
}
