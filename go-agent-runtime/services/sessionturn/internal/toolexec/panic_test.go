package toolexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const panicValue = "boom-from-inner-executor"

// requirePanicDiagnostic asserts one diagnostic carries a recovered panic:
// its original value on one line in Error, and the recovering goroutine's
// stack in the separate Stack field.
func requirePanicDiagnostic(t *testing.T, diagnostics []sessiontrace.ToolDiagnostic, kind sessionturn.Error, value string) sessiontrace.ToolDiagnostic {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Error == nil || !errors.Is(diagnostic.Error, kind) {
			continue
		}
		detail := diagnostic.Error.Error()
		if !strings.Contains(detail, value) || strings.Contains(detail, "\n") {
			t.Fatalf("panic diagnostic = %q, want value %q on one line", detail, value)
		}
		stack := string(diagnostic.Stack)
		if !strings.Contains(stack, "goroutine ") || !strings.Contains(stack, "toolexec") {
			t.Fatalf("panic diagnostic stack = %q, want the recovering goroutine's stack", stack)
		}
		return diagnostic
	}
	t.Fatalf("diagnostics = %#v, want %q panic diagnostic", diagnostics, kind)
	return sessiontrace.ToolDiagnostic{}
}

func TestExecutorPanicPreservesValueAndStackForOperators(t *testing.T) {
	diagnostics := &diagnosticRecorder{}
	inner := executorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		panic(panicValue)
	})
	call := messages.ToolCall{ID: "panic-call", Name: failureTool}
	response, err := New(sessionturn.ToolExecutorRequest{Inner: inner, Diagnostics: diagnostics}).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	requirePanicDiagnostic(t, diagnostics.all(), errPanicked, panicValue)
	if !strings.Contains(response.Content, string(errPanicked)) || strings.Contains(response.Content, panicValue) || strings.Contains(response.Content, "goroutine ") {
		t.Fatalf("provider content = %q, want classification without panic detail", response.Content)
	}
}

// A panic in the timed-out call's permission re-check is part of that call's
// single timeout diagnostic, not a second diagnostic for the same call.
func TestDisplayRecheckPanicsJoinTheTimeoutDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  sessionturn.Error
		value string
		set   func(*screenExecutor)
	}{
		{name: "support probe", kind: errRecheckSupportPanicked, value: "support probe failed", set: func(e *screenExecutor) { e.panicSupport = true }},
		{name: "recheck", kind: errRecheckPanicked, value: "recheck failed", set: func(e *screenExecutor) { e.panicCheck = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newScreenExecutor(tools.DisplayPermission{State: tools.DisplayPermissionDenied})
			tc.set(executor)
			diagnostics := &diagnosticRecorder{}
			response, err := New(sessionturn.ToolExecutorRequest{
				Inner: executor, Timeout: shortTimeout, Presentation: cliPresentation(),
				ScreenPermissionRecheckTimeout: shortTimeout, Diagnostics: diagnostics,
			}).Execute(context.Background(), messages.ToolCall{ID: "screen-panic", Name: screenTool})
			if err != nil {
				t.Fatalf("Execute = %v", err)
			}
			if !waitClosed(executor.exited) {
				t.Fatal("screen executor did not exit")
			}
			got := diagnostics.all()
			if len(got) != 1 {
				t.Fatalf("diagnostics = %#v, want exactly one for the call", got)
			}
			diagnostic := requirePanicDiagnostic(t, got, tc.kind, tc.value)
			if !errors.Is(diagnostic.Error, sessionturn.ErrToolTimeout) || diagnostic.ErrorCode != "capture_failed" {
				t.Fatalf("diagnostic = %#v, want the timeout with its display code", diagnostic)
			}
			if response.Content != displayPrefix+"capture_failed" {
				t.Fatalf("content = %q, want the timeout presentation", response.Content)
			}
		})
	}
}

// A re-check that panics after its bound has expired, when the call has
// already reported its timeout, still reaches operators.
func TestDisplayRecheckPanicAfterTimeoutIsRecorded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executor := newScreenExecutor(tools.DisplayPermission{State: tools.DisplayPermissionDenied})
		executor.panicLate = true
		diagnostics := &diagnosticRecorder{}
		response, err := New(sessionturn.ToolExecutorRequest{
			Inner: executor, Timeout: shortTimeout, Presentation: cliPresentation(),
			ScreenPermissionRecheckTimeout: shortTimeout, Diagnostics: diagnostics,
		}).Execute(context.Background(), messages.ToolCall{ID: "screen-late-panic", Name: screenTool})
		if err != nil || response.Content != displayPrefix+"capture_failed" {
			t.Fatalf("Execute = %q, %v; want the timeout presentation", response.Content, err)
		}
		synctest.Wait()
		// Once the call gives up on the re-check, the worker and the caller
		// record concurrently, so the two diagnostics arrive in either order.
		got := diagnostics.all()
		timeouts := 0
		for _, diagnostic := range got {
			if errors.Is(diagnostic.Error, sessionturn.ErrToolTimeout) && !errors.Is(diagnostic.Error, errRecheckPanicked) {
				timeouts++
			}
		}
		if len(got) != 2 || timeouts != 1 {
			t.Fatalf("diagnostics = %#v, want one timeout and one late panic", got)
		}
		requirePanicDiagnostic(t, got, errRecheckPanicked, "late recheck failed")
	})
}

func TestPanicErrorKeepsStackOutOfItsMessage(t *testing.T) {
	err := newPanicError(errPanicked, "value")
	if got := err.Error(); got != string(errPanicked)+": value" {
		t.Fatalf("Error() = %q, want one line", got)
	}
	if !strings.Contains(string(err.Stack()), "goroutine ") {
		t.Fatalf("Stack() = %q, want a goroutine stack", err.Stack())
	}
}
