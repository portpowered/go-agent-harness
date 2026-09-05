package sessioncapability

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrSessionCapabilityCleanupPanic identifies a capability cleanup hook that
// panicked. Cleanup must not be able to bypass the remaining session
// finalizers or crash the command process.
var (
	ErrSessionCapabilityCleanupPanic   = errors.New("session capability cleanup panicked")
	ErrSessionCapabilityCleanupTimeout = errors.New("session capability cleanup timed out")
)

// DefaultSessionCapabilityCleanupTimeout bounds one transferred capability
// cleanup hook. A non-cooperative browser adapter cannot be allowed to hold
// provider and recording finalization forever; the hook's goroutine may still
// finish later, but the session coordinator records the unresolved cleanup and
// continues its ordered shutdown.
const DefaultSessionCapabilityCleanupTimeout = 15 * time.Second

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
	timeout  time.Duration
}

// NewSessionCapabilityCoordinator creates an idempotent capability cleanup
// owner. Nil cleanup functions are ignored, which lets callers preserve a
// nil/no-resources capability without adding special branches to finalizers.
func NewSessionCapabilityCoordinator(cleanups ...func() error) *SessionCapabilityCoordinator {
	return NewSessionCapabilityCoordinatorWithTimeout(DefaultSessionCapabilityCleanupTimeout, cleanups...)
}

// NewSessionCapabilityCoordinatorWithTimeout creates an idempotent capability
// cleanup owner with an explicit per-hook bound. It is useful for adapters
// whose shutdown budget is known by the caller and for deterministic tests of
// non-cooperative cleanup behavior. A non-positive timeout uses the default.
func NewSessionCapabilityCoordinatorWithTimeout(timeout time.Duration, cleanups ...func() error) *SessionCapabilityCoordinator {
	if timeout <= 0 {
		timeout = DefaultSessionCapabilityCleanupTimeout
	}
	owned := make([]func() error, 0, len(cleanups))
	for _, cleanup := range cleanups {
		if cleanup != nil {
			owned = append(owned, cleanup)
		}
	}
	return &SessionCapabilityCoordinator{cleanups: owned, timeout: timeout}
}

// Close attempts every transferred cleanup in order. A panic from one hook is
// converted to an error and does not prevent later hooks from running.
func (c *SessionCapabilityCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		var closeErrs []error
		for index, cleanup := range c.cleanups {
			if err := invokeSessionCapabilityCleanupWithTimeout(cleanup, c.timeout, index); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		c.mu.Lock()
		c.closeErr = errors.Join(closeErrs...)
		// Mark the coordinator closed only after every cleanup hook has been
		// attempted. An outer command guard that races this call must wait for
		// the sync.Once and observe the complete recorded result.
		c.closed = true
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// IsClosed reports whether this coordinator has started its one cleanup
// attempt. It is used by outer command guards to cover pre-plan failures
// without duplicating an error already returned by a runtime plan finalizer.
func (c *SessionCapabilityCoordinator) IsClosed() bool {
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

func invokeSessionCapabilityCleanupWithTimeout(cleanup func() error, timeout time.Duration, index int) error {
	if timeout <= 0 {
		timeout = DefaultSessionCapabilityCleanupTimeout
	}
	done := make(chan error, 1)
	go func() {
		done <- invokeSessionCapabilityCleanup(cleanup)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("cleanup hook %d: %w after %s", index+1, ErrSessionCapabilityCleanupTimeout, timeout)
	}
}
