package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// defaultSessionToolExecutionTimeout bounds one tool invocation without
// changing the lifetime of the enclosing session.
const defaultSessionToolExecutionTimeout = 60 * time.Second

// The macOS permission API is non-prompting and normally returns immediately,
// but the session boundary must not let a misbehaving injected checker extend a
// tool timeout without bound.
const sessionScreenPermissionRecheckTimeout = 100 * time.Millisecond

const (
	// SessionToolTimeoutClassification is the stable provider-visible marker
	// for an interactive tool deadline. It is deliberately distinct from a
	// transport or enclosing-session timeout: the model can recover from this
	// one failed call and continue the voice turn.
	SessionToolTimeoutClassification = "interactive_tool_timeout"
)

// sessionToolLifecycleMux preserves the optional recording hook while adding
// the participant-owned liveness boundary. A running local tool must suppress
// the provider watchdog; the next accepted response.create re-arms it.
type sessionToolLifecycleMux struct {
	recording sessionToolLifecycleObserver
	progress  *sessionProgressObserver
}

func (m sessionToolLifecycleMux) observeToolCall(call messages.ToolCall) {
	if m.progress != nil {
		m.progress.beginLocalToolExecution()
	}
	if m.recording != nil {
		m.recording.observeToolCall(call)
	}
}

func (m sessionToolLifecycleMux) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if m.recording != nil {
		m.recording.observeToolResult(call, response, failed)
	}
	if m.progress != nil {
		m.progress.endLocalToolExecution()
	}
}

func composeSessionToolLifecycleObserver(recording sessionToolLifecycleObserver, progress *sessionProgressObserver) sessionToolLifecycleObserver {
	if recording == nil && progress == nil {
		return nil
	}
	return sessionToolLifecycleMux{recording: recording, progress: progress}
}

var (
	// ErrSessionToolTimeout is retained behind the correlated tool-result
	// contract so callers and tests can classify a local deadline without
	// parsing the human-readable response content.
	ErrSessionToolTimeout = errors.New("tool execution timed out")
)

// sessionToolExecutor is the session boundary around the executor composed by
// the wire graph. It deliberately does not inspect or duplicate tool
// definitions: the wrapped executor remains the owner of tool lookup and
// argument validation.
type sessionToolExecutor struct {
	inner              messages.ToolExecutor
	timeout            time.Duration
	interactivePolicy  *InteractiveToolPolicy
	observer           sessionToolLifecycleObserver
	cancellationIntent *SessionCancellationIntent
}

var _ messages.ToolExecutor = (*sessionToolExecutor)(nil)

// newSessionToolExecutor retains the legacy single-deadline adapter used by
// non-policy callers. The duplex session path uses the interactive policy
// constructor below.
//
// The duplex loop construction seam passes the returned executor to
// agentloop.WithToolExecutor. Keeping the adapter at this boundary makes an
// individual tool failure a correlated tool result instead of a fatal loop
// error, so one bad call never terminates an ongoing voice session.
//
// Execution contract: inner executors must honor context cancellation
// cooperatively; Go cannot terminate a goroutine that ignores its context. The
// adapter guarantees only that the session itself continues after the deadline
// and that a cooperative worker exits promptly once cancellation fires.
func newSessionToolExecutor(inner messages.ToolExecutor) *sessionToolExecutor {
	return newSessionToolExecutorWithTimeoutAndObserver(inner, 0, nil)
}

// newSessionToolExecutorWithTimeout is the deterministic seam for tests; a
// non-positive timeout selects the session default.
func newSessionToolExecutorWithTimeout(inner messages.ToolExecutor, timeout time.Duration) *sessionToolExecutor {
	return newSessionToolExecutorWithTimeoutAndObserver(inner, timeout, nil)
}

func newSessionToolExecutorWithTimeoutAndObserver(inner messages.ToolExecutor, timeout time.Duration, observer sessionToolLifecycleObserver) *sessionToolExecutor {
	return newSessionToolExecutorWithTimeoutAndObserverAndCancellationIntent(inner, timeout, observer, nil)
}

func newSessionToolExecutorWithTimeoutAndObserverAndCancellationIntent(
	inner messages.ToolExecutor,
	timeout time.Duration,
	observer sessionToolLifecycleObserver,
	cancellationIntent *SessionCancellationIntent,
) *sessionToolExecutor {
	if timeout <= 0 {
		timeout = defaultSessionToolExecutionTimeout
	}
	return &sessionToolExecutor{
		inner:              inner,
		timeout:            timeout,
		observer:           observer,
		cancellationIntent: cancellationIntent,
	}
}

func newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(
	inner messages.ToolExecutor,
	policy *InteractiveToolPolicy,
	timeoutOverride time.Duration,
	observer sessionToolLifecycleObserver,
	cancellationIntent *SessionCancellationIntent,
) *sessionToolExecutor {
	if policy == nil {
		defaultPolicy, err := NewInteractiveToolPolicy(config.DefaultInteractiveToolConfig(), nil)
		if err == nil {
			policy = &defaultPolicy
		}
	}
	var policySnapshot *InteractiveToolPolicy
	if policy != nil {
		clone := policy.Clone()
		policySnapshot = &clone
	}
	return &sessionToolExecutor{
		inner:              inner,
		timeout:            timeoutOverride,
		interactivePolicy:  policySnapshot,
		observer:           observer,
		cancellationIntent: cancellationIntent,
	}
}

type sessionToolExecutionResult struct {
	response messages.ToolCallResponse
	err      error
}

type sessionScreenPermissionRecheckResult struct {
	permission cliTools.DisplayPermission
	err        error
}

// Execute implements messages.ToolExecutor. Errors, panics, and tool-local
// deadline expiry are returned as correlated tool-result content with a nil Go
// error so the loop's ToolRunner never escalates a single tool failure into a
// fatal session error.
func (e *sessionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil {
		return sessionToolFailure(call, errors.New("session tool executor is not configured")), nil
	}
	if e.observer != nil {
		e.observer.observeToolCall(call)
	}
	finish := func(response messages.ToolCallResponse, failed bool) (messages.ToolCallResponse, error) {
		// The provider's call identity is authoritative even if an injected
		// executor omits or changes the response metadata.
		response.ToolCallID = call.ID
		response.Name = call.Name
		if e.observer != nil {
			e.observer.observeToolResult(call, response, failed)
		}
		return response, nil
	}
	if e.inner == nil {
		return finish(sessionToolFailure(call, errors.New("session tool executor is not configured")), true)
	}

	timeout := e.timeout
	if timeout <= 0 && e.interactivePolicy != nil {
		timeout = e.interactivePolicy.TimeoutForTool(call.Name)
	}
	if timeout <= 0 {
		timeout = defaultSessionToolExecutionTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan sessionToolExecutionResult, 1)
	go func() {
		response, err := invokeSessionTool(execCtx, e.inner, call)
		resultCh <- sessionToolExecutionResult{response: response, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			if e.sigintCancelled(execCtx, result.err) {
				return correlatedSessionToolCancellation(call, result.err)
			}
			return finish(sessionToolFailure(call, result.err), true)
		}
		return finish(result.response, sessionToolResponseFailed(result.response.Content))
	case <-execCtx.Done():
		if e.sigintCancelled(execCtx, execCtx.Err()) {
			return correlatedSessionToolCancellation(call, execCtx.Err())
		}
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			if denial, ok := e.screenPermissionDeniedAfterTimeout(ctx, call); ok {
				return finish(sessionToolFailure(call, denial), true)
			}
		}
		failure := sessionToolContextFailure(execCtx.Err())
		if errors.Is(failure, ErrSessionToolTimeout) {
			failure = fmt.Errorf("%w after %s", ErrSessionToolTimeout, timeout)
		}
		return finish(sessionToolFailure(call, failure), true)
	}
}

// screenPermissionDeniedAfterTimeout performs the one optional macOS
// permission re-check allowed for a timed-out physical-screen call. It uses
// the enclosing session context rather than the already-expired tool context,
// then bounds the checker independently so a slow or non-cooperative checker
// cannot hold up the session.
func (e *sessionToolExecutor) screenPermissionDeniedAfterTimeout(ctx context.Context, call messages.ToolCall) (*cliTools.ScreenCaptureError, bool) {
	if e == nil || ctx == nil || ctx.Err() != nil || call.Name != cliTools.ScreenToolID {
		return nil, false
	}
	rechecker, ok := e.inner.(cliTools.ScreenRecordingPermissionRechecker)
	if !ok || !safeScreenPermissionRecheckSupported(rechecker) {
		return nil, false
	}

	recheckCtx, cancel := context.WithTimeout(ctx, sessionScreenPermissionRecheckTimeout)
	defer cancel()
	resultCh := make(chan sessionScreenPermissionRecheckResult, 1)
	go func() {
		permission, err := invokeScreenPermissionRecheck(recheckCtx, rechecker)
		resultCh <- sessionScreenPermissionRecheckResult{permission: permission, err: err}
	}()

	select {
	case result := <-resultCh:
		if ctx.Err() != nil || result.err != nil || result.permission.State != cliTools.DisplayPermissionDenied {
			return nil, false
		}
		return &cliTools.ScreenCaptureError{
			State:     cliTools.ScreenCaptureDenied,
			Operation: "show permission re-check",
			Reason:    result.permission.Reason,
		}, true
	case <-recheckCtx.Done():
		return nil, false
	}
}

func safeScreenPermissionRecheckSupported(rechecker cliTools.ScreenRecordingPermissionRechecker) (supported bool) {
	defer func() {
		if recover() != nil {
			supported = false
		}
	}()
	return rechecker.ScreenRecordingPermissionRecheckSupported()
}

func invokeScreenPermissionRecheck(ctx context.Context, rechecker cliTools.ScreenRecordingPermissionRechecker) (permission cliTools.DisplayPermission, err error) {
	defer func() {
		if recover() != nil {
			permission = cliTools.DisplayPermission{}
			err = errors.New("screen recording permission re-check panicked")
		}
	}()
	return rechecker.RecheckScreenRecordingPermission(ctx)
}

// sigintCancelled identifies the one cancellation path that must not be
// turned into a provider-visible failed tool result. The tool runner still
// receives context.Canceled so the enclosing loop stops, but the lifecycle
// observer records only the provider call, allowing the terminal summary to
// classify the outstanding obligation as a user cancellation. An ordinary
// caller cancellation and every independent tool error retain their existing
// failure/result behavior.
func (e *sessionToolExecutor) sigintCancelled(ctx context.Context, err error) bool {
	return e != nil && e.cancellationIntent != nil && e.cancellationIntent.SIGINTReceived() &&
		errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func correlatedSessionToolCancellation(call messages.ToolCall, err error) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
	}, err
}

// sessionToolResponseFailed recognizes the structured WebMCP failure envelope
// while retaining the generic executor contract: a non-WebMCP result is
// considered complete unless the executor returned a Go error.
func sessionToolResponseFailed(content string) bool {
	var envelope struct {
		Version string `json:"version"`
		OK      *bool  `json:"ok"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return false
	}
	return envelope.Version == "webmcp.tool-result.v1" && envelope.OK != nil && !*envelope.OK
}

// invokeSessionTool runs exactly one inner invocation and confines panic
// recovery to that invocation so an unrelated panicking tool cannot poison the
// session.
func invokeSessionTool(ctx context.Context, executor messages.ToolExecutor, call messages.ToolCall) (response messages.ToolCallResponse, err error) {
	defer func() {
		if recover() != nil {
			response = messages.ToolCallResponse{}
			err = errors.New("tool executor panicked")
		}
	}()
	return executor.Execute(ctx, call)
}

func sessionToolContextFailure(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrSessionToolTimeout
	case errors.Is(err, context.Canceled):
		return errors.New("tool execution canceled")
	default:
		return fmt.Errorf("tool execution stopped: %w", err)
	}
}

func sessionToolFailure(call messages.ToolCall, err error) messages.ToolCallResponse {
	if err == nil {
		err = errors.New("tool execution failed")
	}
	if call.Name == cliTools.ScreenToolID {
		return messages.ToolCallResponse{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    cliTools.ScreenToolErrorResult(err),
		}
	}
	message := fmt.Sprintf("tool %q failed", call.Name)
	if errors.Is(err, ErrSessionToolTimeout) || errors.Is(err, context.DeadlineExceeded) {
		message += fmt.Sprintf(" (classification=%s)", SessionToolTimeoutClassification)
	}
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    fmt.Sprintf("%s: %s", message, err),
	}
}
