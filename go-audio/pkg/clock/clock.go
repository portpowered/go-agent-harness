// Package clock provides host-time and deterministic time sources.
//
// Deterministic is a single virtual time domain. Its logical tick is an
// observation used by the loop, while elapsed time can also be advanced
// directly for schedulers and replay. Timers and context deadlines therefore
// do not depend on a loop tick being produced.
package clock

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Source supplies the current time to code that should not depend directly on
// the host clock.
type Source interface{ Now() time.Time }

// Timer is the small timer contract used by components that need to share a
// clock's notion of time. It intentionally mirrors the useful subset of
// time.Timer so callers can stop every timer they create on teardown.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// TimerSource is an optional extension to Source. Real and deterministic
// clocks implement it; callers that only need timestamps can continue to
// depend on Source.
type TimerSource interface {
	Source
	NewTimer(time.Duration) Timer
}

// Scheduler is the complete application-owned timing contract. Implementing
// this interface means waits and context deadlines use the same time domain
// as timestamps and timers.
type Scheduler interface {
	TimerSource
	Wait(context.Context, time.Duration) error
	WithDeadline(context.Context, time.Time) (context.Context, context.CancelFunc)
	WithTimeout(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

// Real reads the host wall clock.
type Real struct{}

// Now returns the current host time.
func (Real) Now() time.Time { return time.Now() }

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time {
	if t.timer == nil {
		return nil
	}
	return t.timer.C
}
func (t realTimer) Stop() bool { return t.timer != nil && t.timer.Stop() }

// NewTimer creates a host-time timer for components that opt into the
// TimerSource extension.
func (Real) NewTimer(duration time.Duration) Timer {
	return realTimer{timer: time.NewTimer(duration)}
}

// Wait blocks until duration has elapsed on the host clock or ctx is done.
func (r Real) Wait(ctx context.Context, duration time.Duration) error { return wait(ctx, r, duration) }

// WithDeadline creates a context canceled at deadline according to the host
// clock, while retaining cancellation and values from parent.
func (r Real) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return withDeadline(parent, r, deadline)
}

// WithTimeout creates a context canceled after timeout according to the host
// clock, while retaining cancellation and values from parent.
func (r Real) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return r.WithDeadline(parent, r.Now().Add(timeout))
}

// Deterministic is a monotonic virtual clock with a stable base timestamp.
// Its configuration is immutable after construction and its current tick and
// elapsed time are safe to share between goroutines.
type Deterministic struct {
	base         time.Time
	tickDuration time.Duration
	tick         atomic.Uint64
	elapsedNanos atomic.Int64

	// advanceMu makes a time advance and due-timer extraction one atomic
	// scheduler operation. Now remains lock-free for timestamp readers.
	advanceMu sync.Mutex
	timers    deterministicTimerHeap
	sequence  uint64
}

// NewDeterministic creates a clock at tick zero. A zero base uses the Unix
// epoch in UTC, and a non-positive tick duration uses one millisecond.
func NewDeterministic(base time.Time, tickDuration time.Duration) *Deterministic {
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}
	if tickDuration <= 0 {
		tickDuration = time.Millisecond
	}
	return &Deterministic{base: base, tickDuration: tickDuration}
}

// Now returns the timestamp at the current virtual elapsed time.
func (d *Deterministic) Now() time.Time {
	if d == nil {
		return time.Time{}
	}
	return d.base.Add(time.Duration(d.elapsedNanos.Load()))
}

// Tick returns the current logical tick. Ticks are observations and do not
// limit the elapsed-time scheduler.
func (d *Deterministic) Tick() uint64 {
	if d == nil {
		return 0
	}
	return d.tick.Load()
}

// Elapsed returns virtual elapsed time since construction. It can advance
// independently of Tick.
func (d *Deterministic) Elapsed() time.Duration {
	if d == nil {
		return 0
	}
	return time.Duration(d.elapsedNanos.Load())
}

// Advance moves the clock forward by one logical tick and returns the
// resulting tick. It saturates at the maximum uint64 value instead of
// wrapping backward. The elapsed timeline advances by tickDuration unless it
// has already been moved farther by AdvanceBy.
func (d *Deterministic) Advance() uint64 {
	if d == nil {
		return 0
	}
	d.advanceMu.Lock()
	current := d.tick.Load()
	if current != ^uint64(0) {
		current++
		d.tick.Store(current)
		d.advanceElapsedLocked(d.tickDuration)
	}
	d.fireDueTimersLocked()
	d.advanceMu.Unlock()
	return current
}

// AdvanceTo moves the clock to target when target is later than the current
// tick and returns the resulting tick. It never moves the clock backward.
// The elapsed timeline advances to at least target*tickDuration, preserving
// any greater direct virtual-time advancement.
func (d *Deterministic) AdvanceTo(target uint64) uint64 {
	if d == nil {
		return 0
	}
	d.advanceMu.Lock()
	current := d.tick.Load()
	if target > current {
		d.tick.Store(target)
		if elapsed, ok := multiplyDuration(target, d.tickDuration); ok {
			d.setElapsedAtLeastLocked(elapsed)
		} else {
			d.setElapsedAtLeastLocked(maxDuration)
		}
	}
	d.fireDueTimersLocked()
	result := d.tick.Load()
	d.advanceMu.Unlock()
	return result
}

// AdvanceBy advances virtual elapsed time by duration without producing a
// logical tick. Non-positive durations do not move time, and overflow
// saturates at the largest representable duration.
func (d *Deterministic) AdvanceBy(duration time.Duration) time.Time {
	if d == nil {
		return time.Time{}
	}
	d.advanceMu.Lock()
	d.advanceElapsedLocked(duration)
	d.fireDueTimersLocked()
	d.advanceMu.Unlock()
	return d.Now()
}

// AdvanceElapsed is an explicit alias for AdvanceBy for callers that want to
// emphasize that ticks and elapsed time are separate dimensions.
func (d *Deterministic) AdvanceElapsed(duration time.Duration) time.Time {
	return d.AdvanceBy(duration)
}

// AdvanceToElapsed advances virtual elapsed time to target, never backward,
// and returns the resulting timestamp.
func (d *Deterministic) AdvanceToElapsed(target time.Duration) time.Time {
	if d == nil {
		return time.Time{}
	}
	d.advanceMu.Lock()
	if target > time.Duration(d.elapsedNanos.Load()) {
		if target < 0 {
			target = 0
		}
		d.elapsedNanos.Store(int64(target))
	}
	d.fireDueTimersLocked()
	d.advanceMu.Unlock()
	return d.Now()
}

// AdvanceToTime advances virtual time to target, interpreted relative to the
// clock's base timestamp, and returns the resulting timestamp.
func (d *Deterministic) AdvanceToTime(target time.Time) time.Time {
	if d == nil {
		return time.Time{}
	}
	delta := target.Sub(d.base)
	if delta < 0 {
		delta = 0
	}
	return d.AdvanceToElapsed(delta)
}

// NewTimer creates an ordered timer whose deadline is evaluated against the
// current virtual elapsed timestamp. Advancing elapsed time directly is
// sufficient to deliver it; no loop tick is required.
func (d *Deterministic) NewTimer(duration time.Duration) Timer {
	if d == nil {
		return nil
	}
	d.advanceMu.Lock()
	d.sequence++
	nowElapsed := time.Duration(d.elapsedNanos.Load())
	deadlineElapsed := saturatingAdd(nowElapsed, duration)
	timer := &deterministicTimer{
		clock: d, deadlineElapsed: deadlineElapsed, deadline: d.base.Add(deadlineElapsed),
		sequence: d.sequence, ch: make(chan time.Time, 1), active: true, index: -1,
	}
	heap.Push(&d.timers, timer)
	d.fireDueTimersLocked()
	d.advanceMu.Unlock()
	return timer
}

// Wait blocks until duration has elapsed in this virtual time domain or ctx
// is done. Cancellation always wins when it is observed first.
func (d *Deterministic) Wait(ctx context.Context, duration time.Duration) error {
	return wait(ctx, d, duration)
}

// WithDeadline creates a context canceled at deadline in this virtual time
// domain. Parent cancellation and values are preserved.
func (d *Deterministic) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return withDeadline(parent, d, deadline)
}

// WithTimeout creates a context canceled after timeout in this virtual time
// domain. Parent cancellation and values are preserved.
func (d *Deterministic) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if d == nil {
		return withDeadline(parent, d, time.Time{})
	}
	return d.WithDeadline(parent, d.Now().Add(timeout))
}

func (d *Deterministic) advanceElapsedLocked(delta time.Duration) {
	if delta <= 0 {
		return
	}
	current := time.Duration(d.elapsedNanos.Load())
	d.elapsedNanos.Store(int64(saturatingAdd(current, delta)))
}
func (d *Deterministic) setElapsedAtLeastLocked(target time.Duration) {
	if target > time.Duration(d.elapsedNanos.Load()) {
		d.elapsedNanos.Store(int64(target))
	}
}
func (d *Deterministic) fireDueTimersLocked() {
	nowElapsed := time.Duration(d.elapsedNanos.Load())
	for d.timers.Len() > 0 {
		timer := d.timers[0]
		if timer.deadlineElapsed > nowElapsed {
			return
		}
		heap.Pop(&d.timers)
		timer.fire(d.base.Add(nowElapsed))
	}
}

type deterministicTimer struct {
	clock           *Deterministic
	deadline        time.Time
	deadlineElapsed time.Duration
	sequence        uint64
	ch              chan time.Time
	active          bool
	index           int
}

func (t *deterministicTimer) C() <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.ch
}
func (t *deterministicTimer) Stop() bool {
	if t == nil || t.clock == nil {
		return false
	}
	t.clock.advanceMu.Lock()
	if !t.active {
		t.clock.advanceMu.Unlock()
		return false
	}
	t.active = false
	if t.index >= 0 {
		heap.Remove(&t.clock.timers, t.index)
		t.index = -1
	}
	t.clock.advanceMu.Unlock()
	return true
}
func (t *deterministicTimer) fire(at time.Time) {
	if t == nil || !t.active {
		return
	}
	t.active, t.index = false, -1
	select {
	case t.ch <- at:
	default:
	}
}

type deterministicTimerHeap []*deterministicTimer

func (h deterministicTimerHeap) Len() int { return len(h) }
func (h deterministicTimerHeap) Less(i, j int) bool {
	if h[i].deadlineElapsed != h[j].deadlineElapsed {
		return h[i].deadlineElapsed < h[j].deadlineElapsed
	}
	return h[i].sequence < h[j].sequence
}
func (h deterministicTimerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *deterministicTimerHeap) Push(value any) {
	timer := value.(*deterministicTimer)
	timer.index = len(*h)
	*h = append(*h, timer)
}
func (h *deterministicTimerHeap) Pop() any {
	old := *h
	n := len(old)
	timer := old[n-1]
	old[n-1] = nil
	timer.index = -1
	*h = old[:n-1]
	return timer
}

// Ensure returns source unchanged when it is supplied, or a Real source when
// source is nil. This compatibility helper is intentionally separate from
// RequireTimerSource: deterministic callers should use the latter so a source
// without scheduling support cannot silently become host time.
func Ensure(source Source) Source {
	if source == nil {
		return Real{}
	}
	return source
}

// ErrTimerSourceUnavailable indicates that a Source does not provide the
// timer scheduler required for cancellation-aware waits and deadlines.
var ErrTimerSourceUnavailable = errors.New("clock: source does not provide timers")

// RequireTimerSource resolves source without substituting a host clock. A
// nil source is treated as an absent dependency and returns an error.
func RequireTimerSource(source Source) (TimerSource, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: nil source", ErrTimerSourceUnavailable)
	}
	timerSource, ok := source.(TimerSource)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrTimerSourceUnavailable, source)
	}
	return timerSource, nil
}

// Wait uses source's timer domain and returns an error instead of falling back
// to host time when source lacks scheduling support.
func Wait(ctx context.Context, source Source, duration time.Duration) error {
	timerSource, err := RequireTimerSource(source)
	if err != nil {
		return err
	}
	return wait(ctx, timerSource, duration)
}

// WithDeadline uses source's timer domain. It returns an error instead of
// falling back to host time when source lacks scheduling support.
func WithDeadline(parent context.Context, source Source, deadline time.Time) (context.Context, context.CancelFunc, error) {
	timerSource, err := RequireTimerSource(source)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := withDeadline(parent, timerSource, deadline)
	return ctx, cancel, nil
}

// WithTimeout uses source's timer domain. It returns an error instead of
// falling back to host time when source lacks scheduling support.
func WithTimeout(parent context.Context, source Source, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	timerSource, err := RequireTimerSource(source)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := withDeadline(parent, timerSource, timerSource.Now().Add(timeout))
	return ctx, cancel, nil
}

func wait(ctx context.Context, source TimerSource, duration time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := source.NewTimer(duration)
	if timer == nil {
		return ErrTimerSourceUnavailable
	}
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return contextCause(ctx)
	case <-timer.C():
		return nil
	}
}

func withDeadline(parent context.Context, source TimerSource, deadline time.Time) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	child := newDeadlineContext(parent, deadline)
	if err := parent.Err(); err != nil {
		child.finish(contextCause(parent))
		return child, func() { child.finish(context.Canceled) }
	}
	timer := source.NewTimer(deadline.Sub(source.Now()))
	stop := make(chan struct{})
	var stopOnce sync.Once
	cancel := func() {
		stopOnce.Do(func() { close(stop) })
		child.finish(context.Canceled)
	}
	go func() {
		defer timer.Stop()
		select {
		case <-parent.Done():
			child.finish(contextCause(parent))
		case <-timer.C():
			child.finish(context.DeadlineExceeded)
		case <-stop:
		}
	}()
	return child, cancel
}

type deadlineContext struct {
	parent   context.Context
	deadline time.Time
	done     chan struct{}
	mu       sync.Mutex
	err      error
	once     sync.Once
}

func newDeadlineContext(parent context.Context, deadline time.Time) *deadlineContext {
	return &deadlineContext{parent: parent, deadline: deadline, done: make(chan struct{})}
}
func (c *deadlineContext) Deadline() (time.Time, bool) {
	parentDeadline, ok := c.parent.Deadline()
	if ok && parentDeadline.Before(c.deadline) {
		return parentDeadline, true
	}
	return c.deadline, true
}
func (c *deadlineContext) Done() <-chan struct{} { return c.done }
func (c *deadlineContext) Err() error            { c.mu.Lock(); err := c.err; c.mu.Unlock(); return err }
func (c *deadlineContext) Value(key any) any     { return c.parent.Value(key) }
func (c *deadlineContext) Cause() error          { return c.Err() }
func (c *deadlineContext) finish(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = err; c.mu.Unlock(); close(c.done) })
}
func contextCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

const maxDuration = time.Duration(1<<63 - 1)

func saturatingAdd(current, delta time.Duration) time.Duration {
	if delta > 0 && current > maxDuration-delta {
		return maxDuration
	}
	if delta < 0 && current < -maxDuration-delta {
		return 0
	}
	result := current + delta
	if result < 0 {
		return 0
	}
	return result
}
func multiplyDuration(value uint64, unit time.Duration) (time.Duration, bool) {
	if value == 0 || unit <= 0 {
		return 0, true
	}
	if value > uint64(maxDuration)/uint64(unit) {
		return 0, false
	}
	return time.Duration(value) * unit, true
}

var (
	_ Source      = Real{}
	_ Source      = (*Deterministic)(nil)
	_ TimerSource = Real{}
	_ TimerSource = (*Deterministic)(nil)
	_ Scheduler   = Real{}
	_ Scheduler   = (*Deterministic)(nil)
)
