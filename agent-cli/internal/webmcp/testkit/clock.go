package testkit

import (
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// FakeClock is a host-time-free clock. Advancing it synchronously delivers
// every due timer in deadline/creation order; no timer goroutine is created.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	nextID uint64
	timers map[*FakeTimer]struct{}
}

// NewFakeClock accepts either a wall-clock timestamp for browser runtime
// timers or a millisecond integer for recorder/fixture timestamps. The
// flexible input keeps both deterministic seams on one clock implementation.
func NewFakeClock(start any) *FakeClock {
	now := fakeClockStart(start)
	return &FakeClock{now: now, timers: make(map[*FakeTimer]struct{})}
}

func (c *FakeClock) Now() time.Time {
	if c == nil {
		return time.Unix(0, 0).UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// MonotonicMillis adapts the clock to the semantic recorder clock interface.
func (c *FakeClock) MonotonicMillis() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fakeClockMillis(c.now)
}

// Advance moves the clock forward. Negative durations are ignored because a
// logical clock never moves backwards.
func (c *FakeClock) Advance(delta any) time.Time {
	if c == nil {
		return time.Unix(0, 0).UTC()
	}
	duration := fakeClockDelta(delta)
	c.mu.Lock()
	if duration > 0 {
		c.now = c.now.Add(duration)
	}
	c.fireDueLocked()
	now := c.now
	c.mu.Unlock()
	return now
}

// AdvanceTo moves the clock to target when target is later than the current
// time and returns the resulting time.
func (c *FakeClock) AdvanceTo(target time.Time) time.Time {
	c.mu.Lock()
	if target.After(c.now) {
		c.now = target
	}
	c.fireDueLocked()
	now := c.now
	c.mu.Unlock()
	return now
}

// Set moves the clock to an exact timestamp. Unlike AdvanceTo, it may move
// backward so recorder tests can exercise monotonic-clock rejection.
func (c *FakeClock) Set(value any) time.Time {
	if c == nil {
		return time.Unix(0, 0).UTC()
	}
	c.mu.Lock()
	c.now = fakeClockStart(value)
	c.fireDueLocked()
	now := c.now
	c.mu.Unlock()
	return now
}

// NewTimer implements webmcp.TimerFactory. The returned timer has a buffered
// channel and is driven only by Advance/AdvanceTo.
func (c *FakeClock) NewTimer(delta time.Duration) webmcp.Timer {
	return c.NewTimerHandle(delta)
}

func (c *FakeClock) NewTimerHandle(delta time.Duration) *FakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	timer := &FakeTimer{
		clock:    c,
		id:       c.nextID,
		deadline: c.now.Add(delta),
		active:   true,
		channel:  make(chan time.Time, 1),
	}
	c.timers[timer] = struct{}{}
	c.fireDueLocked()
	return timer
}

func (c *FakeClock) fireDueLocked() {
	for {
		var next *FakeTimer
		for timer := range c.timers {
			if !timer.active || timer.deadline.After(c.now) {
				continue
			}
			if next == nil || timer.deadline.Before(next.deadline) ||
				(timer.deadline.Equal(next.deadline) && timer.id < next.id) {
				next = timer
			}
		}
		if next == nil {
			return
		}
		next.active = false
		delete(c.timers, next)
		next.fired = true
		next.channel <- c.now
	}
}

// FakeTimer is a deterministic timer. It is safe to use from the same
// goroutines that use its FakeClock; all state transitions are serialized by
// the clock.
type FakeTimer struct {
	clock    *FakeClock
	id       uint64
	deadline time.Time
	active   bool
	fired    bool
	channel  chan time.Time
}

func (t *FakeTimer) C() <-chan time.Time { return t.channel }

func (t *FakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if !t.active {
		return false
	}
	t.active = false
	delete(t.clock.timers, t)
	return true
}

func (t *FakeTimer) Reset(delta time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	if !wasActive {
		select {
		case <-t.channel:
		default:
		}
	}
	t.clock.nextID++
	t.id = t.clock.nextID
	t.deadline = t.clock.now.Add(delta)
	t.active = true
	t.fired = false
	t.clock.timers[t] = struct{}{}
	t.clock.fireDueLocked()
	return wasActive
}

var (
	_ webmcp.Clock        = (*FakeClock)(nil)
	_ webmcp.TimerFactory = (*FakeClock)(nil)
	_ webmcp.Timer        = (*FakeTimer)(nil)
	_ Clock               = (*FakeClock)(nil)
)

func fakeClockStart(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		if !typed.IsZero() {
			return typed
		}
	case uint64:
		return time.Unix(0, millisecondsToNanos(typed)).UTC()
	case uint:
		return time.Unix(0, millisecondsToNanos(uint64(typed))).UTC()
	case uint32:
		return time.Unix(0, millisecondsToNanos(uint64(typed))).UTC()
	case int:
		if typed > 0 {
			return time.Unix(0, millisecondsToNanos(uint64(typed))).UTC()
		}
	case int64:
		if typed > 0 {
			return time.Unix(0, typed*int64(time.Millisecond)).UTC()
		}
	case int32:
		if typed > 0 {
			return time.Unix(0, int64(typed)*int64(time.Millisecond)).UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

func fakeClockDelta(value any) time.Duration {
	switch typed := value.(type) {
	case time.Duration:
		return typed
	case uint64:
		return time.Duration(millisecondsToNanos(typed))
	case uint:
		return time.Duration(millisecondsToNanos(uint64(typed)))
	case uint32:
		return time.Duration(millisecondsToNanos(uint64(typed)))
	case int:
		if typed > 0 {
			return time.Duration(typed) * time.Millisecond
		}
	case int64:
		if typed > 0 {
			return time.Duration(typed) * time.Millisecond
		}
	case int32:
		if typed > 0 {
			return time.Duration(typed) * time.Millisecond
		}
	}
	return 0
}

func millisecondsToNanos(value uint64) int64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	const nanosPerMillisecond = uint64(time.Millisecond)
	if value > maxInt64/nanosPerMillisecond {
		return int64(maxInt64)
	}
	return int64(value * nanosPerMillisecond)
}

func fakeClockMillis(now time.Time) uint64 {
	seconds := now.Unix()
	if seconds <= 0 {
		if seconds == 0 && now.Nanosecond() > 0 {
			return uint64(now.Nanosecond()) / uint64(time.Millisecond)
		}
		return 0
	}
	return uint64(seconds)*1000 + uint64(now.Nanosecond())/uint64(time.Millisecond)
}
