package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
)

// errCancelled is the cause of a delegation stopped by Cancel.
var errCancelled = errors.New("live delegation cancelled")

// job is one delegation, queued or running.
type job struct {
	value  messages.DelegationCreatedValue
	cancel context.CancelCauseFunc
}

// executor is a bounded worker pool. Up to limit delegations run at once,
// each on its own goroutine with a context derived from the session context,
// never from a tool or response context, so a user interrupt cannot reach
// it. Work beyond the limit waits in a FIFO queue that is never truncated.
type executor struct {
	ctx     context.Context //nolint:containedctx // The session lifetime every delegation derives from.
	stop    context.CancelFunc
	binding livedelegation.Binding
	backend *backendBuilder
	limits  livedelegation.Limits
	tools   *toolGate

	mu      sync.Mutex
	closed  bool
	queue   []*job
	running map[uint64]*job
	started uint64
	wg      sync.WaitGroup
}

func newExecutor(ctx context.Context, binding livedelegation.Binding, backend *backendBuilder) *executor {
	sessionCtx, stop := context.WithCancel(ctx)
	return &executor{
		ctx: sessionCtx, stop: stop, binding: binding, backend: backend,
		limits:  effectiveLimits(binding.Policy.Limits),
		tools:   newToolGate(binding.Serialized),
		running: make(map[uint64]*job),
	}
}

func effectiveLimits(limits livedelegation.Limits) livedelegation.Limits {
	if limits.Concurrency <= 0 {
		limits.Concurrency = livedelegation.DefaultConcurrency
	}
	if limits.MaxTurns <= 0 {
		limits.MaxTurns = livedelegation.DefaultMaxTurns
	}
	if limits.MaxDuration <= 0 {
		limits.MaxDuration = livedelegation.DefaultMaxDuration
	}
	if limits.MaxTokens <= 0 {
		limits.MaxTokens = livedelegation.DefaultMaxTokens
	}
	return limits
}

// Submit queues value and starts it when a worker is free.
func (e *executor) Submit(value messages.DelegationCreatedValue) error {
	value.Transcript = append([]messages.TranscriptFragment(nil), value.Transcript...)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ctx.Err() != nil {
		return livedelegation.ErrClosed
	}
	e.queue = append(e.queue, &job{value: value})
	e.dispatchLocked()
	return nil
}

// dispatchLocked starts queued jobs while workers are free.
func (e *executor) dispatchLocked() {
	for !e.closed && len(e.queue) > 0 && len(e.running) < e.limits.Concurrency {
		next := e.queue[0]
		e.queue[0] = nil
		e.queue = e.queue[1:]
		ctx, cancel := context.WithCancelCause(e.ctx)
		next.cancel = cancel
		// Jobs are keyed by admission order, so a repeated delegation id
		// still runs as its own job.
		e.started++
		e.running[e.started] = next
		e.wg.Add(1)
		go e.work(ctx, e.started, next)
	}
}

func (e *executor) work(ctx context.Context, key uint64, current *job) {
	defer e.wg.Done()
	defer current.cancel(nil)
	newRun(e, current.value).execute(ctx)
	e.mu.Lock()
	delete(e.running, key)
	// The next delegation derives from the session context, never from this
	// finished delegation's context, which Cancel may have ended.
	e.dispatchLocked() //nolint:contextcheck // Queued work inherits the session context, not the finished job's.
	e.mu.Unlock()
}

// Cancel stops a queued or running delegation without answering it.
func (e *executor) Cancel(delegationID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, queued := range e.queue {
		if queued.value.ID == delegationID {
			e.queue = append(e.queue[:i], e.queue[i+1:]...)
			return true
		}
	}
	found := false
	for _, running := range e.running {
		if running.value.ID == delegationID {
			running.cancel(errCancelled)
			found = true
		}
	}
	return found
}

// Close cancels everything and waits for the workers.
func (e *executor) Close() error {
	e.mu.Lock()
	e.closed = true
	e.queue = nil
	e.mu.Unlock()
	e.stop()
	e.wg.Wait()
	return nil
}
