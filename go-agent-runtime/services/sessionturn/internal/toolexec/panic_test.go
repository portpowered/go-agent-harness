package toolexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const panicValue = "boom-from-inner-executor"

// requirePanicDiagnostic asserts one diagnostic carries a recovered panic with
// its original value and the recovering goroutine's stack.
func requirePanicDiagnostic(t *testing.T, diagnostics []sessiontrace.ToolDiagnostic, kind sessionturn.Error, value string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Error == nil || !errors.Is(diagnostic.Error, kind) {
			continue
		}
		detail := diagnostic.Error.Error()
		if !strings.Contains(detail, value) || !strings.Contains(detail, "goroutine ") || !strings.Contains(detail, "toolexec") {
			t.Fatalf("panic diagnostic = %q, want value %q and stack", detail, value)
		}
		return
	}
	t.Fatalf("diagnostics = %#v, want %q panic diagnostic", diagnostics, kind)
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

func TestDisplayRecheckPanicsReachOperatorDiagnostics(t *testing.T) {
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
			_, err := New(sessionturn.ToolExecutorRequest{
				Inner: executor, Timeout: shortTimeout, Presentation: cliPresentation(),
				ScreenPermissionRecheckTimeout: shortTimeout, Diagnostics: diagnostics,
			}).Execute(context.Background(), messages.ToolCall{ID: "screen-panic", Name: screenTool})
			if err != nil {
				t.Fatalf("Execute = %v", err)
			}
			requirePanicDiagnostic(t, diagnostics.all(), tc.kind, tc.value)
			if !waitClosed(executor.exited) {
				t.Fatal("screen executor did not exit")
			}
		})
	}
}
