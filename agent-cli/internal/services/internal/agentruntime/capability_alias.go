package agentruntime

import (
	"errors"
	"time"

	sessioncapability "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/sessioncapability"
)

type SessionCapabilityCoordinator = sessioncapability.SessionCapabilityCoordinator

var ErrSessionCapabilityCleanupPanic = sessioncapability.ErrSessionCapabilityCleanupPanic
var ErrSessionCapabilityCleanupTimeout = sessioncapability.ErrSessionCapabilityCleanupTimeout

func NewSessionCapabilityCoordinator(cleanups ...func() error) *SessionCapabilityCoordinator {
	return sessioncapability.NewSessionCapabilityCoordinator(cleanups...)
}

func NewSessionCapabilityCoordinatorWithTimeout(timeout time.Duration, cleanups ...func() error) *SessionCapabilityCoordinator {
	return sessioncapability.NewSessionCapabilityCoordinatorWithTimeout(timeout, cleanups...)
}

func prepareSessionCapabilityCoordinator(opts SessionRunOptions) (SessionRunOptions, *SessionCapabilityCoordinator) {
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

func closeSessionCapabilityIfNeeded(coordinator *SessionCapabilityCoordinator, runErr *error) {
	if coordinator == nil || coordinator.IsClosed() {
		return
	}
	if closeErr := coordinator.Close(); closeErr != nil {
		*runErr = errors.Join(*runErr, wrapSessionPhaseError("close session capabilities", closeErr))
	}
}
