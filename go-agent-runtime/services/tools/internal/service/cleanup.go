package service

import (
	"errors"
	"fmt"
	"sync"
	"time"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// defaultCleanupTimeout is kept private so the public service contract owns
// the documented default while this implementation owns the shutdown state.
const defaultCleanupTimeout = public.DefaultCapabilityCloseTimeout

type cleanupCoordinator struct {
	once     sync.Once
	mu       sync.Mutex
	closed   bool
	closeErr error
	cleanups []func() error
	timeout  time.Duration
}

var _ public.CleanupCoordinator = (*cleanupCoordinator)(nil)

func newCleanupCoordinator(cleanups ...func() error) public.CleanupCoordinator {
	return newCleanupCoordinatorWithTimeout(defaultCleanupTimeout, cleanups...)
}

func newCleanupCoordinatorWithTimeout(timeout time.Duration, cleanups ...func() error) public.CleanupCoordinator {
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	owned := make([]func() error, 0, len(cleanups))
	for _, cleanup := range cleanups {
		if cleanup != nil {
			owned = append(owned, cleanup)
		}
	}
	return &cleanupCoordinator{cleanups: owned, timeout: timeout}
}

func (c *cleanupCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		var closeErrs []error
		for index, cleanup := range c.cleanups {
			if err := invokeCleanupWithTimeout(cleanup, c.timeout, index); err != nil {
				closeErrs = append(closeErrs, err)
			}
		}
		c.mu.Lock()
		c.closeErr = errors.Join(closeErrs...)
		c.closed = true
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

func (c *cleanupCoordinator) IsClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func invokeCleanupWithTimeout(cleanup func() error, timeout time.Duration, index int) error {
	done := make(chan error, 1)
	go func() { done <- invokeCleanup(cleanup) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("cleanup hook %d: %w after %s", index+1, public.ErrCapabilityCloseTimeout, timeout)
	}
}

func invokeCleanup(cleanup func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", public.ErrCapabilityClosePanic, recovered)
		}
	}()
	return cleanup()
}
