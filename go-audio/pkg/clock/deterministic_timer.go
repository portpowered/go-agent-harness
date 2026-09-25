package clock

import (
	"container/heap"
	"context"
	"time"
)

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
		t.clock.notifyTimersChangedLocked()
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

// WaitForTimers blocks until at least n timers are pending (created and
// neither fired nor stopped) or ctx is done. It lets a test advance virtual
// time only after the code under test has armed its timer, without spinning
// on the scheduler.
func (d *Deterministic) WaitForTimers(ctx context.Context, n int) error {
	return d.waitForPendingTimers(ctx, func(pending int) bool { return pending >= n })
}

func (d *Deterministic) waitForPendingTimers(ctx context.Context, ready func(pending int) bool) error {
	if d == nil {
		return ErrTimerSourceUnavailable
	}
	var done <-chan struct{} // a nil ctx never cancels, like context.Background
	if ctx != nil {
		done = ctx.Done()
	}
	for {
		d.advanceMu.Lock()
		if ready(d.timers.Len()) {
			d.advanceMu.Unlock()
			return nil
		}
		if d.timersChanged == nil {
			d.timersChanged = make(chan struct{})
		}
		changed := d.timersChanged
		d.advanceMu.Unlock()
		select {
		case <-done:
			return contextCause(ctx)
		case <-changed:
		}
	}
}

func (d *Deterministic) notifyTimersChangedLocked() {
	if d.timersChanged != nil {
		close(d.timersChanged)
		d.timersChanged = nil
	}
}
