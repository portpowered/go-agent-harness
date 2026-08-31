package participants

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

type countingToolExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (e *countingToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "ok"}, nil
}

func (e *countingToolExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
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

func TestToolRunner_EmptyResultPreservesCallID(t *testing.T) {
	const callID = "tc-empty"
	exec := &testToolExecutor{results: map[string]string{"empty": ""}}
	runner := NewToolRunner(exec, 10)
	ap := NewActiveParticipant(messages.Tool, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.ToolBatchRequest{
		Calls: []messages.ToolCall{{ID: callID, Name: "empty"}},
	})

	var deltas []messages.StreamMessage
	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for empty tool result")
		}
		deltas = append(deltas, delta)
		if _, ok := delta.Value.(*messages.MessageEndValue); ok {
			break
		}
	}

	if len(deltas) != 4 {
		t.Fatalf("empty result deltas = %d, want MESSAGE.START, TEXT.START, TEXT.END, MESSAGE.END", len(deltas))
	}
	if _, ok := deltas[1].Value.(*messages.TextStartValue); !ok || deltas[1].ToolCallId != "tc-empty" {
		t.Fatalf("empty result start = %#v, want correlated TEXT.START", deltas[1])
	}
	if _, ok := deltas[2].Value.(*messages.TextEndValue); !ok || deltas[2].ToolCallId != "tc-empty" {
		t.Fatalf("empty result end = %#v, want correlated TEXT.END", deltas[2])
	}

	results := messages.ReconstructToolMessagesFromDeltas(deltas)
	if len(results) != 1 || results[0].ToolCallID != "tc-empty" {
		t.Fatalf("reconstructed empty results = %#v, want one result for tc-empty", results)
	}
	if len(results[0].ContentParts) != 1 || results[0].TextContent() != "" {
		t.Fatalf("reconstructed empty content = %#v, want one empty text part", results[0].ContentParts)
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

func TestExecuteBatch_DuplicateCallIDIsAdmittedOnce(t *testing.T) {
	exec := &countingToolExecutor{}
	runner := NewToolRunner(exec, 10)
	call := messages.ToolCall{ID: "provider-call-1", Name: "lookup"}

	first, err := runner.executeBatch(context.Background(), []messages.ToolCall{call})
	if err != nil {
		t.Fatalf("first executeBatch error = %v", err)
	}
	if len(first) != 1 || first[0].ToolCallID != call.ID {
		t.Fatalf("first results = %#v, want one result for %q", first, call.ID)
	}

	second, err := runner.executeBatch(context.Background(), []messages.ToolCall{call})
	if err != nil {
		t.Fatalf("duplicate executeBatch error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("duplicate results = %#v, want no second result", second)
	}
	if got := exec.callCount(); got != 1 {
		t.Fatalf("executor call count = %d, want exactly one admission", got)
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

type acknowledgementGateExecutor struct {
	release <-chan struct{}
}

func (e acknowledgementGateExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if call.Name == "slow" {
		select {
		case <-e.release:
		case <-ctx.Done():
			return messages.ToolCallResponse{}, ctx.Err()
		}
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: call.Name + " result"}, nil
}

func TestToolRunner_AcknowledgesOnlyPendingLongRunningCalls(t *testing.T) {
	release := make(chan struct{})
	acknowledgements := make(chan []messages.ToolCall, 2)
	runner := NewToolRunner(acknowledgementGateExecutor{release: release}, 8)
	runner.ConfigureAcknowledgement(10*time.Millisecond, func(name string) bool {
		return name == "slow"
	}, func(_ context.Context, calls []messages.ToolCall) {
		acknowledgements <- calls
	})

	resultCh := make(chan []messages.ToolCallResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		results, err := runner.executeBatch(context.Background(), []messages.ToolCall{
			{ID: "fast-call", Name: "fast"},
			{ID: "slow-call", Name: "slow"},
		})
		resultCh <- results
		errCh <- err
	}()

	select {
	case calls := <-acknowledgements:
		if len(calls) != 1 || calls[0].ID != "slow-call" {
			t.Fatalf("acknowledged calls = %#v, want only slow-call", calls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long-running call did not trigger acknowledgement")
	}
	close(release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("executeBatch error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeBatch did not complete after release")
	}
	results := <-resultCh
	if len(results) != 2 || results[0].ToolCallID != "fast-call" || results[1].ToolCallID != "slow-call" {
		t.Fatalf("results = %#v, want stable call order", results)
	}
	select {
	case calls := <-acknowledgements:
		t.Fatalf("duplicate acknowledgement = %#v", calls)
	default:
	}
}

func TestToolRunner_FastCallCompletingBeforeThresholdDoesNotAcknowledge(t *testing.T) {
	acknowledged := make(chan struct{}, 1)
	runner := NewToolRunner(&testToolExecutor{results: map[string]string{"fast": "done"}}, 8)
	runner.ConfigureAcknowledgement(100*time.Millisecond, func(string) bool { return true }, func(context.Context, []messages.ToolCall) {
		acknowledged <- struct{}{}
	})

	results, err := runner.executeBatch(context.Background(), []messages.ToolCall{{ID: "fast-call", Name: "fast"}})
	if err != nil {
		t.Fatalf("executeBatch error = %v", err)
	}
	if len(results) != 1 || results[0].Content != "done" {
		t.Fatalf("results = %#v, want fast result", results)
	}
	select {
	case <-acknowledged:
		t.Fatal("fast call emitted an unnecessary acknowledgement")
	default:
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
