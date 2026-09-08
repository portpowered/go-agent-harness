package browser

import (
	"context"
	"errors"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type ClassifiedError = public.ClassifiedError
type ToolResultError = public.ToolResultError

// NewClassifiedError creates a safe broker error. An empty message is filled
// with the stable default when it is converted to a result envelope.
func NewClassifiedError(code ErrorCode, message string, details map[string]any) *ClassifiedError {
	return &ClassifiedError{
		Code:      code,
		Message:   message,
		Retryable: defaultRetryable(code),
		Details:   withAmbiguityRecovery(code, details),
	}
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
			details = cloneDetails(classified.Details)
		}
		code := classified.Code
		if !IsKnownErrorCode(code) {
			code = fallback
		}
		message := classified.Message
		if message == "" {
			message = DefaultErrorMessage(code)
		}
		return ToolResultError{Code: string(code), Message: message, Retryable: classified.Retryable, Details: withAmbiguityRecovery(code, details)}
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
		Message:   DefaultErrorMessage(fallback),
		Retryable: defaultRetryable(fallback),
		Details:   withAmbiguityRecovery(fallback, details),
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

// DefaultErrorMessage returns the safe model-facing message for code.
func DefaultErrorMessage(code ErrorCode) string {
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

// ContextErrorCode returns the C0 operation class for a context failure.
// Adapter packages use this helper without exposing their transport errors.
func ContextErrorCode(err error) ErrorCode {
	if errors.Is(err, context.Canceled) {
		return ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorInvocationTimedOut
	}
	return ErrorInvocationFailed
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	result := make(map[string]any, len(details))
	for key, value := range details {
		result[key] = value
	}
	return result
}
