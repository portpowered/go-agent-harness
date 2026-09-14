package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const defaultToolExecutionTimeout = 60 * time.Second

const errToolTimeout sessionturn.ErrorCode = "tool execution timed out"

type toolExecutor struct {
	inner         messages.ToolExecutor
	policy        tools.InteractiveToolPolicy
	timeout       time.Duration
	observeCall   func(messages.ToolCall)
	observeResult func(messages.ToolCall, messages.ToolCallResponse, bool)
	diagnose      func(messages.ToolCall, error)
	present       func(messages.ToolCall, error) messages.ToolCallResponse
}

func (*toolExecutor) SessionTurnToolExecutor() {}

func newToolExecutor(inner messages.ToolExecutor, policy tools.InteractiveToolPolicy, timeout time.Duration, observeCall func(messages.ToolCall), observeResult func(messages.ToolCall, messages.ToolCallResponse, bool), diagnose func(messages.ToolCall, error), present func(messages.ToolCall, error) messages.ToolCallResponse) messages.ToolExecutor {
	if inner == nil {
		return nil
	}
	return &toolExecutor{inner: inner, policy: clonePolicy(policy), timeout: timeout, observeCall: observeCall, observeResult: observeResult, diagnose: diagnose, present: present}
}

func (e *toolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e == nil || e.inner == nil {
		err := errors.New("session turn tool executor is not configured")
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: fmt.Sprintf("tool %q failed: %v", call.Name, err)}, nil
	}
	if e.observeCall != nil {
		e.observeCall(call)
	}
	timeout := e.timeout
	if timeout <= 0 && e.policy != nil {
		timeout = e.policy.TimeoutForTool(call.Name)
	}
	if timeout <= 0 {
		timeout = defaultToolExecutionTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resultCh := make(chan toolResult, 1)
	go func() {
		response, err := invokeTool(execCtx, e.inner, call)
		resultCh <- toolResult{response: response, err: err}
	}()
	select {
	case result := <-resultCh:
		if result.err == nil {
			result.response.ToolCallID, result.response.Name = call.ID, call.Name
			return e.finish(call, result.response, toolResponseFailed(result.response.Content), nil)
		}
		return e.failed(call, result.err)
	case <-execCtx.Done():
		if ctx.Err() != nil {
			return e.finish(call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, false, ctx.Err())
		}
		return e.failed(call, fmt.Errorf("%w after %s", errToolTimeout, timeout))
	}
}

type toolResult struct {
	response messages.ToolCallResponse
	err      error
}

func (e *toolExecutor) failed(call messages.ToolCall, err error) (messages.ToolCallResponse, error) {
	if e.diagnose != nil {
		e.diagnose(call, err)
	}
	return e.finish(call, e.failure(call, err), true, nil)
}
func (e *toolExecutor) finish(call messages.ToolCall, response messages.ToolCallResponse, failed bool, runErr error) (messages.ToolCallResponse, error) {
	response.ToolCallID, response.Name = call.ID, call.Name
	if e.observeResult != nil {
		e.observeResult(call, response, failed)
	}
	return response, runErr
}
func (e *toolExecutor) failure(call messages.ToolCall, err error) messages.ToolCallResponse {
	if e.present != nil {
		response := e.present(call, err)
		response.ToolCallID, response.Name = call.ID, call.Name
		return response
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: fmt.Sprintf("tool %q failed: %v", call.Name, err)}
}
func invokeTool(ctx context.Context, executor messages.ToolExecutor, call messages.ToolCall) (response messages.ToolCallResponse, err error) {
	defer func() {
		if recover() != nil {
			response = messages.ToolCallResponse{}
			err = errors.New("tool executor panicked")
		}
	}()
	return executor.Execute(ctx, call)
}

func toolResponseFailed(content string) bool {
	var envelope struct {
		Version string `json:"version"`
		OK      *bool  `json:"ok"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return false
	}
	return envelope.Version == "webmcp.tool-result.v1" && envelope.OK != nil && !*envelope.OK
}

var _ messages.ToolExecutor = (*toolExecutor)(nil)
