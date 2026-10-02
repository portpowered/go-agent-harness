package tools

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// invocationStateErrorCode maps a terminal invocation state to the error code
// reported when the page supplied no known code.
func invocationStateErrorCode(state webmcp.InvocationState) webmcp.ErrorCode {
	switch state {
	case webmcp.InvocationCanceled:
		return webmcp.ErrorInvocationCanceled
	case webmcp.InvocationTimedOut:
		return webmcp.ErrorInvocationTimedOut
	case webmcp.InvocationOrphaned:
		return webmcp.ErrorInvocationOrphaned
	case webmcp.InvocationCreated, webmcp.InvocationAwaitingApproval, webmcp.InvocationQueued,
		webmcp.InvocationDispatching, webmcp.InvocationDispatched, webmcp.InvocationCompleted,
		webmcp.InvocationError, webmcp.InvocationPolicyDenied:
		return webmcp.ErrorInvocationFailed
	default:
		return webmcp.ErrorInvocationFailed
	}
}
