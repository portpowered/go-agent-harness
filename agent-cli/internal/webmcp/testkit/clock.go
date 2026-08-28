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

func NewFakeClock(now time.Time) *FakeClock {
	if now.IsZero() {
		now = time.Unix(0, 0).UTC()
	}
	return &FakeClock{now: now, timers: make(map[*FakeTimer]struct{})}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward. Negative durations are ignored because a
// logical clock never moves backwards.
func (c *FakeClock) Advance(delta time.Duration) time.Time {
	c.mu.Lock()
	if delta > 0 {
		c.now = c.now.Add(delta)
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

// Set is an explicit alias for advancing to a timestamp. It never moves the
// logical clock backward.
func (c *FakeClock) Set(target time.Time) time.Time { return c.AdvanceTo(target) }

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
)
