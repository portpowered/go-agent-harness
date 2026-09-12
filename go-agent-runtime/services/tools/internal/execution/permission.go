package execution

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type permissionResult struct {
	permission public.DisplayPermission
	err        error
}

func (c *controller) permissionDeniedAfterTimeout(ctx context.Context, call messages.ToolCall) (error, bool) {
	if ctx == nil || ctx.Err() != nil || c.failureKind(call) != public.ToolExecutionFailurePhysicalDisplay {
		return nil, false
	}
	rechecker := c.permissionRechecker
	if rechecker == nil {
		var ok bool
		rechecker, ok = c.executor.(public.ScreenRecordingPermissionRechecker)
		if !ok {
			return nil, false
		}
	}
	if !safeRecheckSupported(rechecker) {
		return nil, false
	}
	recheckCtx, cancel := context.WithTimeout(ctx, public.ToolExecutionPermissionRecheckTimeout)
	defer cancel()
	resultCh := make(chan permissionResult, 1)
	go func() {
		permission, err := invokeRecheck(recheckCtx, rechecker)
		resultCh <- permissionResult{permission: permission, err: err}
	}()
	select {
	case result := <-resultCh:
		if ctx.Err() != nil || result.err != nil || result.permission.State != public.DisplayPermissionDenied {
			return nil, false
		}
		return permissionError(c.permissionError, result.permission.Reason), true
	case <-recheckCtx.Done():
		return nil, false
	}
}

func safeRecheckSupported(rechecker public.ScreenRecordingPermissionRechecker) (supported bool) {
	defer func() {
		if recover() != nil {
			supported = false
		}
	}()
	return rechecker.ScreenRecordingPermissionRecheckSupported()
}

func invokeRecheck(ctx context.Context, rechecker public.ScreenRecordingPermissionRechecker) (permission public.DisplayPermission, err error) {
	defer func() {
		if recover() != nil {
			permission = public.DisplayPermission{}
			err = errors.New("screen recording permission re-check panicked")
		}
	}()
	return rechecker.RecheckScreenRecordingPermission(ctx)
}
