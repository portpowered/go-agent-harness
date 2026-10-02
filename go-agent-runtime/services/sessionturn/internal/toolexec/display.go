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
//
// A panic recovered from the support probe or the re-check is returned as
// panicked, for the caller to fold into the call's single timeout
// diagnostic. A re-check that panics only after its bound has expired, when
// the caller has already reported the timeout, records its own diagnostic.
func (e *Executor) deniedScreenPermission(ctx context.Context, call messages.ToolCall) (permission tools.DisplayPermission, denied bool, panicked error) {
	if contextDone(ctx) || !e.presentation.displayTool(call.Name) || e.pageSightTool(call) || e.presentation.DisplayPermissionDenied == nil {
		return tools.DisplayPermission{}, false, nil
	}
	rechecker, ok := e.inner.(tools.ScreenRecordingPermissionRechecker)
	if !ok {
		return tools.DisplayPermission{}, false, nil
	}
	supported, err := recheckSupported(rechecker)
	if err != nil || !supported {
		return tools.DisplayPermission{}, false, err
	}
	result, arrived := e.boundedRecheck(ctx, call, rechecker)
	switch {
	case !arrived:
		return tools.DisplayPermission{}, false, nil
	case isPanic(result.err):
		return tools.DisplayPermission{}, false, result.err
	case contextDone(ctx) || result.err != nil || result.permission.State != tools.DisplayPermissionDenied:
		return tools.DisplayPermission{}, false, nil
	}
	return result.permission, true, nil
}

// boundedRecheck runs the re-check within e.recheckLimit and reports whether
// its result arrived in time. resultCh is unbuffered and abandoned is closed
// only here, so exactly one side owns the result: this function when it
// receives it, the worker (which records a late panic) once it has given up.
func (e *Executor) boundedRecheck(ctx context.Context, call messages.ToolCall, rechecker tools.ScreenRecordingPermissionRechecker) (recheckResult, bool) {
	recheckCtx, cancel := context.WithTimeout(ctx, e.recheckLimit)
	defer cancel()
	resultCh := make(chan recheckResult)
	abandoned := make(chan struct{})
	go func() {
		permission, err := recheck(recheckCtx, rechecker)
		select {
		case resultCh <- recheckResult{permission: permission, err: err}:
		case <-abandoned:
			if isPanic(err) {
				e.recordDiagnostic(call, err)
			}
		}
	}()
	select {
	case result := <-resultCh:
		return result, true
	case <-recheckCtx.Done():
		close(abandoned)
		return recheckResult{}, false
	}
}

func isPanic(err error) bool {
	var panicked *panicError
	return errors.As(err, &panicked)
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

// contextDone reports whether ctx has ended. A session that ended before or
// during the re-check is no permission finding and no re-check failure.
func contextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
