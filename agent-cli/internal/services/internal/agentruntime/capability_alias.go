package agentruntime

import (
	"context"
	"errors"
	"time"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

type SessionCapabilityCoordinator = runtimeTools.CleanupCoordinator

var ErrSessionCapabilityCleanupPanic = runtimeTools.ErrCapabilityClosePanic
var ErrSessionCapabilityCleanupTimeout = runtimeTools.ErrCapabilityCloseTimeout

func NewSessionCapabilityCoordinator(cleanups ...func() error) SessionCapabilityCoordinator {
	return runtimeToolsWire.NewService().NewCleanupCoordinator(cleanups...)
}

func NewSessionCapabilityCoordinatorWithTimeout(timeout time.Duration, cleanups ...func() error) SessionCapabilityCoordinator {
	return runtimeToolsWire.NewService().NewCleanupCoordinatorWithTimeout(timeout, cleanups...)
}

func prepareSessionCapabilityCoordinator(opts SessionRunOptions) (SessionRunOptions, SessionCapabilityCoordinator) {
	if opts.capabilityCoordinator != nil {
		return opts, opts.capabilityCoordinator
	}
	if opts.CapabilityClose == nil {
		return opts, nil
	}
	coordinator := NewSessionCapabilityCoordinator(opts.CapabilityClose)
	opts.capabilityCoordinator = coordinator
	opts.CapabilityClose = coordinator.Close
	return opts, coordinator
}

func closeSessionCapabilityIfNeeded(coordinator SessionCapabilityCoordinator, runErr *error) {
	if coordinator == nil || coordinator.IsClosed() {
		return
	}
	if closeErr := coordinator.Close(); closeErr != nil {
		*runErr = errors.Join(*runErr, wrapSessionPhaseError("close session capabilities", closeErr))
	}
}

func safeScreenPermissionRecheckSupported(rechecker cliTools.ScreenRecordingPermissionRechecker) (supported bool) {
	defer func() {
		if recover() != nil {
			supported = false
		}
	}()
	return rechecker.ScreenRecordingPermissionRecheckSupported()
}

type runtimeScreenPermissionRechecker struct {
	inner runtimeTools.ScreenRecordingPermissionRechecker
}

func (r runtimeScreenPermissionRechecker) ScreenRecordingPermissionRecheckSupported() bool {
	return r.inner != nil && r.inner.ScreenRecordingPermissionRecheckSupported()
}

func (r runtimeScreenPermissionRechecker) RecheckScreenRecordingPermission(ctx context.Context) (cliTools.DisplayPermission, error) {
	permission, err := r.inner.RecheckScreenRecordingPermission(ctx)
	return cliTools.DisplayPermission{State: cliTools.DisplayPermissionState(permission.State), Reason: permission.Reason}, err
}

func sessionScreenPermissionRechecker(executor messages.ToolExecutor) (cliTools.ScreenRecordingPermissionRechecker, bool) {
	if executor == nil {
		return nil, false
	}
	if rechecker, ok := executor.(cliTools.ScreenRecordingPermissionRechecker); ok {
		return rechecker, true
	}
	if rechecker, ok := executor.(runtimeTools.ScreenRecordingPermissionRechecker); ok {
		return runtimeScreenPermissionRechecker{inner: rechecker}, true
	}
	return nil, false
}

func invokeScreenPermissionRecheck(ctx context.Context, rechecker cliTools.ScreenRecordingPermissionRechecker) (permission cliTools.DisplayPermission, err error) {
	defer func() {
		if recover() != nil {
			permission = cliTools.DisplayPermission{}
			err = errors.New("screen recording permission re-check panicked")
		}
	}()
	return rechecker.RecheckScreenRecordingPermission(ctx)
}
