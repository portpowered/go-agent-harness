package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type refreshingExecutor struct {
	mu      sync.RWMutex
	current public.ToolExecutor
}

func newRefreshingExecutor(executor public.ToolExecutor) *refreshingExecutor {
	return &refreshingExecutor{current: executor}
}

func (e *refreshingExecutor) Execute(ctx context.Context, call public.ToolCall) (public.ToolCallResponse, error) {
	current := e.load()
	if current == nil {
		return public.ToolCallResponse{}, fmt.Errorf("room capability executor is unavailable")
	}
	return current.Execute(ctx, call)
}

func (e *refreshingExecutor) IsPageSightTool(name string) bool {
	router, ok := e.load().(runtimeTools.PageSightToolRouter)
	return ok && router.IsPageSightTool(name)
}

func (e *refreshingExecutor) ResolvesDynamicTools() bool {
	router, ok := e.load().(runtimeTools.DynamicToolRouter)
	return ok && router.ResolvesDynamicTools()
}

func (e *refreshingExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	rechecker, ok := e.load().(runtimeTools.ScreenRecordingPermissionRechecker)
	return ok && rechecker.ScreenRecordingPermissionRecheckSupported()
}

func (e *refreshingExecutor) RecheckScreenRecordingPermission(ctx context.Context) (runtimeTools.DisplayPermission, error) {
	rechecker, ok := e.load().(runtimeTools.ScreenRecordingPermissionRechecker)
	if !ok {
		return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionUnavailable, Reason: "screen recording permission re-check is unavailable"}, nil
	}
	return rechecker.RecheckScreenRecordingPermission(ctx)
}

func (e *refreshingExecutor) Replace(executor public.ToolExecutor) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.current = executor
	e.mu.Unlock()
}

func (e *refreshingExecutor) load() public.ToolExecutor {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current
}

type lifecycle struct {
	initializeFn  func(context.Context) error
	refreshFn     func(context.Context) ([]public.ToolDefinition, error)
	closeFn       func() error
	closeTimeout  time.Duration
	initialize    sync.Once
	initializeErr error
	closeOnce     sync.Once
	closeErr      error
}

func newLifecycle(initialize func(context.Context) error, refresh func(context.Context) ([]public.ToolDefinition, error), closeFn func() error, closeTimeout time.Duration) *lifecycle {
	return &lifecycle{initializeFn: initialize, refreshFn: refresh, closeFn: closeFn, closeTimeout: closeTimeout}
}

func (l *lifecycle) Initialize(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("capability initialization context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.initializeFn == nil {
		return nil
	}
	l.initialize.Do(func() { l.initializeErr = l.initializeFn(ctx) })
	return l.initializeErr
}

func (l *lifecycle) RefreshDefinitions(ctx context.Context) ([]public.ToolDefinition, error) {
	if l == nil {
		return nil, nil
	}
	if ctx == nil {
		return nil, fmt.Errorf("capability refresh context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l.refreshFn == nil {
		return nil, nil
	}
	return l.refreshFn(ctx)
}

func (l *lifecycle) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() { l.closeErr = closeWithTimeout(l.closeFn, l.closeTimeout) })
	return l.closeErr
}

func closeWithTimeout(closeFn func() error, timeout time.Duration) error {
	if closeFn == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = public.DefaultCloseTimeout
	}
	done := make(chan error, 1)
	go func() { done <- invokeClose(closeFn) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%w after %s", public.ErrCapabilityCloseTimeout, timeout)
	}
}

func invokeClose(closeFn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", public.ErrCapabilityClosePanic, recovered)
		}
	}()
	return closeFn()
}
