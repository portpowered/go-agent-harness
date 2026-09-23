package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	activeCount   int
	activeIdle    chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

type toolExecutionResult struct {
	response messages.ToolCallResponse
	err      error
}

func (*toolExecutor) SessionTurnToolExecutor() {}

func newToolExecutor(inner messages.ToolExecutor, policy tools.InteractiveToolPolicy, timeout time.Duration, observeCall func(messages.ToolCall), observeResult func(messages.ToolCall, messages.ToolCallResponse, bool), diagnose func(messages.ToolCall, error), present func(messages.ToolCall, error) messages.ToolCallResponse) messages.ToolExecutor {
	if inner == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	activeIdle := make(chan struct{})
	close(activeIdle)
	return &toolExecutor{inner: inner, policy: clonePolicy(policy), timeout: timeout, observeCall: observeCall, observeResult: observeResult, diagnose: diagnose, present: present, ctx: ctx, cancel: cancel, activeIdle: activeIdle}
}

func (e *toolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil || e.inner == nil {
		err := errors.New("session turn tool executor is not configured")
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: fmt.Sprintf("tool %q failed: %v", call.Name, err)}, nil
	}
	if ctx == nil {
		return e.failed(call, errors.New("session turn tool executor requires a non-nil context"))
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
	callCtx, cancelCall := context.WithCancel(ctx)
	stopClose := context.AfterFunc(e.ctx, cancelCall) //nolint:contextcheck // The separately owned executor lifetime must also cancel this caller-derived tool.
	defer func() {
		stopClose()
		cancelCall()
	}()
	execCtx, cancel := context.WithTimeout(callCtx, timeout)
	defer cancel()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return e.failed(call, sessionturn.ErrSessionClosed)
	}
	if e.activeCount == 0 {
		e.activeIdle = make(chan struct{})
	}
	e.activeCount++
	e.mu.Unlock()
	defer e.finishCall()
	if err := execCtx.Err(); err != nil {
		return e.finish(call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, false, err)
	}
	resultCh := make(chan toolExecutionResult, 1)
	go func() {
		response, err := invokeTool(execCtx, e.inner, call)
		resultCh <- toolExecutionResult{response: response, err: err}
	}()
	var response messages.ToolCallResponse
	var err error
	select {
	case result := <-resultCh:
		response, err = result.response, result.err
	case <-ctx.Done():
		return e.finish(call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, false, ctx.Err())
	case <-execCtx.Done():
		return e.failed(call, fmt.Errorf("%w after %s", errToolTimeout, timeout))
	}
	if ctx.Err() != nil {
		return e.finish(call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, false, ctx.Err())
	}
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		return e.failed(call, fmt.Errorf("%w after %s", errToolTimeout, timeout))
	}
	if err != nil {
		return e.failed(call, err)
	}
	response.ToolCallID, response.Name = call.ID, call.Name
	return e.finish(call, response, toolResponseFailed(response.Content), nil)
}

func (e *toolExecutor) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		if e.cancel != nil {
			e.cancel()
		}
		idle := e.activeIdle
		e.mu.Unlock()
		<-idle
	})
	return e.closeErr
}

func (e *toolExecutor) finishCall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeCount--
	if e.activeCount == 0 {
		close(e.activeIdle)
	}
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
