package chrome

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func classifiedOpenError(candidate webmcp.BrowserCandidate, cause error) error {
	if classified, ok := cause.(*webmcp.ClassifiedError); ok {
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
	if classified, ok := cause.(*webmcp.ClassifiedError); ok {
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
	if classified, ok := cause.(*webmcp.ClassifiedError); ok {
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
	if classified, ok := cause.(*webmcp.ClassifiedError); ok {
		return classified
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

func classifyTargetCleanupError(session *targetSession, phase string, cause error) error {
	if classified, ok := cause.(*webmcp.ClassifiedError); ok {
		return classified
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
