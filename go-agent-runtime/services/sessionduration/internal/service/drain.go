package service

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	defaultDrainQuietPeriod = 25 * time.Millisecond
	defaultDrainWallSafety  = 250 * time.Millisecond
)

func (c *controller) drainLoop(ctx context.Context, loop sessionduration.Loop, policy sessionduration.DrainPolicy) error { //nolint:contextcheck // drain uses a detached fallback after runner cancellation.
	if c == nil || loop == nil || loop.Deltas() == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.drainAvailable(loop); err != nil {
		return err
	}
	if policy.Clock == nil {
		return nil
	}
	quietPeriod, wallSafety := drainDurations(policy)
	timer, err := newDrainTimer(policy.Clock, quietPeriod)
	if err != nil {
		return err
	}
	return c.drainUntilQuiet(ctx, loop, policy.Clock, timer, quietPeriod, wallSafety, policy.Pending)
}

func drainDurations(policy sessionduration.DrainPolicy) (quietPeriod, wallSafety time.Duration) {
	quietPeriod = policy.QuietPeriod
	if quietPeriod <= 0 {
		quietPeriod = defaultDrainQuietPeriod
	}
	wallSafety = policy.WallSafety
	if wallSafety <= 0 {
		wallSafety = defaultDrainWallSafety
	}
	return quietPeriod, wallSafety
}

func newDrainTimer(clock sessionduration.TimerScheduler, duration time.Duration) (sessionduration.Timer, error) {
	timer := clock.NewTimer(duration)
	if timer == nil {
		return nil, errors.New("session duration clock returned a nil drain timer")
	}
	return timer, nil
}

func (c *controller) drainUntilQuiet(ctx context.Context, loop sessionduration.Loop, clock sessionduration.TimerScheduler, timer sessionduration.Timer, quietPeriod, wallSafety time.Duration, pending func() bool) error {
	defer func() { timer.Stop() }()
	wallTimer := time.NewTimer(wallSafety)
	defer wallTimer.Stop()
	for {
		done, err := c.awaitDrainActivity(ctx, loop, clock, &timer, wallTimer.C, quietPeriod, pending)
		if done || err != nil {
			return err
		}
	}
}

func (c *controller) awaitDrainActivity(ctx context.Context, loop sessionduration.Loop, clock sessionduration.TimerScheduler, timer *sessionduration.Timer, wallDone <-chan time.Time, quietPeriod time.Duration, pending func() bool) (bool, error) {
	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-(*timer).C():
		return c.finishDrainQuietPeriod(loop, clock, timer, quietPeriod, pending)
	case <-wallDone:
		return true, nil
	case msg, ok := <-loop.Deltas().Chan():
		if !ok {
			return true, nil
		}
		if err := c.admitAndPublishDrain(msg); err != nil {
			return true, err
		}
		return false, extendDrainQuietPeriod(clock, timer, quietPeriod)
	}
}

func (c *controller) finishDrainQuietPeriod(loop sessionduration.Loop, clock sessionduration.TimerScheduler, timer *sessionduration.Timer, quietPeriod time.Duration, pending func() bool) (bool, error) {
	if drainPending(pending) {
		return false, extendDrainQuietPeriod(clock, timer, quietPeriod)
	}
	if err := c.drainAvailable(loop); err != nil {
		return true, err
	}
	if drainPending(pending) {
		return false, extendDrainQuietPeriod(clock, timer, quietPeriod)
	}
	return true, nil
}

func extendDrainQuietPeriod(clock sessionduration.TimerScheduler, timer *sessionduration.Timer, quietPeriod time.Duration) error {
	next, err := resetDrainTimer(clock, *timer, quietPeriod)
	if err == nil {
		*timer = next
	}
	return err
}

func drainPending(pending func() bool) bool { return pending != nil && pending() }

func (c *controller) drainAvailable(loop sessionduration.Loop) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if err := c.admitAndPublishDrain(msg); err != nil {
			return err
		}
	}
}

func (c *controller) admitAndPublishDrain(msg messages.StreamMessage) error {
	admission := c.ObserveDrain(msg)
	if !admission.Accepted {
		return nil
	}
	return publish(c.options.Publication, admission.Message)
}

func resetDrainTimer(clock sessionduration.TimerScheduler, timer sessionduration.Timer, duration time.Duration) (sessionduration.Timer, error) {
	if timer != nil && !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	next := clock.NewTimer(duration)
	if next == nil {
		return nil, errors.New("session duration clock returned a nil drain timer")
	}
	return next, nil
}
