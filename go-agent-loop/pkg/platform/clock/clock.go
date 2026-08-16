// Package clock provides host-time and logical-tick time sources.
package clock

import (
	"sync/atomic"
	"time"
)

// Source supplies the current time to code that should not depend directly on
// the host clock.
type Source interface {
	Now() time.Time
}

// Real reads the host wall clock.
type Real struct{}

// Now returns the current host time.
func (Real) Now() time.Time {
	return time.Now()
}

// Deterministic maps a logical tick to a stable timestamp. Its configuration
// is immutable after construction and its current tick is safe to share
// between goroutines.
type Deterministic struct {
	base         time.Time
	tickDuration time.Duration
	tick         atomic.Uint64
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
			return current
		}
		if d.tick.CompareAndSwap(current, current+1) {
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
			return current
		}
		if d.tick.CompareAndSwap(current, target) {
			return target
		}
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
	_ Source = Real{}
	_ Source = (*Deterministic)(nil)
)
