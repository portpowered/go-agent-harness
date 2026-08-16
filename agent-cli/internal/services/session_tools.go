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

// sessionToolExecutor is the session boundary around the executor already
// supplied to the non-session path. It deliberately does not inspect or
// duplicate tool definitions: the wrapped executor remains the owner of tool
// lookup and argument validation.
type sessionToolExecutor struct {
	inner   messages.ToolExecutor
	timeout time.Duration
}

var _ messages.ToolExecutor = (*sessionToolExecutor)(nil)

// newSessionToolExecutor adapts an injected executor for use by a duplex
// session. A non-positive timeout selects the session default.
//
// The session loop construction seam should pass the returned executor to
// agentloop.WithToolExecutor. Keeping the adapter at this boundary makes an
// individual tool failure a tool result instead of a fatal loop error.
func newSessionToolExecutor(inner messages.ToolExecutor, timeout time.Duration) *sessionToolExecutor {
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
// deadline expiry are returned as correlated tool-result content with a nil
// Go error so ToolRunner does not terminate the session.
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
