package agentruntime

import (
	"errors"
	"time"

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

func releaseSessionClaim(claim *sessionRecordingClaim, runErr *error) {
	if claim == nil || runErr == nil {
		return
	}
	if err := claim.release(); err != nil {
		*runErr = errors.Join(*runErr, err)
	}
}
