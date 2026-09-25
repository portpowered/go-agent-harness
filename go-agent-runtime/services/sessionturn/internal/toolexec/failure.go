package toolexec

import (
	"context"
	"errors"
	"fmt"

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
	e.recordDiagnostic(call, err)
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
	if e.diagnostics == nil || err == nil {
		return
	}
	diagnostic := sessiontrace.ToolDiagnostic{ToolCallID: call.ID, ToolName: call.Name, Error: err}
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
