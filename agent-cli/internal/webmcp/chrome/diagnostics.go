package chrome

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// loopbackAddressClass is the public scope label for a loopback endpoint.
const loopbackAddressClass = "loopback"

func addressClass(candidate webmcp.BrowserCandidate) string {
	if candidate.Loopback {
		return loopbackAddressClass
	}
	endpoint := strings.TrimSpace(candidate.HTTPURL)
	if endpoint == "" {
		endpoint = strings.TrimSpace(candidate.BrowserWSURL)
	}
	parsed, err := url.Parse(endpoint)
	if err == nil {
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return loopbackAddressClass
		}
	}
	return "non_loopback"
}

// closeAfterRead releases a response body or read-only file after its bytes
// were consumed. A close failure cannot invalidate data that was already read
// and validated, so it is intentionally not surfaced to the caller.
func closeAfterRead(closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		return
	}
}

// removeBestEffort deletes temporary staging state. The path is either already
// consumed by a successful rename or abandoned on an error path whose own error
// is reported, so a cleanup failure is intentionally not surfaced.
func removeBestEffort(remove func(string) error, path string) {
	if err := remove(path); err != nil {
		return
	}
}

func newChromeForTestingError(category string, cause error) error {
	return &ChromeForTestingError{Category: category, Cause: cause}
}

// ChromeForTestingError classifies an internal fallback failure without
// rendering URLs, paths, command output, or HTTP details.
type ChromeForTestingError struct {
	Category string
	Cause    error
}

func (e *ChromeForTestingError) Error() string {
	if e == nil {
		return "Chrome for Testing fallback failed"
	}
	category := safeAcquisitionCategory(e.Category)
	if category == "" {
		category = "acquisition_failed"
	}
	return "Chrome for Testing fallback failed: " + category
}

func (e *ChromeForTestingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Managed launch phases before and while waiting for DevTools readiness.
const (
	managedBrowserPhaseStartup   = "startup"
	managedBrowserPhaseReadiness = "readiness"
)

func newManagedBrowserLaunchError(phase, mode string, processExit, cause error) error {
	if phase == "" {
		phase = managedBrowserPhaseStartup
	}
	if mode == "" {
		mode = "unknown"
	}
	details := errors.Join(cause, processExit)
	return &ManagedBrowserLaunchError{Phase: phase, Mode: mode, Cause: details}
}

// ManagedBrowserLaunchError is the single safe operator-facing launch error.
// Phase and Mode are bounded labels; Cause is retained for errors.Is and
// diagnostics but is never interpolated into Error, preventing profile paths,
// URLs, command output, and nested process details from leaking.
type ManagedBrowserLaunchError struct {
	Phase string
	Mode  string
	Cause error
}

func (e *ManagedBrowserLaunchError) Error() string {
	if e == nil {
		return ErrManagedBrowserLaunch.Error()
	}
	phase := safeManagedBrowserLabel(e.Phase, managedBrowserPhaseStartup)
	mode := safeManagedBrowserLabel(e.Mode, "unknown")
	remediation := "check the Chrome prerequisite, writable agent config directory, and loopback DevTools availability, or supply an explicit browser endpoint"
	switch phase {
	case "configuration":
		remediation = "fix the managed browser startup URL and retry"
	case "profile":
		remediation = "make the agent config directory writable and retry"
	case "acquisition":
		remediation = fmt.Sprintf("install Chrome %d or newer, or supply an explicit browser endpoint", MinimumManagedChromeMajor)
	case "port":
		remediation = "retry so the agent can reserve a free loopback DevTools port"
	case "start":
		remediation = "check that the qualified Chrome executable can start with an agent-owned profile"
	case managedBrowserPhaseReadiness, managedBrowserPhaseStartup:
		remediation = "check that Chrome can publish a loopback DevTools endpoint and retry"
	}
	return fmt.Sprintf("managed WebMCP browser launch failed during %s in %s mode; %s", phase, mode, remediation)
}

func (e *ManagedBrowserLaunchError) Unwrap() error {
	if e == nil {
		return ErrManagedBrowserLaunch
	}
	return errors.Join(ErrManagedBrowserLaunch, e.Cause)
}

func safeManagedBrowserLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32 {
		return fallback
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return fallback
	}
	return value
}
