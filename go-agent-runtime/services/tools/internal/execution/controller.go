package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
)

type controller struct {
	executor              messages.ToolExecutor
	timeoutOverride       time.Duration
	interactivePolicy     public.InteractiveToolPolicy
	timeoutPolicy         public.ToolExecutionTimeoutPolicy
	useDefaultInteractive bool
	observer              public.ToolExecutionObserver
	cancellationIntent    public.ToolExecutionCancellationIntent
	diagnostics           public.ToolExecutionDiagnosticSink
	permissionRechecker   public.ScreenRecordingPermissionRechecker
	physicalMatcher       public.PhysicalDisplayToolMatcher
	screenErrorCode       public.ScreenToolErrorCode
	permissionError       public.PermissionDeniedErrorFactory
}

type executionResult struct {
	response messages.ToolCallResponse
	err      error
}

func newController(request public.ToolExecutionRequest) *controller {
	return &controller{
		executor:              request.Executor,
		timeoutOverride:       request.TimeoutOverride,
		interactivePolicy:     clonePolicy(request.InteractivePolicy),
		timeoutPolicy:         request.TimeoutForTool,
		useDefaultInteractive: request.UseDefaultInteractivePolicy,
		observer:              request.Observer,
		cancellationIntent:    request.CancellationIntent,
		diagnostics:           request.Diagnostics,
		permissionRechecker:   request.PermissionRechecker,
		physicalMatcher:       request.IsPhysicalDisplayTool,
		screenErrorCode:       request.ScreenErrorCode,
		permissionError:       request.PermissionDeniedError,
	}
}

// Execute keeps a tool-local deadline separate from the parent session and
// turns ordinary failures into one correlated result so the loop continues.
//
//nolint:contextcheck // nil has no caller lineage; background is the compatibility fallback.
func (c *controller) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return projectFailure(call, errors.New("tool execution controller is not configured"), public.ToolExecutionFailureGeneric, nil), nil
	}
	c.observeCall(call)
	return c.execute(ctx, call)
}

func (c *controller) execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if c.executor == nil {
		return c.finish(call, c.failure(call, errors.New("tool execution controller is not configured")), true)
	}

	timeout := c.timeoutForTool(call.Name)
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resultCh := startExecution(execCtx, c.executor, call)
	return c.await(ctx, execCtx, call, timeout, resultCh)
}

func (c *controller) await(ctx, execCtx context.Context, call messages.ToolCall, timeout time.Duration, resultCh <-chan executionResult) (messages.ToolCallResponse, error) {
	select {
	case result := <-resultCh:
		return c.complete(execCtx, call, result)
	case <-execCtx.Done():
		return c.deadline(ctx, execCtx, call, timeout)
	}
}

func startExecution(ctx context.Context, executor messages.ToolExecutor, call messages.ToolCall) <-chan executionResult {
	resultCh := make(chan executionResult, 1)
	go func() {
		response, err := invoke(ctx, executor, call)
		resultCh <- executionResult{response: response, err: err}
	}()
	return resultCh
}

func (c *controller) complete(execCtx context.Context, call messages.ToolCall, result executionResult) (messages.ToolCallResponse, error) {
	if result.err != nil {
		if c.sigintCancelled(execCtx, result.err) {
			return correlatedCancellation(call, result.err)
		}
		return c.finish(call, c.failure(call, result.err), true)
	}
	return c.finish(call, result.response, responseFailed(result.response.Content))
}

func (c *controller) deadline(ctx, execCtx context.Context, call messages.ToolCall, timeout time.Duration) (messages.ToolCallResponse, error) {
	if c.sigintCancelled(execCtx, execCtx.Err()) {
		return correlatedCancellation(call, execCtx.Err())
	}
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		if err, denied := c.permissionDeniedAfterTimeout(ctx, call); denied {
			return c.finish(call, c.failure(call, err), true)
		}
	}
	failure := contextFailure(execCtx.Err())
	if errors.Is(failure, public.ErrToolExecutionTimeout) {
		failure = fmt.Errorf("%w after %s", public.ErrToolExecutionTimeout, timeout)
	}
	return c.finish(call, c.failure(call, failure), true)
}

func (c *controller) finish(call messages.ToolCall, response messages.ToolCallResponse, failed bool) (messages.ToolCallResponse, error) {
	response.ToolCallID = call.ID
	response.Name = call.Name
	returned := cloneToolCallResponse(response)
	c.observeResult(call, cloneToolCallResponse(returned), failed)
	return returned, nil
}

func (c *controller) timeoutForTool(name string) time.Duration {
	if c.timeoutOverride > 0 {
		return c.timeoutOverride
	}
	if c.interactivePolicy != nil {
		if timeout := safePolicyTimeout(c.interactivePolicy, name); timeout > 0 {
			return timeout
		}
	}
	if timeout := safeTimeoutPolicy(c.timeoutPolicy, name); timeout > 0 {
		return timeout
	}
	if c.useDefaultInteractive {
		return public.DefaultInteractiveFastReadTimeout
	}
	return public.DefaultToolExecutionTimeout
}

func (c *controller) failure(call messages.ToolCall, err error) messages.ToolCallResponse {
	kind := c.failureKind(call)
	if c.diagnostics != nil {
		c.diagnostics.RecordToolExecutionDiagnostic(public.ToolExecutionDiagnostic{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Kind:       kind,
			Error:      err,
		})
	}
	return projectFailure(call, err, kind, c.screenErrorCode)
}

func (c *controller) failureKind(call messages.ToolCall) public.ToolExecutionFailureKind {
	if isPageSightTool(c.executor, call.Name) {
		return public.ToolExecutionFailurePageSight
	}
	if safePhysicalMatch(c.physicalMatcher, call.Name) {
		return public.ToolExecutionFailurePhysicalDisplay
	}
	return public.ToolExecutionFailureGeneric
}

func (c *controller) sigintCancelled(ctx context.Context, err error) bool {
	return safeSIGINT(c.cancellationIntent) && errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func correlatedCancellation(call messages.ToolCall, err error) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, err
}

func contextFailure(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return public.ErrToolExecutionTimeout
	case errors.Is(err, context.Canceled):
		return errors.New("tool execution canceled")
	default:
		return fmt.Errorf("tool execution stopped: %w", err)
	}
}

func invoke(ctx context.Context, executor messages.ToolExecutor, call messages.ToolCall) (response messages.ToolCallResponse, err error) {
	defer func() {
		if recover() != nil {
			response = messages.ToolCallResponse{}
			err = errors.New("tool executor panicked")
		}
	}()
	return executor.Execute(ctx, call)
}

func responseFailed(content string) bool {
	if refusal, ok := filesystem.FilesystemRefusalFromContent(content); ok {
		return refusal.Type != ""
	}
	var envelope struct {
		Version string `json:"version"`
		OK      *bool  `json:"ok"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return false
	}
	return envelope.Version == "webmcp.tool-result.v1" && envelope.OK != nil && !*envelope.OK
}
