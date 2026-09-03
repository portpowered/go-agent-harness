package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestFixtureExecutorSupportsOrderedOutputs(t *testing.T) {
	observations := filepath.Join(t.TempDir(), "observations.jsonl")
	executor := &fixtureExecutor{fixture: fixture{
		Observations: observations,
		Calls: []expectedToolCall{
			{Name: "first", Arguments: `{"n":1}`, Output: `{"ok":1}`},
			{Name: "second", Arguments: `{"n":2}`, Output: `{"ok":2}`},
		},
	}}
	for index, call := range []messages.ToolCall{
		{ID: "call-1", Name: "first", Arguments: `{"n":1}`},
		{ID: "call-2", Name: "second", Arguments: `{"n":2}`},
	} {
		response, err := executor.Execute(context.Background(), call)
		if err != nil {
			t.Fatalf("execute call %d: %v", index, err)
		}
		if response.ToolCallID != call.ID || response.Content != executor.fixture.Calls[index].Output {
			t.Fatalf("response %d = %+v", index, response)
		}
	}
	if err := executor.verify(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(observations)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; lines != 2 {
		t.Fatalf("observation lines = %d, want 2", lines)
	}
}

func TestFixtureExecutorSupportsInjectedError(t *testing.T) {
	executor := &fixtureExecutor{fixture: fixture{
		Observations: filepath.Join(t.TempDir(), "observations.jsonl"),
		Calls:        []expectedToolCall{{Name: "fail", Arguments: `{}`, Error: "fixture failure"}},
	}}
	_, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-fail", Name: "fail", Arguments: `{}`})
	if err == nil || err.Error() != "fixture failure" {
		t.Fatalf("injected error = %v", err)
	}
}

func TestFixtureExecutorDelayHonorsCancellation(t *testing.T) {
	executor := &fixtureExecutor{fixture: fixture{
		Observations: filepath.Join(t.TempDir(), "observations.jsonl"),
		Calls:        []expectedToolCall{{Name: "slow", Arguments: `{}`, DelayMS: 1000}},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := executor.Execute(ctx, messages.ToolCall{ID: "call-slow", Name: "slow", Arguments: `{}`})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delayed cancellation error = %v", err)
	}
}

func TestFixtureExecutorRejectsWrongCallIdentity(t *testing.T) {
	executor := &fixtureExecutor{fixture: fixture{
		Observations: filepath.Join(t.TempDir(), "observations.jsonl"),
		Calls:        []expectedToolCall{{Name: "expected", Arguments: `{"id":1}`}},
	}}
	_, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-wrong", Name: "other", Arguments: `{"id":1}`})
	if err == nil || !strings.Contains(err.Error(), "want \"expected\"") {
		t.Fatalf("identity mismatch error = %v", err)
	}
}
