package browser

import (
	"context"
	"errors"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// NewClassifiedError creates a safe broker error. An empty message is filled
// with the stable default when it is converted to a result envelope.
func NewClassifiedError(code ErrorCode, message string, details map[string]any) *public.ClassifiedError {
	return &public.ClassifiedError{
		Code:      code,
		Message:   message,
		Retryable: defaultRetryable(code),
		Details:   withAmbiguityRecovery(code, details),
	}
}

// ResultErrorFor converts an internal error into a stable model-facing error.
// Unknown errors receive a safe generic message and never expose Error().
func ResultErrorFor(err error, fallback ErrorCode, details map[string]any) public.ToolResultError {
	if details == nil {
		details = map[string]any{}
	}
	var classified *public.ClassifiedError
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
		return public.ToolResultError{Code: string(code), Message: message, Retryable: classified.Retryable, Details: withAmbiguityRecovery(code, details)}
	}
	if errors.Is(err, context.Canceled) {
		fallback = public.ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		fallback = public.ErrorInvocationTimedOut
	}
	if !IsKnownErrorCode(fallback) {
		fallback = public.ErrorInvocationFailed
	}
	return public.ToolResultError{
		Code:      string(fallback),
		Message:   DefaultErrorMessage(fallback),
		Retryable: defaultRetryable(fallback),
		Details:   withAmbiguityRecovery(fallback, details),
	}
}

func defaultRetryable(code ErrorCode) bool {
	switch code {
	case public.ErrorWebMCPDisabled, public.ErrorEndpointUnreachable, public.ErrorNoEligibleTab,
		public.ErrorAmbiguousBrowser, public.ErrorAmbiguousTab, public.ErrorStaleSelection,
		public.ErrorStaleToolRef, public.ErrorApprovalRequired, public.ErrorInvalidToolInput,
		public.ErrorTargetAttachFailed:
		return true
	default:
		return false
	}
}

// DefaultErrorMessage returns the safe model-facing message for code.
func DefaultErrorMessage(code ErrorCode) string {
	switch code {
	case public.ErrorWebMCPDisabled:
		return "Browser tools are not enabled."
	case public.ErrorInvalidToolInput:
		return "The broker tool input is invalid."
	case public.ErrorStaleToolRef:
		return "The page tool reference is no longer current."
	case public.ErrorStaleSelection:
		return "The selected browser target is no longer current."
	case public.ErrorInvocationCanceled:
		return "The browser invocation was canceled."
	case public.ErrorInvocationTimedOut:
		return "The browser invocation timed out."
	case public.ErrorInvocationFailed:
		return "The browser invocation failed."
	case public.ErrorBrowserDisconnected:
		return "The browser connection ended before the operation completed."
	default:
		return "The WebMCP operation could not be completed."
	}
}

// ContextErrorCode returns the C0 operation class for a context failure.
// Adapter packages use this helper without exposing their transport errors.
func ContextErrorCode(err error) ErrorCode {
	if errors.Is(err, context.Canceled) {
		return public.ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return public.ErrorInvocationTimedOut
	}
	return public.ErrorInvocationFailed
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
