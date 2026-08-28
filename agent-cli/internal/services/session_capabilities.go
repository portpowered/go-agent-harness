package services

import (
	"errors"
	"fmt"
	"sync"
)

// ErrSessionCapabilityCleanupPanic identifies a capability cleanup hook that
// panicked. Cleanup must not be able to bypass the remaining session
// finalizers or crash the command process.
var ErrSessionCapabilityCleanupPanic = errors.New("session capability cleanup panicked")

// SessionCapabilityCoordinator owns the cleanup hook transferred from a
// session capability factory. It runs cleanup functions in declaration order,
// attempts every function even after an error, and records the aggregate so
// repeated calls are safe and return the same result without doing new work.
//
// The coordinator is intentionally small: browser-specific ownership remains
// inside the capability hook (normally the WebMCP broker), while session
// runtime code owns the one transfer point and lifecycle invocation.
type SessionCapabilityCoordinator struct {
	once     sync.Once
	mu       sync.Mutex
	closed   bool
	closeErr error
	cleanups []func() error
}

// NewSessionCapabilityCoordinator creates an idempotent capability cleanup
// owner. Nil cleanup functions are ignored, which lets callers preserve a
// nil/no-resources capability without adding special branches to finalizers.
func NewSessionCapabilityCoordinator(cleanups ...func() error) *SessionCapabilityCoordinator {
	owned := make([]func() error, 0, len(cleanups))
	for _, cleanup := range cleanups {
		if cleanup != nil {
			owned = append(owned, cleanup)
		}
	}
	return &SessionCapabilityCoordinator{cleanups: owned}
}

// Close attempts every transferred cleanup in order. A panic from one hook is
// converted to an error and does not prevent later hooks from running.
func (c *SessionCapabilityCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()

		var closeErrs []error
		for _, cleanup := range c.cleanups {
			if err := invokeSessionCapabilityCleanup(cleanup); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		c.closeErr = errors.Join(closeErrs...)
	})
	return c.closeErr
}

// isClosed reports whether this coordinator has started its one cleanup
// attempt. It is used by outer command guards to cover pre-plan failures
// without duplicating an error already returned by a runtime plan finalizer.
func (c *SessionCapabilityCoordinator) isClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func invokeSessionCapabilityCleanup(cleanup func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", ErrSessionCapabilityCleanupPanic, recovered)
		}
	}()
	return cleanup()
}

// prepareSessionCapabilityCoordinator converts a caller-provided hook into a
// coordinator once. Keeping the coordinator in the value options allows all
// nested session wrappers (audio, image, duration, and recording) to share
// one ownership token instead of independently closing the capability.
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
	if coordinator == nil || coordinator.isClosed() {
		return
	}
	if closeErr := coordinator.Close(); closeErr != nil {
		*runErr = errors.Join(*runErr, wrapSessionPhaseError("close session capabilities", closeErr))
	}
}

func closeSessionCapabilityForPlan(coordinator *SessionCapabilityCoordinator, runErr *error) {
	if coordinator == nil {
		return
	}
	if closeErr := coordinator.Close(); closeErr != nil {
		*runErr = errors.Join(*runErr, wrapSessionPhaseError("close session capabilities", closeErr))
	}
}
