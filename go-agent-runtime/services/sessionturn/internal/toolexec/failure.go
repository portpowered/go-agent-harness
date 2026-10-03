package toolexec

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const errToolFailed sessionturn.Error = "tool execution failed"

// pageSightFallback is the encoded failure used when a host supplies no
// page-sight presentation.
const pageSightFallback = `{"version":2,"status":"error","source":"browser_page","error_code":"page_sight_unavailable","error":"Browser-page sight is unavailable."}`

type presentation struct {
	sessionturn.ToolPresentation
}

func (p presentation) displayTool(name string) bool {
	return p.DisplayTool != nil && p.DisplayTool(name)
}

// IsBrowserTool forwards the wrapped executor's browser routing, so a host
// can group the browser's tools behind this executor.
func (e *Executor) IsBrowserTool(name string) bool {
	if e == nil || e.inner == nil {
		return false
	}
	router, ok := e.inner.(tools.BrowserToolRouter)
	return ok && router.IsBrowserTool(name)
}

func (e *Executor) pageSightTool(call messages.ToolCall) bool {
	if e == nil || e.inner == nil {
		return false
	}
	router, ok := e.inner.(tools.PageSightToolRouter)
	return ok && router.IsPageSightTool(call.Name)
}

// failure records the original error for operators, then projects the
// customer-safe provider result.
func (e *Executor) failure(call messages.ToolCall, err error) messages.ToolCallResponse {
	return e.failureWithCause(call, err, nil)
}

// failureWithCause is failure for an err that a secondary operator-only
// cause accompanied, such as a panic in the timed-out call's permission
// re-check. The call still records one diagnostic, carrying both, and the
// provider sees only err.
func (e *Executor) failureWithCause(call messages.ToolCall, err, cause error) messages.ToolCallResponse {
	e.recordDiagnosticWithCause(call, err, cause)
	err = providerSafe(err)
	if e.pageSightTool(call) {
		return e.pageSightFailure(call)
	}
	if e.presentation.displayTool(call.Name) && e.presentation.DisplayFailure != nil {
		if err == nil {
			err = errToolFailed
		}
		// This is the sole provider-facing presentation of a host-display
		// failure; the typed error remains on the operator diagnostic path.
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: e.presentation.DisplayFailure(err)}
	}
	return genericFailure(call, err)
}

func (e *Executor) recordDiagnostic(call messages.ToolCall, err error) {
	e.recordDiagnosticWithCause(call, err, nil)
}

// recordDiagnosticWithCause records err, joined on one line with an optional
// cause. The display error code is derived from err alone, and a recovered
// panic's stack (from err or cause) goes in the diagnostic's Stack field.
func (e *Executor) recordDiagnosticWithCause(call messages.ToolCall, err, cause error) {
	if e.diagnostics == nil || err == nil {
		return
	}
	diagnostic := sessiontrace.ToolDiagnostic{ToolCallID: call.ID, ToolName: call.Name, Error: err}
	if cause != nil {
		diagnostic.Error = fmt.Errorf("%w; %w", err, cause)
	}
	var panicked *panicError
	if errors.As(diagnostic.Error, &panicked) {
		diagnostic.Stack = panicked.Stack()
	}
	switch {
	case e.pageSightTool(call):
		diagnostic.Source = e.presentation.PageSightSource
		diagnostic.ErrorCode = sessionturn.PageSightUnavailableErrorCode
	case e.presentation.displayTool(call.Name):
		diagnostic.Source = e.presentation.DisplaySource
		if e.presentation.DisplayErrorCode != nil {
			diagnostic.ErrorCode = e.presentation.DisplayErrorCode(err)
		}
	}
	e.diagnostics.RecordSessionToolDiagnostic(diagnostic)
}

func (e *Executor) pageSightFailure(call messages.ToolCall) messages.ToolCallResponse {
	content := pageSightFallback
	if e.presentation.PageSightFailure != nil {
		content = e.presentation.PageSightFailure()
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: content}
}

func genericFailure(call messages.ToolCall, err error) messages.ToolCallResponse {
	if err == nil {
		err = errToolFailed
	}
	message := fmt.Sprintf("tool %q failed", call.Name)
	if errors.Is(err, sessionturn.ErrToolTimeout) || errors.Is(err, context.DeadlineExceeded) {
		message += fmt.Sprintf(" (classification=%s)", sessionturn.ToolTimeoutClassification)
	}
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    fmt.Sprintf("%s: %s", message, err),
	}
}

// panicError preserves a recovered panic's value and goroutine stack for the
// operator diagnostic path. Error is one line (classification and value);
// Stack returns the multi-line stack separately. Unwrap exposes only the
// classification so callers keep matching the stable sentinel.
type panicError struct {
	kind  sessionturn.Error
	value any
	stack []byte
}

func newPanicError(kind sessionturn.Error, value any) *panicError {
	return &panicError{kind: kind, value: value, stack: debug.Stack()}
}

func (p *panicError) Error() string {
	return fmt.Sprintf("%s: %v", p.kind, p.value)
}

// Stack returns the recovering goroutine's stack.
func (p *panicError) Stack() []byte { return p.stack }

func (p *panicError) Unwrap() error { return p.kind }

// providerSafe keeps panic values and stacks on the operator diagnostic path;
// the provider-visible result carries only the panic classification.
func providerSafe(err error) error {
	var panicked *panicError
	if errors.As(err, &panicked) {
		return panicked.kind
	}
	return err
}
