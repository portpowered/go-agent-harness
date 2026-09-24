package toolexec

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	screenTool       = "screen"
	pageTool         = "show_page"
	displaySource    = "screen"
	pageSource       = "browser_page"
	deniedCode       = "screen_recording_permission_denied"
	displayPrefix    = "display-failure: "
	pageSightContent = "page-sight-failure"
	shortTimeout     = 15 * time.Millisecond
	exitBound        = time.Second
	responseBound    = 500 * time.Millisecond
)

type executorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f executorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

func blockingExecutor(started, exited chan struct{}) executorFunc {
	return func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		if started != nil {
			close(started)
		}
		if exited != nil {
			defer close(exited)
		}
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

// deniedError mimics the CLI's typed display error.
type deniedError struct{ reason string }

func (e *deniedError) Error() string { return "show permission re-check (denied): " + e.reason }

// cliPresentation mimics the CLI projection: display failures are safe text,
// display diagnostics are classified, page sight has its own envelope.
func cliPresentation() sessionturn.ToolPresentation {
	return sessionturn.ToolPresentation{
		DisplayTool:      func(name string) bool { return name == screenTool },
		DisplayFailure:   func(err error) string { return displayPrefix + displayCode(err) },
		DisplayErrorCode: displayCode,
		DisplayPermissionDenied: func(permission tools.DisplayPermission) error {
			return &deniedError{reason: permission.Reason}
		},
		FailedContent:    func(content string) bool { return content == "refused" },
		PageSightFailure: func() string { return pageSightContent },
		DisplaySource:    displaySource,
		PageSightSource:  pageSource,
	}
}

func displayCode(err error) string {
	var denied *deniedError
	if errors.As(err, &denied) {
		return deniedCode
	}
	return "capture_failed"
}

// fakePolicy is a fixed-budget interactive policy.
type fakePolicy struct {
	settings    tools.InteractiveToolPolicySettings
	longRunning map[string]bool
}

func (p fakePolicy) Settings() tools.InteractiveToolPolicySettings { return p.settings }
func (p fakePolicy) ClassForTool(name string) tools.InteractiveToolClass {
	if p.longRunning[name] {
		return tools.InteractiveToolClassBoundedLongRunning
	}
	return tools.InteractiveToolClassFastRead
}
func (p fakePolicy) TimeoutForTool(name string) time.Duration {
	if p.ClassForTool(name) == tools.InteractiveToolClassBoundedLongRunning {
		return p.settings.LongRunningTimeout
	}
	return p.settings.FastReadTimeout
}
func (p fakePolicy) Clone() tools.InteractiveToolPolicy { return p }
func (p fakePolicy) Validate() error                    { return nil }

// recordingLifecycle records observer order across the three lifecycle roles.
type recordingLifecycle struct {
	mu      sync.Mutex
	events  []string
	results []bool
}

func (r *recordingLifecycle) add(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recordingLifecycle) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type namedObserver struct {
	name string
	log  *recordingLifecycle
}

func (o namedObserver) ObserveToolCall(messages.ToolCall) { o.log.add(o.name + ".call") }
func (o namedObserver) ObserveToolResult(_ messages.ToolCall, _ messages.ToolCallResponse, failed bool) {
	o.log.add(o.name + ".result")
	o.log.mu.Lock()
	o.log.results = append(o.log.results, failed)
	o.log.mu.Unlock()
}

type progressObserver struct{ log *recordingLifecycle }

func (o progressObserver) ObserveProviderToolCallWithID(id, _ string) {
	o.log.add("progress.call." + id)
}
func (o progressObserver) BeginLocalToolExecution() { o.log.add("progress.begin") }
func (o progressObserver) EndLocalToolExecution()   { o.log.add("progress.end") }

type sigintIntent struct{}

func (sigintIntent) SIGINTReceived() bool { return true }

type diagnosticRecorder struct {
	mu          sync.Mutex
	diagnostics []sessiontrace.ToolDiagnostic
}

func (r *diagnosticRecorder) RecordSessionToolDiagnostic(diagnostic sessiontrace.ToolDiagnostic) {
	r.mu.Lock()
	r.diagnostics = append(r.diagnostics, diagnostic)
	r.mu.Unlock()
}

func (r *diagnosticRecorder) all() []sessiontrace.ToolDiagnostic {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessiontrace.ToolDiagnostic(nil), r.diagnostics...)
}

func waitClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(exitBound):
		return false
	}
}
