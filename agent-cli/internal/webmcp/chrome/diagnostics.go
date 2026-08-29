package chrome

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func classifiedOpenError(candidate webmcp.BrowserCandidate, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	return &webmcp.ClassifiedError{
		Code:      webmcp.ErrorEndpointUnreachable,
		Message:   "The browser endpoint could not be reached.",
		Retryable: true,
		Details: map[string]any{
			"endpoint_kind": "cdp",
			"address_class": addressClass(candidate),
			"phase":         "connect",
		},
		Cause: cause,
	}
}

func classifiedHandleError(candidate webmcp.BrowserCandidate, code webmcp.ErrorCode, phase string, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	if code == "" {
		code = webmcp.ErrorBrowserProtocol
	}
	return &webmcp.ClassifiedError{
		Code:      code,
		Message:   webmcp.DefaultErrorMessage(code),
		Retryable: code == webmcp.ErrorEndpointUnreachable || code == webmcp.ErrorTargetAttachFailed,
		Details: map[string]any{
			"phase":       phase,
			"reason_code": safeReason(cause),
		},
		Cause: cause,
	}
}

func classifiedTargetError(candidate webmcp.BrowserCandidate, targetID webmcp.TargetID, phase string, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	return &webmcp.ClassifiedError{
		Code:      webmcp.ErrorTargetAttachFailed,
		Message:   "The selected browser target could not be initialized.",
		Retryable: true,
		Details: map[string]any{
			"browser_id":  string(candidate.ID),
			"target_id":   string(targetID),
			"phase":       phase,
			"reason_code": safeReason(cause),
		},
		Cause: cause,
	}
}

func classifySessionError(session *targetSession, fallback webmcp.ErrorCode, phase string, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	if classified := sessionLifecycleError(session); classified != nil {
		return classified
	}
	if session != nil && session.handle != nil && session.handle.isDisconnected() {
		return browserDisconnectedError(session.Context(), phase, cause)
	}
	page := session.Context()
	code := fallback
	if errors.Is(cause, context.Canceled) {
		code = webmcp.ErrorInvocationCanceled
	} else if errors.Is(cause, context.DeadlineExceeded) {
		code = webmcp.ErrorInvocationTimedOut
	}
	return &webmcp.ClassifiedError{
		Code:      code,
		Message:   webmcp.DefaultErrorMessage(code),
		Retryable: code == webmcp.ErrorTargetAttachFailed || code == webmcp.ErrorEndpointUnreachable,
		Details: map[string]any{
			"browser_id":  string(page.Key.BrowserID),
			"target_id":   string(page.Key.TargetID),
			"phase":       phase,
			"reason_code": safeReason(cause),
		},
		Cause: cause,
	}
}

// classifyInvocationError retains the uncertainty that remains when a CDP
// invoke command fails. The protocol executor does not expose whether the
// command reached Chrome before returning an error, so callers must not retry
// the operation transparently.
func classifyInvocationError(session *targetSession, invocationID, phase string, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	if classified := sessionLifecycleError(session); classified != nil {
		return classified
	}
	if session != nil && session.handle != nil && session.handle.isDisconnected() {
		return browserDisconnectedError(session.Context(), phase, cause)
	}

	page := session.Context()
	code := webmcp.ErrorInvocationFailed
	details := map[string]any{
		"browser_id":          string(page.Key.BrowserID),
		"target_id":           string(page.Key.TargetID),
		"phase":               phase,
		"reason_code":         safeReason(cause),
		"side_effect_unknown": true,
	}
	if invocationID != "" {
		details["invocation_id"] = invocationID
	}
	if errors.Is(cause, context.Canceled) {
		code = webmcp.ErrorInvocationCanceled
		details["cancel_source"] = "caller"
	} else if errors.Is(cause, context.DeadlineExceeded) {
		code = webmcp.ErrorInvocationTimedOut
		details["timeout_ms"] = session.handle.timeout().Milliseconds()
	}
	return &webmcp.ClassifiedError{
		Code:      code,
		Message:   webmcp.DefaultErrorMessage(code),
		Retryable: false,
		Details:   details,
		Cause:     cause,
	}
}

// classifyCancellationError describes the explicit cancel operation without
// pretending that cancellation rolls back a page-side effect. A failed or
// interrupted cancel command is deliberately non-retryable and retains the
// possibility that the invocation is still running or has already completed.
func classifyCancellationError(session *targetSession, invocationID string, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	if classified := sessionLifecycleError(session); classified != nil {
		return classified
	}
	if session != nil && session.handle != nil && session.handle.isDisconnected() {
		failure := browserDisconnectedError(session.Context(), "cancel", cause)
		failure.(*webmcp.ClassifiedError).Details["invocation_id"] = invocationID
		return failure
	}

	page := session.Context()
	cancelSource := "explicit"
	if errors.Is(cause, context.Canceled) {
		cancelSource = "caller"
	}
	return &webmcp.ClassifiedError{
		Code:      webmcp.ErrorInvocationCanceled,
		Message:   webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled),
		Retryable: false,
		Details: map[string]any{
			"browser_id":          string(page.Key.BrowserID),
			"target_id":           string(page.Key.TargetID),
			"invocation_id":       invocationID,
			"cancel_source":       cancelSource,
			"phase":               "cancel",
			"reason_code":         safeReason(cause),
			"side_effect_unknown": true,
		},
		Cause: cause,
	}
}

func sessionLifecycleError(session *targetSession) *webmcp.ClassifiedError {
	if session == nil {
		return nil
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(session.Err(), &classified) {
		return nil
	}
	switch classified.Code {
	case webmcp.ErrorTargetDetached, webmcp.ErrorBrowserDisconnected:
		return classified
	default:
		return nil
	}
}

func classifyTargetCleanupError(session *targetSession, phase string, cause error) error {
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil {
		return classified
	}
	if session != nil && session.handle != nil && session.handle.isDisconnected() {
		return browserDisconnectedError(session.Context(), phase, cause)
	}
	page := session.Context()
	return &webmcp.ClassifiedError{
		Code:      webmcp.ErrorTargetDetached,
		Message:   webmcp.DefaultErrorMessage(webmcp.ErrorTargetDetached),
		Retryable: false,
		Details: map[string]any{
			"browser_id":  string(page.Key.BrowserID),
			"target_id":   string(page.Key.TargetID),
			"phase":       phase,
			"reason_code": safeReason(cause),
		},
		Cause: cause,
	}
}

func browserDisconnectedError(page webmcp.PageContext, phase string, cause error) error {
	return &webmcp.ClassifiedError{
		Code:      webmcp.ErrorBrowserDisconnected,
		Message:   webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected),
		Retryable: false,
		Details: map[string]any{
			"browser_id":         string(page.Key.BrowserID),
			"target_id":          string(page.Key.TargetID),
			"phase":              phase,
			"reconnect_required": true,
		},
		Cause: cause,
	}
}

func safeReason(err error) string {
	if err == nil {
		return "unknown"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case strings.Contains(strings.ToLower(err.Error()), "method not found"):
		return "method_not_found"
	case strings.Contains(strings.ToLower(err.Error()), "target"):
		return "target_error"
	default:
		return "protocol_error"
	}
}

func addressClass(candidate webmcp.BrowserCandidate) string {
	if candidate.Loopback {
		return "loopback"
	}
	endpoint := strings.TrimSpace(candidate.HTTPURL)
	if endpoint == "" {
		endpoint = strings.TrimSpace(candidate.BrowserWSURL)
	}
	parsed, err := url.Parse(endpoint)
	if err == nil {
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return "loopback"
		}
	}
	return "non_loopback"
}
