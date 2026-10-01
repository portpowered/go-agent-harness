package toolexec

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	errRecheckPanicked        sessionturn.Error = "screen recording permission re-check panicked"
	errRecheckSupportPanicked sessionturn.Error = "screen recording permission re-check support probe panicked"
)

type recheckResult struct {
	permission tools.DisplayPermission
	err        error
}

// deniedScreenPermission performs the one optional permission re-check
// allowed for a timed-out physical-display call. It uses the enclosing
// session context and bounds the checker independently so a slow checker
// cannot hold up the session.
func (e *Executor) deniedScreenPermission(ctx context.Context, call messages.ToolCall) (tools.DisplayPermission, bool) {
	if ctx.Err() != nil || !e.presentation.displayTool(call.Name) || e.pageSightTool(call) || e.presentation.DisplayPermissionDenied == nil {
		return tools.DisplayPermission{}, false
	}
	rechecker, ok := e.inner.(tools.ScreenRecordingPermissionRechecker)
	if !ok {
		return tools.DisplayPermission{}, false
	}
	supported, err := recheckSupported(rechecker)
	if err != nil {
		e.recordDiagnostic(call, err)
	}
	if !supported {
		return tools.DisplayPermission{}, false
	}
	recheckCtx, cancel := context.WithTimeout(ctx, e.recheckLimit)
	defer cancel()
	resultCh := make(chan recheckResult, 1)
	go func() {
		permission, err := recheck(recheckCtx, rechecker)
		resultCh <- recheckResult{permission: permission, err: err}
	}()
	select {
	case result := <-resultCh:
		var panicked *panicError
		if errors.As(result.err, &panicked) {
			e.recordDiagnostic(call, result.err)
		}
		if ctx.Err() != nil || result.err != nil || result.permission.State != tools.DisplayPermissionDenied {
			return tools.DisplayPermission{}, false
		}
		return result.permission, true
	case <-recheckCtx.Done():
		return tools.DisplayPermission{}, false
	}
}

func recheckSupported(rechecker tools.ScreenRecordingPermissionRechecker) (supported bool, err error) {
	defer func() {
		if value := recover(); value != nil {
			supported = false
			err = newPanicError(errRecheckSupportPanicked, value)
		}
	}()
	return rechecker.ScreenRecordingPermissionRecheckSupported(), nil
}

func recheck(ctx context.Context, rechecker tools.ScreenRecordingPermissionRechecker) (permission tools.DisplayPermission, err error) {
	defer func() {
		if value := recover(); value != nil {
			permission = tools.DisplayPermission{}
			err = newPanicError(errRecheckPanicked, value)
		}
	}()
	return rechecker.RecheckScreenRecordingPermission(ctx)
}
