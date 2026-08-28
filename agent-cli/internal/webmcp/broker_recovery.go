package webmcp

import (
	"context"
	"errors"
)

func staleSelectionForSession(selected *brokerSession, reason string) error {
	if selected == nil {
		return staleSelectionError("", "", 0, reason)
	}
	if failure := sessionLifecycleFailure(selected); failure != nil {
		return failure
	}
	return staleSelectionError(selected.context.Key.BrowserID, selected.context.Key.TargetID, selected.context.Generation, reason)
}

func targetAttachError(selector TargetSelector, phase string, cause error) error {
	if _, lifecycle := lifecycleClassifiedError(cause); lifecycle {
		return cause
	}
	return classified(ErrorTargetAttachFailed, "the selected browser target could not be initialized", map[string]any{
		"browser_id":  string(selector.BrowserID),
		"target_id":   string(selector.TargetID),
		"phase":       phase,
		"reason_code": "attach_failed",
	}, cause)
}

func invocationFailureResultForError(invocation *brokerInvocation, err error, fallback ErrorCode) InvokeResult {
	code := errorCodeFor(err, fallback)
	details := classifiedDetails(err)
	if len(details) == 0 {
		details = map[string]any{
			"invocation_id": string(invocation.invocation.ID),
			"phase":         "dispatch",
		}
		if code == ErrorInvocationFailed || code == ErrorBrowserDisconnected {
			details["side_effect_unknown"] = true
		}
	}
	return invocationFailureResult(invocation, invocationStateForError(code), code, details)
}

func invocationStateForError(code ErrorCode) InvocationState {
	switch code {
	case ErrorInvocationCanceled:
		return InvocationCanceled
	case ErrorInvocationTimedOut:
		return InvocationTimedOut
	case ErrorInvocationOrphaned:
		return InvocationOrphaned
	default:
		return InvocationError
	}
}

func errorCodeFor(err error, fallback ErrorCode) ErrorCode {
	var classifiedError *ClassifiedError
	if errors.As(err, &classifiedError) && classifiedError != nil && IsKnownErrorCode(classifiedError.Code) {
		return classifiedError.Code
	}
	if errors.Is(err, context.Canceled) {
		return ErrorInvocationCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorInvocationTimedOut
	}
	return fallback
}
