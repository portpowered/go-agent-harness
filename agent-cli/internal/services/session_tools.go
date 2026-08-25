package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// defaultSessionToolExecutionTimeout bounds one tool invocation without
// changing the lifetime of the enclosing session.
const defaultSessionToolExecutionTimeout = 60 * time.Second

// sessionToolExecutor is the session boundary around the executor composed by
// the wire graph. It deliberately does not inspect or duplicate tool
// definitions: the wrapped executor remains the owner of tool lookup and
// argument validation.
type sessionToolExecutor struct {
	inner   messages.ToolExecutor
	timeout time.Duration
}

var _ messages.ToolExecutor = (*sessionToolExecutor)(nil)

// newSessionToolExecutor adapts the composed session executor for use by a
// duplex agent loop. A non-positive timeout selects the session default.
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
	return newSessionToolExecutorWithTimeout(inner, 0)
}

// newSessionToolExecutorWithTimeout is the deterministic seam for tests; a
// non-positive timeout selects the session default.
func newSessionToolExecutorWithTimeout(inner messages.ToolExecutor, timeout time.Duration) *sessionToolExecutor {
	if timeout <= 0 {
		timeout = defaultSessionToolExecutionTimeout
	}
	return &sessionToolExecutor{inner: inner, timeout: timeout}
}

type sessionToolExecutionResult struct {
	response messages.ToolCallResponse
	err      error
}

// Execute implements messages.ToolExecutor. Errors, panics, and tool-local
// deadline expiry are returned as correlated tool-result content with a nil Go
// error so the loop's ToolRunner never escalates a single tool failure into a
// fatal session error.
func (e *sessionToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || e.inner == nil {
		return sessionToolFailure(call, errors.New("session tool executor is not configured")), nil
	}

	execCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	resultCh := make(chan sessionToolExecutionResult, 1)
	go func() {
		response, err := invokeSessionTool(execCtx, e.inner, call)
		resultCh <- sessionToolExecutionResult{response: response, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return sessionToolFailure(call, result.err), nil
		}
		// The provider's call identity is authoritative even if an injected
		// executor omits or changes the response metadata.
		result.response.ToolCallID = call.ID
		result.response.Name = call.Name
		return result.response, nil
	case <-execCtx.Done():
		return sessionToolFailure(call, sessionToolContextFailure(execCtx.Err())), nil
	}
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
		return errors.New("tool execution timed out")
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
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    fmt.Sprintf("tool %q failed: %s", call.Name, err),
	}
}
