package webmcp

import (
	"context"
	"errors"
)

var (
	ErrClosed             = errors.New("webmcp: closed")
	ErrBrowserNotFound    = errors.New("webmcp: browser not found")
	ErrTargetNotFound     = errors.New("webmcp: target not found")
	ErrInvocationNotFound = errors.New("webmcp: invocation not found")
	ErrEventBufferFull    = errors.New("webmcp: event buffer full")
	ErrInvalidToolInput   = errors.New("webmcp: invalid tool input")
	ErrStaleSelection     = errors.New("webmcp: stale selection")
	ErrStaleToolRef       = errors.New("webmcp: stale tool ref")
)

// ErrorCode is the model-facing, stable WebMCP failure vocabulary.
type ErrorCode string

const (
	ErrorWebMCPDisabled       ErrorCode = "webmcp_disabled"
	ErrorEndpointNotFound     ErrorCode = "endpoint_not_found"
	ErrorEndpointUnreachable  ErrorCode = "endpoint_unreachable"
	ErrorRemoteEndpointDenied ErrorCode = "remote_endpoint_denied"
	ErrorBrowserProtocol      ErrorCode = "browser_protocol_invalid"
	ErrorUnsupportedWebMCP    ErrorCode = "unsupported_webmcp"
	ErrorNoEligibleTab        ErrorCode = "no_eligible_tab"
	ErrorAmbiguousBrowser     ErrorCode = "ambiguous_browser"
	ErrorAmbiguousTab         ErrorCode = "ambiguous_tab"
	ErrorStaleSelection       ErrorCode = "stale_selection"
	ErrorStaleToolRef         ErrorCode = "stale_tool_ref"
	ErrorOriginDenied         ErrorCode = "origin_denied"
	ErrorApprovalRequired     ErrorCode = "approval_required"
	ErrorApprovalDenied       ErrorCode = "approval_denied"
	ErrorInvalidToolInput     ErrorCode = "invalid_tool_input"
	ErrorResultTooLarge       ErrorCode = "result_too_large"
	ErrorTargetAttachFailed   ErrorCode = "target_attach_failed"
	ErrorTargetDetached       ErrorCode = "target_detached"
	ErrorPageNavigated        ErrorCode = "page_navigated"
	ErrorInvocationFailed     ErrorCode = "invocation_failed"
	ErrorInvocationCanceled   ErrorCode = "invocation_canceled"
	ErrorInvocationTimedOut   ErrorCode = "invocation_timed_out"
	ErrorInvocationOrphaned   ErrorCode = "invocation_orphaned"
	ErrorBrowserDisconnected  ErrorCode = "browser_disconnected"
)

var knownErrorCodes = map[ErrorCode]struct{}{
	ErrorWebMCPDisabled: {}, ErrorEndpointNotFound: {}, ErrorEndpointUnreachable: {},
	ErrorRemoteEndpointDenied: {}, ErrorBrowserProtocol: {}, ErrorUnsupportedWebMCP: {},
	ErrorNoEligibleTab: {}, ErrorAmbiguousBrowser: {}, ErrorAmbiguousTab: {},
	ErrorStaleSelection: {}, ErrorStaleToolRef: {}, ErrorOriginDenied: {},
	ErrorApprovalRequired: {}, ErrorApprovalDenied: {}, ErrorInvalidToolInput: {},
	ErrorResultTooLarge: {}, ErrorTargetAttachFailed: {}, ErrorTargetDetached: {},
	ErrorPageNavigated: {}, ErrorInvocationFailed: {}, ErrorInvocationCanceled: {},
	ErrorInvocationTimedOut: {}, ErrorInvocationOrphaned: {}, ErrorBrowserDisconnected: {},
}

// IsKnownErrorCode reports whether code belongs to the frozen C0 vocabulary.
func IsKnownErrorCode(code ErrorCode) bool {
	_, ok := knownErrorCodes[code]
	return ok
}

// ClassifiedError carries a safe, already-classified broker failure across a
// neutral seam. Message and Details are model-visible and therefore callers
// constructing this type must not put secrets, raw input, or page output in
// them.
type ClassifiedError struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Details   map[string]any
	Cause     error
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return "webmcp classified error"
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewClassifiedError creates a safe broker error. An empty message is filled
// with the stable default when it is converted to a result envelope.
func NewClassifiedError(code ErrorCode, message string, details map[string]any) *ClassifiedError {
	return &ClassifiedError{Code: code, Message: message, Retryable: defaultRetryable(code), Details: details}
}

// ResultErrorFor converts an internal error into a stable model-facing error.
// Unknown errors receive a safe generic message and never expose Error().
func ResultErrorFor(err error, fallback ErrorCode, details map[string]any) ToolResultError {
	if details == nil {
		details = map[string]any{}
	}
	var classified *ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		if classified.Details != nil {
			details = classified.Details
		}
		code := classified.Code
		if !IsKnownErrorCode(code) {
			code = fallback
		}
		message := classified.Message
		if message == "" {
			message = defaultErrorMessage(code)
		}
		return ToolResultError{Code: string(code), Message: message, Retryable: classified.Retryable, Details: details}
	}
	if errors.Is(err, context.Canceled) {
		fallback = ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		fallback = ErrorInvocationTimedOut
	}
	if !IsKnownErrorCode(fallback) {
		fallback = ErrorInvocationFailed
	}
	return ToolResultError{
		Code:      string(fallback),
		Message:   defaultErrorMessage(fallback),
		Retryable: defaultRetryable(fallback),
		Details:   details,
	}
}

func defaultRetryable(code ErrorCode) bool {
	switch code {
	case ErrorWebMCPDisabled, ErrorEndpointUnreachable, ErrorNoEligibleTab,
		ErrorAmbiguousBrowser, ErrorAmbiguousTab, ErrorStaleSelection,
		ErrorStaleToolRef, ErrorApprovalRequired, ErrorInvalidToolInput,
		ErrorTargetAttachFailed:
		return true
	default:
		return false
	}
}

func defaultErrorMessage(code ErrorCode) string {
	switch code {
	case ErrorWebMCPDisabled:
		return "Browser tools are not enabled."
	case ErrorInvalidToolInput:
		return "The broker tool input is invalid."
	case ErrorStaleToolRef:
		return "The page tool reference is no longer current."
	case ErrorStaleSelection:
		return "The selected browser target is no longer current."
	case ErrorInvocationCanceled:
		return "The browser invocation was canceled."
	case ErrorInvocationTimedOut:
		return "The browser invocation timed out."
	case ErrorInvocationFailed:
		return "The browser invocation failed."
	case ErrorBrowserDisconnected:
		return "The browser connection ended before the operation completed."
	default:
		return "The WebMCP operation could not be completed."
	}
}
