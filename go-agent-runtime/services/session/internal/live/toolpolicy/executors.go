package toolpolicy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type timedExecutor struct {
	inner     messages.ToolExecutor
	scheduler platformclock.Scheduler
	timeout   time.Duration
}

func (e timedExecutor) AllowUnadvertisedTools() bool {
	replacement, ok := e.inner.(interface{ AllowUnadvertisedTools() bool })
	return ok && replacement.AllowUnadvertisedTools()
}

// NewTimedToolExecutor applies one legacy request-wide deadline.
func NewTimedToolExecutor(inner messages.ToolExecutor, scheduler platformclock.Scheduler, timeout time.Duration) messages.ToolExecutor {
	if inner == nil || timeout <= 0 {
		return inner
	}
	return timedExecutor{inner: inner, scheduler: scheduler, timeout: timeout}
}

func (e timedExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return messages.ToolCallResponse{}, err
	}
	if e.scheduler == nil {
		return messages.ToolCallResponse{}, session.ErrLiveSchedulerUnavailable
	}
	toolCtx, cancel := e.scheduler.WithTimeout(ctx, e.timeout)
	defer cancel()
	response, err := e.inner.Execute(toolCtx, call)
	if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return response, errors.Join(session.ErrLiveToolExecutionTimeout, err)
	}
	return response, err
}

type interactiveExecutor struct {
	inner           messages.ToolExecutor
	scheduler       platformclock.Scheduler
	policy          runtimeTools.InteractiveToolPolicy
	timeoutOverride time.Duration
}

// NewInteractiveToolExecutor applies class-specific deadlines at the live
// session boundary and returns expiry as a correlated tool result.
func NewInteractiveToolExecutor(inner messages.ToolExecutor, scheduler platformclock.Scheduler, policy runtimeTools.InteractiveToolPolicy, timeoutOverride time.Duration) messages.ToolExecutor {
	if inner == nil {
		return nil
	}
	if policy != nil {
		policy = policy.Clone()
	}
	if policy == nil && timeoutOverride <= 0 {
		return inner
	}
	return interactiveExecutor{inner: inner, scheduler: scheduler, policy: policy, timeoutOverride: timeoutOverride}
}

func (e interactiveExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return messages.ToolCallResponse{}, err
	}
	timeout := e.timeoutOverride
	if timeout <= 0 && e.policy != nil {
		timeout = e.policy.TimeoutForTool(call.Name)
	}
	if timeout <= 0 {
		return e.inner.Execute(ctx, call)
	}
	if e.scheduler == nil {
		return messages.ToolCallResponse{}, session.ErrLiveSchedulerUnavailable
	}
	toolCtx, cancel := e.scheduler.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := e.inner.Execute(toolCtx, call)
	if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return messages.ToolCallResponse{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("tool %q failed (classification=%s): %s after %s", call.Name, runtimeTools.InteractiveToolTimeoutClassification, session.ErrLiveToolExecutionTimeout, timeout),
		}, nil
	}
	return response, err
}

func (e interactiveExecutor) AllowUnadvertisedTools() bool {
	replacement, ok := e.inner.(interface{ AllowUnadvertisedTools() bool })
	return ok && replacement.AllowUnadvertisedTools()
}

// WithActiveCapture delays a tool result until the next active audio turn.
func WithActiveCapture(inner messages.ToolExecutor, wait func(context.Context) error) messages.ToolExecutor {
	if inner == nil {
		return nil
	}
	return activeCaptureExecutor{inner: inner, wait: wait}
}

type activeCaptureExecutor struct {
	inner messages.ToolExecutor
	wait  func(context.Context) error
}

func (e activeCaptureExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e.wait != nil {
		if err := e.wait(ctx); err != nil {
			return messages.ToolCallResponse{}, err
		}
	}
	return e.inner.Execute(ctx, call)
}

// NormalizeCapabilityLifecycle copies optional handle hooks onto the binding
// consumed by the live owner.
func NormalizeCapabilityLifecycle(binding *session.LiveCapabilities) {
	if binding == nil || binding.Handle == nil {
		return
	}
	binding.Initialize = binding.Handle.Initialize
	binding.RefreshDefinitions = binding.Handle.RefreshDefinitions
	binding.Close = binding.Handle.Close
	binding.BrowserWatch = nil
	if watcher, ok := binding.Handle.(session.LiveCapabilityWatcher); ok {
		binding.BrowserWatch = watcher.BrowserWatch
	}
}
