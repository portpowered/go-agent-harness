// Package clock provides host-time and logical-tick time sources.
package clock

import (
	"sync"
	"sync/atomic"
	"time"
)

// Source supplies the current time to code that should not depend directly on
// the host clock.
type Source interface {
	Now() time.Time
}

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

// Real reads the host wall clock.
type Real struct{}

// Now returns the current host time.
func (Real) Now() time.Time {
	return time.Now()
}

type realTimer struct {
	timer *time.Timer
}

func (t realTimer) C() <-chan time.Time {
	if t.timer == nil {
		return nil
	}
	return t.timer.C
}

func (t realTimer) Stop() bool {
	return t.timer != nil && t.timer.Stop()
}

// NewTimer creates a host-time timer for components that opt into the
// TimerSource extension.
func (Real) NewTimer(duration time.Duration) Timer {
	return realTimer{timer: time.NewTimer(duration)}
}

// Deterministic maps a logical tick to a stable timestamp. Its configuration
// is immutable after construction and its current tick is safe to share
// between goroutines.
type Deterministic struct {
	base         time.Time
	tickDuration time.Duration
	tick         atomic.Uint64
	timersMu     sync.Mutex
	timers       map[*deterministicTimer]struct{}
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

	return &Deterministic{
		base:         base,
		tickDuration: tickDuration,
		timers:       make(map[*deterministicTimer]struct{}),
	}
}

// Now returns the timestamp for the tick observed by this call.
func (d *Deterministic) Now() time.Time {
	return d.base.Add(elapsed(d.tick.Load(), d.tickDuration))
}

// Tick returns the current logical tick.
func (d *Deterministic) Tick() uint64 {
	return d.tick.Load()
}

// Advance moves the clock forward by one tick and returns the resulting tick.
// It saturates at the maximum uint64 value instead of wrapping backward.
func (d *Deterministic) Advance() uint64 {
	for {
		current := d.tick.Load()
		if current == ^uint64(0) {
			d.fireDueTimers()
			return current
		}
		if d.tick.CompareAndSwap(current, current+1) {
			d.fireDueTimers()
			return current + 1
		}
	}
}

// AdvanceTo moves the clock to target when target is later than the current
// tick and returns the resulting tick. It never moves the clock backward.
func (d *Deterministic) AdvanceTo(target uint64) uint64 {
	for {
		current := d.tick.Load()
		if target <= current {
			d.fireDueTimers()
			return current
		}
		if d.tick.CompareAndSwap(current, target) {
			d.fireDueTimers()
			return target
		}
	}
}

// NewTimer creates a timer whose deadline is evaluated against the current
// logical timestamp. The timer is delivered when Advance or AdvanceTo moves
// the clock to or beyond that deadline.
func (d *Deterministic) NewTimer(duration time.Duration) Timer {
	if d == nil {
		return nil
	}
	timer := &deterministicTimer{
		clock:    d,
		deadline: d.Now().Add(duration),
		ch:       make(chan time.Time, 1),
		active:   true,
	}
	d.timersMu.Lock()
	if d.timers == nil {
		d.timers = make(map[*deterministicTimer]struct{})
	}
	d.timers[timer] = struct{}{}
	d.timersMu.Unlock()
	d.fireDueTimers()
	return timer
}

func (d *Deterministic) fireDueTimers() {
	if d == nil {
		return
	}
	now := d.Now()
	d.timersMu.Lock()
	due := make([]*deterministicTimer, 0)
	for timer := range d.timers {
		if !now.Before(timer.deadline) {
			delete(d.timers, timer)
			due = append(due, timer)
		}
	}
	d.timersMu.Unlock()
	for _, timer := range due {
		timer.fire(now)
	}
}

func (d *Deterministic) removeTimer(timer *deterministicTimer) {
	if d == nil || timer == nil {
		return
	}
	d.timersMu.Lock()
	delete(d.timers, timer)
	d.timersMu.Unlock()
}

type deterministicTimer struct {
	clock    *Deterministic
	deadline time.Time
	ch       chan time.Time

	mu     sync.Mutex
	active bool
}

func (t *deterministicTimer) C() <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.ch
}

func (t *deterministicTimer) Stop() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	wasActive := t.active
	t.active = false
	t.mu.Unlock()
	if wasActive {
		t.clock.removeTimer(t)
	}
	return wasActive
}

func (t *deterministicTimer) fire(at time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return
	}
	t.active = false
	t.mu.Unlock()
	select {
	case t.ch <- at:
	default:
	}
}

// Ensure returns source unchanged when it is supplied, or a Real source when
// source is nil.
func Ensure(source Source) Source {
	if source == nil {
		return Real{}
	}
	return source
}

// elapsed derives the tick offset without allowing duration multiplication to
// wrap into a timestamp earlier than the base at very large logical ticks.
func elapsed(tick uint64, tickDuration time.Duration) time.Duration {
	if tick == 0 || tickDuration <= 0 {
		return 0
	}

	const maxDuration = time.Duration(1<<63 - 1)
	if tick > uint64(maxDuration)/uint64(tickDuration) {
		return maxDuration
	}
	return time.Duration(tick) * tickDuration
}

var (
	_ Source      = Real{}
	_ Source      = (*Deterministic)(nil)
	_ TimerSource = Real{}
	_ TimerSource = (*Deterministic)(nil)
)
