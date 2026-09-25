// Package toolexec implements the session-owned tool executor: per-call
// deadlines, panic isolation, operator diagnostics, and correlated failure
// results that never escalate one tool failure into a fatal session error.
package toolexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	errNotConfigured sessionturn.Error = "session tool executor is not configured"
	errPanicked      sessionturn.Error = "tool executor panicked"
	errCanceled      sessionturn.Error = "tool execution canceled"
)

// Executor wraps a composed executor without owning lookup or validation.
type Executor struct {
	inner        messages.ToolExecutor
	timeout      time.Duration
	policy       tools.InteractiveToolPolicy
	cancellation sessionturn.CancellationIntent
	diagnostics  sessiontrace.ToolDiagnosticSink
	presentation presentation
}

var _ messages.ToolExecutor = (*Executor)(nil)

// New snapshots the request. Workers must honor context cancellation because
// Go cannot stop a goroutine.
func New(request sessionturn.ToolExecutorRequest) *Executor {
	executor := &Executor{
		inner:        request.Inner,
		timeout:      request.Timeout,
		cancellation: request.Cancellation,
		diagnostics:  request.Diagnostics,
		presentation: presentation{ToolPresentation: request.Presentation},
	}
	if request.Policy != nil {
		executor.policy = request.Policy.Clone()
	}
	return executor
}

type executionResult struct {
	response messages.ToolCallResponse
	err      error
}

// Execute implements messages.ToolExecutor. Errors, panics, and tool-local
// deadline expiry are returned as correlated tool-result content with a nil
// Go error so the loop's tool runner keeps the session alive.
//
//nolint:contextcheck // a nil caller context selects the background context.
func (e *Executor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil {
		return genericFailure(call, errNotConfigured), nil
	}
	if e.inner == nil {
		return e.finish(call, e.failure(call, errNotConfigured))
	}
	timeout := e.callTimeout(call.Name)
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resultCh := make(chan executionResult, 1)
	go func() {
		response, err := invoke(execCtx, e.inner, call)
		resultCh <- executionResult{response: response, err: err}
	}()
	select {
	case result := <-resultCh:
		return e.completed(execCtx, call, result)
	case <-execCtx.Done():
		return e.expired(ctx, execCtx, call, timeout)
	}
}

func (e *Executor) callTimeout(name string) time.Duration {
	timeout := e.timeout
	if timeout <= 0 && e.policy != nil {
		timeout = e.policy.TimeoutForTool(name)
	}
	if timeout <= 0 {
		timeout = sessionturn.DefaultToolExecutionTimeout
	}
	return timeout
}

func (e *Executor) completed(execCtx context.Context, call messages.ToolCall, result executionResult) (messages.ToolCallResponse, error) {
	if result.err == nil {
		return e.finish(call, result.response)
	}
	if e.sigintCancelled(execCtx, result.err) {
		return cancelledResult(call, result.err)
	}
	return e.finish(call, e.failure(call, result.err))
}

func (e *Executor) expired(ctx, execCtx context.Context, call messages.ToolCall, timeout time.Duration) (messages.ToolCallResponse, error) {
	if e.sigintCancelled(execCtx, execCtx.Err()) {
		return cancelledResult(call, execCtx.Err())
	}
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		if permission, denied := e.deniedScreenPermission(ctx, call); denied {
			return e.finish(call, e.failure(call, e.presentation.DisplayPermissionDenied(permission)))
		}
	}
	failure := contextFailure(execCtx.Err())
	if errors.Is(failure, sessionturn.ErrToolTimeout) {
		failure = fmt.Errorf("%w after %s", sessionturn.ErrToolTimeout, timeout)
	}
	return e.finish(call, e.failure(call, failure))
}

// finish keeps the provider's call identity authoritative even when an
// injected executor omits or changes the response metadata.
func (e *Executor) finish(call messages.ToolCall, response messages.ToolCallResponse) (messages.ToolCallResponse, error) {
	response.ToolCallID = call.ID
	response.Name = call.Name
	return response, nil
}

// sigintCancelled identifies the one cancellation that must not become a
// provider-visible failed result: the runner receives context.Canceled so
// the terminal summary classifies the obligation as a user cancellation.
func (e *Executor) sigintCancelled(ctx context.Context, err error) bool {
	return e.cancellation != nil && e.cancellation.SIGINTReceived() &&
		errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func cancelledResult(call messages.ToolCall, err error) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, err
}

// invoke runs exactly one inner invocation and confines panic recovery to it.
func invoke(ctx context.Context, executor messages.ToolExecutor, call messages.ToolCall) (response messages.ToolCallResponse, err error) {
	defer func() {
		if recover() != nil {
			response = messages.ToolCallResponse{}
			err = errPanicked
		}
	}()
	return executor.Execute(ctx, call)
}

func contextFailure(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return sessionturn.ErrToolTimeout
	case errors.Is(err, context.Canceled):
		return errCanceled
	default:
		return fmt.Errorf("tool execution stopped: %w", err)
	}
}
