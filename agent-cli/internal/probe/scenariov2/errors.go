package scenariov2

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	invocationErrorCode    = "invocation_error"
	invocationNotFoundCode = "invocation_not_found"
)

// BrowserExecutorError is a safe, actionable classification for
// real-adapter construction and operation failures. It deliberately excludes
// the underlying error text because browser errors can contain endpoint or
// transport details that do not belong in probe output.
type BrowserExecutorError struct {
	Mode  BrowserExecutorMode
	Code  webmcp.ErrorCode
	Phase string
	Cause error
}

func (e *BrowserExecutorError) Error() string {
	if e == nil {
		return ErrRealAdapterUnavailable.Error()
	}
	return fmt.Sprintf("probe.scenario.v2 browser executor %q failed: code=%s phase=%s; %s", e.Mode, e.Code, e.Phase, browserAction(e.Code))
}

func (e *BrowserExecutorError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause == nil {
		return ErrRealAdapterUnavailable
	}
	return errors.Join(ErrRealAdapterUnavailable, e.Cause)
}

func newBrowserExecutorError(mode BrowserExecutorMode, phase string, fallback webmcp.ErrorCode, cause error) error {
	code := fallback
	var classified *webmcp.ClassifiedError
	if errors.As(cause, &classified) && classified != nil && webmcp.IsKnownErrorCode(classified.Code) {
		code = classified.Code
	}
	if !webmcp.IsKnownErrorCode(code) {
		code = webmcp.ErrorBrowserProtocol
	}
	if phase == "" {
		phase = "browser"
	}
	return &BrowserExecutorError{Mode: mode, Code: code, Phase: phase, Cause: cause}
}

func browserOperationError(mode BrowserExecutorMode, phase string, cause error) error {
	if cause == nil {
		return nil
	}
	var executorErr *BrowserExecutorError
	if errors.As(cause, &executorErr) {
		return cause
	}
	fallback := webmcp.ErrorBrowserProtocol
	if errors.Is(cause, webmcp.ErrClosed) || errors.Is(cause, webmcp.ErrBrowserNotFound) {
		fallback = webmcp.ErrorBrowserDisconnected
	}
	if errors.Is(cause, context.Canceled) {
		fallback = webmcp.ErrorInvocationCanceled
	}
	return newBrowserExecutorError(mode, phase, fallback, cause)
}

// browserActions maps a classified prerequisite failure to operator advice.
func browserActions() map[webmcp.ErrorCode]string {
	return map[webmcp.ErrorCode]string{
		webmcp.ErrorEndpointNotFound:     "configure an explicit browser endpoint or start the browser",
		webmcp.ErrorEndpointUnreachable:  "verify the configured browser endpoint is running and reachable",
		webmcp.ErrorRemoteEndpointDenied: "use a permitted loopback endpoint or explicitly allow the remote endpoint",
		webmcp.ErrorUnsupportedWebMCP:    "use a browser adapter that provides the stateful WebMCP probe seam",
		webmcp.ErrorBrowserDisconnected:  "reconnect the browser and rerun the probe",
		webmcp.ErrorBrowserProtocol:      "check the browser protocol and probe adapter composition",
		webmcp.ErrorInvocationCanceled:   "rerun the probe without cancellation",
	}
}

func browserAction(code webmcp.ErrorCode) string {
	if action, ok := browserActions()[code]; ok {
		return action
	}
	return "inspect the browser prerequisite and rerun the probe"
}

// ErrorCode projects an execution failure onto its stable result code.
func ErrorCode(err error) string {
	if err == nil {
		return invocationErrorCode
	}
	var executorErr *BrowserExecutorError
	if errors.As(err, &executorErr) && executorErr != nil {
		return string(executorErr.Code)
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code != "" {
		return string(classified.Code)
	}
	if errors.Is(err, webmcp.ErrStaleToolRef) {
		return string(webmcp.ErrorStaleToolRef)
	}
	if errors.Is(err, webmcp.ErrInvocationNotFound) {
		return invocationNotFoundCode
	}
	return invocationErrorCode
}

func isStaleToolError(err error) bool {
	if errors.Is(err, webmcp.ErrStaleToolRef) {
		return true
	}
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorStaleToolRef
}
