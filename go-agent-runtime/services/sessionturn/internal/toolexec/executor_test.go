package toolexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	timedOutText       = "timed out"
	classificationText = "classification=" + sessionturn.ToolTimeoutClassification
	failureTool        = "failure_case"
	briefTimeout       = 10 * time.Millisecond
	fastBudget         = 3 * time.Second
	otherFastBudget    = 7 * time.Second
	longBudget         = 11 * time.Second
	otherLongBudget    = 19 * time.Second
)

func TestPreservesCallAndCorrelatesSuccess(t *testing.T) {
	call := messages.ToolCall{ID: "s14-call-42", Name: "distinctive_session_tool", Arguments: `{"city":"São Paulo","units":"metric"}`}
	got := make(chan messages.ToolCall, 1)
	inner := executorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		got <- call
		return messages.ToolCallResponse{ToolCallID: "wrong", Name: "wrong", Content: "s14-result-payload"}, nil
	})
	response, err := New(sessionturn.ToolExecutorRequest{Inner: inner, Timeout: time.Second}).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if received := <-got; received != call {
		t.Fatalf("executor call = %#v, want %#v", received, call)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name || response.Content != "s14-result-payload" {
		t.Fatalf("response = %#v", response)
	}
}

func failureCases(call messages.ToolCall) []struct {
	name     string
	executor messages.ToolExecutor
	timeout  time.Duration
	contains string
} {
	return []struct {
		name     string
		executor messages.ToolExecutor
		timeout  time.Duration
		contains string
	}{
		{name: "unknown tool", executor: executorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{}, errors.New(`tool "missing_tool" not found`)
		}), contains: "not found"},
		{name: "malformed arguments", executor: executorFunc(func(_ context.Context, got messages.ToolCall) (messages.ToolCallResponse, error) {
			if got.Arguments != call.Arguments {
				return messages.ToolCallResponse{}, errors.New("arguments were changed")
			}
			return messages.ToolCallResponse{}, errors.New("failed to parse tool arguments")
		}), contains: "parse tool arguments"},
		{name: "executor panic", executor: executorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
			panic("panic detail must stay inside this invocation")
		}), contains: "panicked"},
		{name: "executor timeout", timeout: briefTimeout, executor: blockingExecutor(nil, nil), contains: timedOutText},
		{name: "not configured", contains: "not configured"},
	}
}

func TestConvertsFailuresToCorrelatedResults(t *testing.T) {
	call := messages.ToolCall{ID: "s4-call-1", Name: failureTool, Arguments: `{not-json}`}
	for _, tc := range failureCases(call) {
		t.Run(tc.name, func(t *testing.T) {
			response, err := New(sessionturn.ToolExecutorRequest{Inner: tc.executor, Timeout: tc.timeout}).Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			if response.ToolCallID != call.ID || response.Name != call.Name || len(response.ContentParts) != 0 {
				t.Fatalf("response = %#v", response)
			}
			if !strings.Contains(response.Content, tc.contains) || !strings.Contains(response.Content, `tool "`+failureTool+`" failed`) {
				t.Fatalf("failure response = %q, want %q", response.Content, tc.contains)
			}
		})
	}
}

func TestNilExecutorAndContext(t *testing.T) {
	var executor *Executor
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "id", Name: "tool"})
	if err != nil || !strings.Contains(response.Content, string(errNotConfigured)) {
		t.Fatalf("nil executor = %#v, %v", response, err)
	}
	inner := executorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		if ctx == nil {
			return messages.ToolCallResponse{}, errors.New("nil context")
		}
		return messages.ToolCallResponse{Content: "ok"}, nil
	})
	var missing context.Context
	response, err = New(sessionturn.ToolExecutorRequest{Inner: inner}).Execute(missing, messages.ToolCall{ID: "id"})
	if err != nil || response.Content != "ok" {
		t.Fatalf("nil context = %#v, %v", response, err)
	}
}

func TestCooperativeWorkerExitsAfterTimeout(t *testing.T) {
	exited := make(chan struct{})
	response, err := New(sessionturn.ToolExecutorRequest{Inner: blockingExecutor(nil, exited), Timeout: briefTimeout}).Execute(context.Background(), messages.ToolCall{ID: "t-call", Name: "t-tool"})
	if err != nil || !strings.Contains(response.Content, timedOutText) {
		t.Fatalf("response = %q, %v", response.Content, err)
	}
	if !waitClosed(exited) {
		t.Fatal("timeout worker did not exit after Execute returned")
	}
}

func TestInteractivePolicyTimeoutClassifiesAndCancels(t *testing.T) {
	policy := fakePolicy{settings: tools.InteractiveToolPolicySettings{FastReadTimeout: shortTimeout, LongRunningTimeout: time.Second}}
	started, exited := make(chan struct{}), make(chan struct{})
	call := messages.ToolCall{ID: "policy-timeout-call", Name: "policy_slow_read", Arguments: `{}`}
	begin := time.Now()
	response, err := New(sessionturn.ToolExecutorRequest{Inner: blockingExecutor(started, exited), Policy: policy}).Execute(context.Background(), call)
	if err != nil || response.ToolCallID != call.ID || response.Name != call.Name {
		t.Fatalf("response = %#v, %v", response, err)
	}
	if !strings.Contains(response.Content, classificationText) || !strings.Contains(response.Content, "tool execution timed out") {
		t.Fatalf("response = %q, want classification and explanation", response.Content)
	}
	if time.Since(begin) > responseBound {
		t.Fatal("interactive timeout exceeded the bounded tolerance")
	}
	if !waitClosed(started) || !waitClosed(exited) {
		t.Fatal("policy-selected worker did not start and exit")
	}
}

func TestSIGINTCancellationDoesNotRecordFailedResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log := &recordingLifecycle{}
	executor := New(sessionturn.ToolExecutorRequest{
		Inner: blockingExecutor(nil, nil), Timeout: time.Second, Cancellation: sigintIntent{},
		Lifecycle: sessionturn.ToolLifecycle{Recording: namedObserver{name: "recording", log: log}},
	})
	response, err := executor.Execute(ctx, messages.ToolCall{ID: "sigint-call", Name: "sleep"})
	if !errors.Is(err, context.Canceled) || response.ToolCallID != "sigint-call" {
		t.Fatalf("SIGINT error = %v, response=%#v", err, response)
	}
	if got := log.snapshot(); len(got) != 1 || got[0] != "recording.call" {
		t.Fatalf("lifecycle = %v, want only the provider call", got)
	}
}

func TestSIGINTCancellationFromCompletedWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := executorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		cancel()
		return messages.ToolCallResponse{}, context.Canceled
	})
	executor := New(sessionturn.ToolExecutorRequest{Inner: inner, Timeout: time.Minute, Cancellation: sigintIntent{}})
	if _, err := executor.Execute(ctx, messages.ToolCall{ID: "sigint"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCallerCancellationWithoutSIGINTIsCorrelatedFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := New(sessionturn.ToolExecutorRequest{Inner: blockingExecutor(nil, nil), Timeout: time.Minute}).Execute(ctx, messages.ToolCall{ID: "c", Name: "tool"})
	if err != nil || !strings.Contains(response.Content, string(errCanceled)) || strings.Contains(response.Content, classificationText) {
		t.Fatalf("response = %q, %v", response.Content, err)
	}
}

func deadlineRemaining(t *testing.T, request sessionturn.ToolExecutorRequest, name string) time.Duration {
	t.Helper()
	remaining := make(chan time.Duration, 1)
	request.Inner = executorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return messages.ToolCallResponse{}, errors.New("missing per-call deadline")
		}
		remaining <- time.Until(deadline)
		return messages.ToolCallResponse{Content: "ok"}, nil
	})
	response, err := New(request).Execute(context.Background(), messages.ToolCall{ID: name, Name: name})
	if err != nil || response.Content != "ok" {
		t.Fatalf("Execute = %#v, %v", response, err)
	}
	return <-remaining
}

func TestDefaultWrapperAppliesProductionBound(t *testing.T) {
	if sessionturn.DefaultToolExecutionTimeout != time.Minute {
		t.Fatalf("default timeout = %s, want 60s", sessionturn.DefaultToolExecutionTimeout)
	}
	remaining := deadlineRemaining(t, sessionturn.ToolExecutorRequest{}, "d-tool")
	if remaining <= 0 || remaining > time.Minute || remaining < time.Minute-time.Second {
		t.Fatalf("default deadline = %s, want within 1s of 60s", remaining)
	}
}

func TestExecutorUsesIndependentSessionBudgets(t *testing.T) {
	longRunning := map[string]bool{"exec": true}
	policyA := fakePolicy{settings: tools.InteractiveToolPolicySettings{FastReadTimeout: fastBudget, LongRunningTimeout: longBudget}, longRunning: longRunning}
	policyB := fakePolicy{settings: tools.InteractiveToolPolicySettings{FastReadTimeout: otherFastBudget, LongRunningTimeout: otherLongBudget}, longRunning: longRunning}
	checks := []struct {
		policy tools.InteractiveToolPolicy
		name   string
		want   time.Duration
	}{
		{policyA, "read_file", fastBudget}, {policyB, "read_file", otherFastBudget},
		{policyA, "exec", longBudget}, {policyB, "exec", otherLongBudget},
	}
	for _, check := range checks {
		remaining := deadlineRemaining(t, sessionturn.ToolExecutorRequest{Policy: check.policy}, check.name)
		if remaining > check.want || remaining < check.want-time.Second {
			t.Fatalf("%s deadline = %s, want close to %s", check.name, remaining, check.want)
		}
	}
	if remaining := deadlineRemaining(t, sessionturn.ToolExecutorRequest{Policy: policyA, Timeout: otherFastBudget}, "exec"); remaining < otherFastBudget-time.Second || remaining > otherFastBudget {
		t.Fatalf("explicit timeout = %s, want override of policy", remaining)
	}
}

func TestLifecycleOrderAndFailureClassification(t *testing.T) {
	log := &recordingLifecycle{}
	contents := []string{"plain", `{"version":"webmcp.tool-result.v1","ok":false}`, `{"version":"webmcp.tool-result.v1","ok":true}`, "refused"}
	next := make(chan string, len(contents))
	for _, content := range contents {
		next <- content
	}
	executor := New(sessionturn.ToolExecutorRequest{
		Inner: executorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{Content: <-next}, nil
		}),
		Presentation: cliPresentation(),
		Lifecycle: sessionturn.ToolLifecycle{
			Recording: namedObserver{name: "recording", log: log}, Runtime: namedObserver{name: "runtime", log: log},
			Progress: progressObserver{log: log},
		},
	})
	for range contents {
		if _, err := executor.Execute(context.Background(), messages.ToolCall{ID: "id"}); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	}
	want := []string{"runtime.call", "progress.call.id", "progress.begin", "recording.call", "runtime.result", "recording.result", "progress.end"}
	got := log.snapshot()
	for i, event := range want {
		if got[i] != event {
			t.Fatalf("lifecycle = %v, want prefix %v", got, want)
		}
	}
	wantFailed := []bool{false, false, true, true, false, false, true, true}
	for i, failed := range wantFailed {
		if log.results[i] != failed {
			t.Fatalf("failed flags = %v, want %v", log.results, wantFailed)
		}
	}
}
