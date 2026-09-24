package toolexec

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const deniedReason = "permission became ineffective during capture"

// screenExecutor times out every call and answers the permission re-check.
type screenExecutor struct {
	mu           sync.Mutex
	permission   tools.DisplayPermission
	recheckErr   error
	recheckWait  <-chan struct{}
	rechecks     int
	supported    bool
	panicSupport bool
	panicCheck   bool
	pageSight    bool
	started      chan struct{}
	exited       chan struct{}
	startOnce    sync.Once
}

func newScreenExecutor(permission tools.DisplayPermission) *screenExecutor {
	return &screenExecutor{permission: permission, supported: true, started: make(chan struct{}), exited: make(chan struct{})}
}

func (e *screenExecutor) Execute(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
	e.startOnce.Do(func() { close(e.started) })
	defer func() {
		select {
		case <-e.exited:
		default:
			close(e.exited)
		}
	}()
	<-ctx.Done()
	return messages.ToolCallResponse{}, ctx.Err()
}

func (e *screenExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	if e.panicSupport {
		panic("support probe failed")
	}
	return e.supported
}

func (e *screenExecutor) RecheckScreenRecordingPermission(ctx context.Context) (tools.DisplayPermission, error) {
	e.mu.Lock()
	e.rechecks++
	permission, recheckErr, wait := e.permission, e.recheckErr, e.recheckWait
	e.mu.Unlock()
	if e.panicCheck {
		panic("recheck failed")
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return tools.DisplayPermission{}, ctx.Err()
		}
	}
	return permission, recheckErr
}

func (e *screenExecutor) IsPageSightTool(name string) bool { return e.pageSight && name == pageTool }

func (e *screenExecutor) recheckCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rechecks
}

func TestScreenTimeoutDeniedRecheckUsesOneCorrelatedPermissionResult(t *testing.T) {
	executor := newScreenExecutor(tools.DisplayPermission{State: tools.DisplayPermissionDenied, Reason: deniedReason})
	diagnostics := &diagnosticRecorder{}
	call := messages.ToolCall{ID: "screen-timeout-denied", Name: screenTool, Arguments: `{"action":"screenshot"}`}
	response, err := New(sessionturn.ToolExecutorRequest{Inner: executor, Timeout: shortTimeout, Presentation: cliPresentation(), Diagnostics: diagnostics}).Execute(context.Background(), call)
	if err != nil || response.ToolCallID != call.ID || response.Name != call.Name || len(response.ContentParts) != 0 {
		t.Fatalf("response = %#v, %v", response, err)
	}
	if response.Content != displayPrefix+deniedCode {
		t.Fatalf("content = %q, want customer-safe denial", response.Content)
	}
	if executor.recheckCount() != 1 || !waitClosed(executor.exited) {
		t.Fatalf("rechecks = %d, want exactly one and an exited worker", executor.recheckCount())
	}
	got := diagnostics.all()
	var denied *deniedError
	if len(got) != 1 || got[0].ToolCallID != call.ID || got[0].Source != displaySource || got[0].ErrorCode != deniedCode || !errors.As(got[0].Error, &denied) || denied.reason != deniedReason {
		t.Fatalf("diagnostics = %#v, want original typed denial", got)
	}
}

type recheckCase struct {
	name       string
	permission tools.DisplayPermission
	recheckErr error
	wait       bool
	callName   string
	prepare    func(*screenExecutor)
	rechecks   int
}

func recheckCases() []recheckCase {
	return []recheckCase{
		{name: "granted", permission: tools.DisplayPermission{State: tools.DisplayPermissionGranted}, rechecks: 1},
		{name: "unavailable", permission: tools.DisplayPermission{State: tools.DisplayPermissionUnavailable}, rechecks: 1},
		{name: "failed", recheckErr: errors.New("permission service failed"), rechecks: 1},
		{name: "unrelated tool", permission: tools.DisplayPermission{State: tools.DisplayPermissionDenied}, callName: pageTool},
		{name: "bounded inconclusive", wait: true, rechecks: 1},
		{name: "unsupported", permission: tools.DisplayPermission{State: tools.DisplayPermissionDenied}, prepare: func(e *screenExecutor) { e.supported = false }},
		{name: "support panic", permission: tools.DisplayPermission{State: tools.DisplayPermissionDenied}, prepare: func(e *screenExecutor) { e.panicSupport = true }},
		{name: "recheck panic", permission: tools.DisplayPermission{State: tools.DisplayPermissionDenied}, prepare: func(e *screenExecutor) { e.panicCheck = true }, rechecks: 1},
	}
}

func TestScreenTimeoutRecheckPreservesTimeoutForNonDenial(t *testing.T) {
	for _, tc := range recheckCases() {
		t.Run(tc.name, func(t *testing.T) { runRecheckCase(t, tc) })
	}
}

func recheckExecutor(t *testing.T, tc recheckCase) *screenExecutor {
	t.Helper()
	executor := newScreenExecutor(tc.permission)
	executor.recheckErr = tc.recheckErr
	if tc.prepare != nil {
		tc.prepare(executor)
	}
	if tc.wait {
		unblock := make(chan struct{})
		executor.recheckWait = unblock
		t.Cleanup(func() { close(unblock) })
	}
	return executor
}

func runRecheckCase(t *testing.T, tc recheckCase) {
	executor := recheckExecutor(t, tc)
	name := tc.callName
	if name == "" {
		name = screenTool
	}
	begin := time.Now()
	response, err := New(sessionturn.ToolExecutorRequest{Inner: executor, Timeout: shortTimeout, Presentation: cliPresentation()}).Execute(context.Background(), messages.ToolCall{ID: "screen-" + tc.name, Name: name})
	if err != nil || time.Since(begin) > responseBound {
		t.Fatalf("Execute = %v after %s", err, time.Since(begin))
	}
	want := displayPrefix + "capture_failed"
	if tc.callName != "" {
		want = classificationText
	}
	if !strings.Contains(response.Content, want) {
		t.Fatalf("content = %q, want %q", response.Content, want)
	}
	if executor.recheckCount() != tc.rechecks || !waitClosed(executor.exited) {
		t.Fatalf("rechecks = %d, want %d", executor.recheckCount(), tc.rechecks)
	}
}

func TestPageSightFailureUsesPresentationAndFallback(t *testing.T) {
	failing := &pageSightExecutor{}
	diagnostics := &diagnosticRecorder{}
	call := messages.ToolCall{ID: "page", Name: pageTool}
	response, err := New(sessionturn.ToolExecutorRequest{Inner: failing, Presentation: cliPresentation(), Diagnostics: diagnostics}).Execute(context.Background(), call)
	if err != nil || response.Content != pageSightContent || response.ToolCallID != call.ID {
		t.Fatalf("page sight = %#v, %v", response, err)
	}
	got := diagnostics.all()
	if len(got) != 1 || got[0].Source != pageSource || got[0].ErrorCode != sessionturn.PageSightUnavailableErrorCode {
		t.Fatalf("diagnostics = %#v", got)
	}
	response, err = New(sessionturn.ToolExecutorRequest{Inner: failing}).Execute(context.Background(), call)
	if err != nil || response.Content != pageSightFallback {
		t.Fatalf("fallback = %q, %v", response.Content, err)
	}
}

func TestGenericDiagnosticsAndNilDisplayFailure(t *testing.T) {
	diagnostics := &diagnosticRecorder{}
	cause := errors.New("tool broke")
	inner := executorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, cause
	})
	presentation := sessionturn.ToolPresentation{DisplayTool: func(string) bool { return true }}
	response, err := New(sessionturn.ToolExecutorRequest{Inner: inner, Presentation: presentation, Diagnostics: diagnostics}).Execute(context.Background(), messages.ToolCall{ID: "g", Name: screenTool})
	if err != nil || !strings.Contains(response.Content, cause.Error()) {
		t.Fatalf("generic = %q, %v", response.Content, err)
	}
	if got := diagnostics.all(); len(got) != 1 || got[0].ErrorCode != "" || !errors.Is(got[0].Error, cause) {
		t.Fatalf("diagnostics = %#v", got)
	}
	if content := genericFailure(messages.ToolCall{Name: "x"}, nil).Content; !strings.Contains(content, string(errToolFailed)) {
		t.Fatalf("nil failure = %q", content)
	}
	if content := genericFailure(messages.ToolCall{Name: "x"}, context.DeadlineExceeded).Content; !strings.Contains(content, classificationText) {
		t.Fatalf("deadline failure = %q", content)
	}
	if err := contextFailure(errors.New("other")); err == nil || !strings.Contains(err.Error(), "tool execution stopped") {
		t.Fatalf("context failure = %v", err)
	}
}

type pageSightExecutor struct{}

func (pageSightExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{}, errors.New("page executor failed")
}

func (pageSightExecutor) IsPageSightTool(name string) bool { return name == pageTool }
