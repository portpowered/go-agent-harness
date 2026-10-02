package probe

import (
	"sync/atomic"
	"time"
)

// ManualPatienceClock is a deterministic, concurrency-safe clock for policy
// tests. Its elapsed duration is independent of host scheduling and sleeps.
type ManualPatienceClock struct {
	base    time.Time
	elapsed atomic.Int64
}

func NewManualPatienceClock(base time.Time) *ManualPatienceClock {
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}
	return &ManualPatienceClock{base: base}
}

func (c *ManualPatienceClock) Now() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.base.Add(time.Duration(c.elapsed.Load()))
}

func (c *ManualPatienceClock) Elapsed() time.Duration {
	if c == nil {
		return 0
	}
	return time.Duration(c.elapsed.Load())
}

func (c *ManualPatienceClock) Advance(duration time.Duration) time.Duration {
	if c == nil || duration <= 0 {
		return c.Elapsed()
	}
	for {
		current := c.elapsed.Load()
		if duration > time.Duration(1<<63-1)-time.Duration(current) {
			if c.elapsed.CompareAndSwap(current, int64(1<<63-1)) {
				return time.Duration(1<<63 - 1)
			}
			continue
		}
		next := current + int64(duration)
		if c.elapsed.CompareAndSwap(current, next) {
			return time.Duration(next)
		}
	}
}
