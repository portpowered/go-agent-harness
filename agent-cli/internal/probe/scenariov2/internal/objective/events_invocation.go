package objective

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func (e *BrowserEvidence) onInvocationCreated(ctx eventContext) {
	appendObserved(&e.operations, "invoke", ctx.position)
	if !ctx.hasFields {
		return
	}
	id, idOK := payloadString(ctx.fields, fieldInvocationID)
	if !idOK || id == "" {
		return
	}
	invocation := e.invocationByID[id]
	if invocation == nil {
		invocation = &persistedInvocation{ID: id}
		e.invocations = append(e.invocations, invocation)
		e.invocationByID[id] = invocation
	}
	invocation.Name, _ = payloadString(ctx.fields, "tool_name")
	invocation.ToolRef, _ = payloadString(ctx.fields, fieldToolRef)
	invocation.State = string(webmcp.InvocationCreated)
	invocation.CreatedPos = ctx.position
}

func (e *BrowserEvidence) onInvocationDispatched(ctx eventContext) {
	appendObserved(&e.methods, "WebMCP.invokeTool", ctx.position)
	if !ctx.hasFields {
		return
	}
	if invocation := e.invocationForEvent(ctx.fields); invocation != nil {
		invocation.Input = append(json.RawMessage(nil), ctx.fields["input"]...)
		invocation.State = string(webmcp.InvocationDispatched)
		invocation.DispatchedPos = ctx.position
	}
}

func (e *BrowserEvidence) onInvocationApproval(ctx eventContext) {
	if !ctx.hasFields {
		return
	}
	e.approvalRequested = true
	e.approvalPos = ctx.position
	if invocation := e.invocationForEvent(ctx.fields); invocation != nil {
		invocation.Approval = true
		invocation.ApprovalPos = ctx.position
	}
}

func (e *BrowserEvidence) onInvocationCompleted(ctx eventContext) {
	if !ctx.hasFields {
		return
	}
	if invocation := e.invocationForEvent(ctx.fields); invocation != nil {
		invocation.Output = append(json.RawMessage(nil), ctx.fields["output"]...)
		invocation.State, _ = payloadString(ctx.fields, "status")
		invocation.Terminal = true
		invocation.TerminalPos = ctx.position
	}
}

func (e *BrowserEvidence) onInvocationError(ctx eventContext) {
	if !ctx.hasFields {
		return
	}
	if code, ok := payloadString(ctx.fields, fieldCode); ok && code == string(webmcp.ErrorStaleToolRef) {
		e.stale = true
		e.stalePos = ctx.position
		e.staleToolRef, _ = payloadString(ctx.fields, fieldToolRef)
	}
	if invocation := e.invocationForEvent(ctx.fields); invocation != nil {
		invocation.ErrorCode, _ = payloadString(ctx.fields, fieldCode)
		invocation.State = string(webmcp.InvocationError)
		invocation.Terminal = true
		invocation.TerminalPos = ctx.position
	}
}

func (e *BrowserEvidence) onInvocationCanceled(ctx eventContext) {
	e.canceled = true
	e.canceledPos = ctx.position
	if !ctx.hasFields {
		return
	}
	if invocation := e.invocationForEvent(ctx.fields); invocation != nil {
		invocation.State = string(webmcp.InvocationCanceled)
		invocation.Terminal = true
		invocation.TerminalPos = ctx.position
	}
}

func (e *BrowserEvidence) onInvocationCancel(ctx eventContext) {
	appendObserved(&e.operations, "cancel", ctx.position)
	appendObserved(&e.methods, "WebMCP.cancelInvocation", ctx.position)
}

func (e *BrowserEvidence) invocationForEvent(fields map[string]json.RawMessage) *persistedInvocation {
	id, ok := payloadString(fields, fieldInvocationID)
	if !ok {
		return nil
	}
	return e.invocationByID[id]
}
