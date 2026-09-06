package webmcp

import (
	"errors"

	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

var (
	// These sentinels describe host broker state and stay at the Chrome/session
	// adapter edge. Reusable tools code returns classified result errors instead
	// of exposing broker lifecycle details.
	ErrClosed             = errors.New("webmcp: closed")
	ErrBrowserNotFound    = errors.New("webmcp: browser not found")
	ErrTargetNotFound     = errors.New("webmcp: target not found")
	ErrInvocationNotFound = errors.New("webmcp: invocation not found")
	ErrEventBufferFull    = errors.New("webmcp: event buffer full")
	ErrInvalidToolInput   = errors.New("webmcp: invalid tool input")
	ErrStaleSelection     = errors.New("webmcp: stale selection")
	ErrStaleToolRef       = errors.New("webmcp: stale tool ref")
	ErrCloseTimeout       = errors.New("webmcp: close timed out")
	ErrClosePanic         = errors.New("webmcp: close panicked")
)

type ErrorCode = runtimeTools.ErrorCode
type ClassifiedError = runtimeTools.ClassifiedError

const (
	ErrorWebMCPDisabled       = runtimeTools.ErrorWebMCPDisabled
	ErrorEndpointNotFound     = runtimeTools.ErrorEndpointNotFound
	ErrorEndpointUnreachable  = runtimeTools.ErrorEndpointUnreachable
	ErrorRemoteEndpointDenied = runtimeTools.ErrorRemoteEndpointDenied
	ErrorBrowserProtocol      = runtimeTools.ErrorBrowserProtocol
	ErrorUnsupportedWebMCP    = runtimeTools.ErrorUnsupportedWebMCP
	ErrorNoEligibleTab        = runtimeTools.ErrorNoEligibleTab
	ErrorAmbiguousBrowser     = runtimeTools.ErrorAmbiguousBrowser
	ErrorAmbiguousTab         = runtimeTools.ErrorAmbiguousTab
	ErrorStaleSelection       = runtimeTools.ErrorStaleSelection
	ErrorStaleToolRef         = runtimeTools.ErrorStaleToolRef
	ErrorOriginDenied         = runtimeTools.ErrorOriginDenied
	ErrorApprovalRequired     = runtimeTools.ErrorApprovalRequired
	ErrorApprovalDenied       = runtimeTools.ErrorApprovalDenied
	ErrorInvalidToolInput     = runtimeTools.ErrorInvalidToolInput
	ErrorResultTooLarge       = runtimeTools.ErrorResultTooLarge
	ErrorTargetAttachFailed   = runtimeTools.ErrorTargetAttachFailed
	ErrorTargetDetached       = runtimeTools.ErrorTargetDetached
	ErrorPageNavigated        = runtimeTools.ErrorPageNavigated
	ErrorInvocationFailed     = runtimeTools.ErrorInvocationFailed
	ErrorInvocationCanceled   = runtimeTools.ErrorInvocationCanceled
	ErrorInvocationTimedOut   = runtimeTools.ErrorInvocationTimedOut
	ErrorInvocationOrphaned   = runtimeTools.ErrorInvocationOrphaned
	ErrorBrowserDisconnected  = runtimeTools.ErrorBrowserDisconnected
)

func IsKnownErrorCode(code ErrorCode) bool { return code.IsKnown() }

func NewClassifiedError(code ErrorCode, message string, details map[string]any) *ClassifiedError {
	return browserContract().NewClassifiedError(code, message, details)
}

func ResultErrorFor(err error, fallback ErrorCode, details map[string]any) ToolResultError {
	return browserContract().ResultErrorFor(err, fallback, details)
}

func DefaultErrorMessage(code ErrorCode) string { return browserContract().DefaultErrorMessage(code) }

func ContextErrorCode(err error) ErrorCode { return browserContract().ContextErrorCode(err) }
