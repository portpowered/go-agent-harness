package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionToolExecutorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f sessionToolExecutorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

func TestSessionToolExecutor_PreservesCallAndCorrelatesSuccess(t *testing.T) {
	call := messages.ToolCall{
		ID:        "s14-call-42",
		Name:      "distinctive_session_tool",
		Arguments: `{"city":"São Paulo","units":"metric"}`,
	}
	var got messages.ToolCall
	var mu sync.Mutex
	inner := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		got = call
		mu.Unlock()
		return messages.ToolCallResponse{Content: "s14-result-payload"}, nil
	})

	response, err := newSessionToolExecutor(inner, time.Second).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if got != call {
		t.Fatalf("executor call = %#v, want %#v", got, call)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
	}
	if response.Content != "s14-result-payload" {
		t.Fatalf("response content = %q, want exact executor payload", response.Content)
	}
}

func TestSessionToolExecutor_ConvertsFailuresToCorrelatedResults(t *testing.T) {
	call := messages.ToolCall{ID: "s4-call-1", Name: "failure_case", Arguments: `{not-json}`}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	cases := []struct {
		name     string
		executor messages.ToolExecutor
		timeout  time.Duration
		contains string
	}{
		{
			name: "unknown tool",
			executor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				return messages.ToolCallResponse{}, errors.New(`tool "missing_tool" not found`)
			}),
			contains: "not found",
		},
		{
			name: "malformed arguments",
			executor: sessionToolExecutorFunc(func(_ context.Context, got messages.ToolCall) (messages.ToolCallResponse, error) {
				if got.Arguments != call.Arguments {
					return messages.ToolCallResponse{}, errors.New("arguments were changed")
				}
				return messages.ToolCallResponse{}, errors.New("failed to parse tool arguments")
			}),
			contains: "parse tool arguments",
		},
		{
			name: "executor panic",
			executor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				panic("panic detail must stay inside this invocation")
			}),
			contains: "panicked",
		},
		{
			name:    "executor timeout",
			timeout: 10 * time.Millisecond,
			executor: sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
				<-ctx.Done()
				<-release
				return messages.ToolCallResponse{}, ctx.Err()
			}),
			contains: "timed out",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := newSessionToolExecutor(tc.executor, tc.timeout).Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			if response.ToolCallID != call.ID || response.Name != call.Name {
				t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
			}
			if strings.TrimSpace(response.Content) == "" {
				t.Fatal("failure response content is empty")
			}
			if !strings.Contains(response.Content, tc.contains) {
				t.Fatalf("failure response = %q, want substring %q", response.Content, tc.contains)
			}
			if len(response.ContentParts) != 0 {
				t.Fatalf("failure response unexpectedly contained content parts: %#v", response.ContentParts)
			}
		})
	}
}

func TestSessionToolExecutor_UsesDefaultTimeoutForNonPositivePolicy(t *testing.T) {
	inner := sessionToolExecutorFunc(func(_ context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "ok"}, nil
	})

	response, err := newSessionToolExecutor(inner, 0).Execute(context.Background(), messages.ToolCall{ID: "call", Name: "tool"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.Content != "ok" {
		t.Fatalf("response content = %q, want ok", response.Content)
	}
}
