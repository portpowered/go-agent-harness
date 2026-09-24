package probe

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// BargeInCoordinator owns the bounded context shared by event gates and
// observer workers in a proof. Workers must honor the context passed to their
// function; StopAndWait gives them a second, bounded join boundary during
// teardown instead of using an unbounded WaitGroup.Wait.
type BargeInCoordinator struct {
	ctx    context.Context
	cancel context.CancelFunc
	ledger *BargeInLedger
	bound  time.Duration

	workerMu    sync.Mutex
	workerCount int
	workersDone chan struct{}
}

// NewBargeInCoordinator creates one shared, bounded proof context.
func NewBargeInCoordinator(parent context.Context, timeout time.Duration, ledger *BargeInLedger) (*BargeInCoordinator, error) {
	if parent == nil {
		return nil, fmt.Errorf("%w: coordinator parent context is required", ErrBargeInWait)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: coordinator timeout must be positive", ErrBargeInWait)
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	coordinator := &BargeInCoordinator{
		ctx:         ctx,
		cancel:      cancel,
		ledger:      ledger,
		bound:       timeout,
		workersDone: make(chan struct{}),
	}
	close(coordinator.workersDone)
	return coordinator, nil
}

// Context is the shared cancellation path for all proof work.
func (c *BargeInCoordinator) Context() context.Context {
	if c == nil {
		return nil
	}
	return c.ctx
}

// Go starts one context-aware proof worker. Calls to Go must be complete before
// WaitForWorkers or StopAndWait begins.
func (c *BargeInCoordinator) Go(worker func(context.Context)) {
	if c == nil || worker == nil {
		return
	}
	c.workerMu.Lock()
	if c.workerCount == 0 {
		c.workersDone = make(chan struct{})
	}
	c.workerCount++
	c.workerMu.Unlock()
	go func() {
		defer c.workerFinished()
		worker(c.ctx)
	}()
}

func (c *BargeInCoordinator) workerFinished() {
	c.workerMu.Lock()
	defer c.workerMu.Unlock()
	c.workerCount--
	if c.workerCount == 0 {
		close(c.workersDone)
	}
}

// WaitFor waits for an event gate under the coordinator's shared deadline.
func (c *BargeInCoordinator) WaitFor(boundary string, signal <-chan struct{}) error {
	if c == nil {
		return fmt.Errorf("%w: coordinator is nil", ErrBargeInWait)
	}
	return c.ledger.WaitFor(c.ctx, boundary, signal, c.remaining())
}

// WaitForWorkers waits for all workers while respecting the shared deadline.
// A worker that ignores Context is reported as a bounded teardown failure.
func (c *BargeInCoordinator) WaitForWorkers(boundary string) error {
	if c == nil {
		return fmt.Errorf("%w: coordinator is nil", ErrBargeInWait)
	}
	c.workerMu.Lock()
	done := c.workersDone
	c.workerMu.Unlock()
	select {
	case <-done:
		return nil
	case <-c.ctx.Done():
		select {
		case <-done:
			return nil
		default:
		}
		return c.ledger.waitError(boundary, c.bound, c.ctx.Err())
	}
}

// StopAndWait cancels every worker and joins them for at most the smaller of
// the proof bound and one second. A cooperative worker normally returns before
// this grace bound; a non-cooperative worker produces a diagnostic error.
func (c *BargeInCoordinator) StopAndWait(boundary string) error {
	if c == nil {
		return fmt.Errorf("%w: coordinator is nil", ErrBargeInWait)
	}
	c.cancel()
	c.workerMu.Lock()
	done := c.workersDone
	c.workerMu.Unlock()
	joinTimeout := c.bound
	if joinTimeout > time.Second {
		joinTimeout = time.Second
	}
	timer := time.NewTimer(joinTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		select {
		case <-done:
			return nil
		default:
		}
		return c.ledger.waitError(boundary, joinTimeout, context.DeadlineExceeded)
	}
}

func (c *BargeInCoordinator) remaining() time.Duration {
	deadline, ok := c.ctx.Deadline()
	if !ok {
		return c.bound
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}
