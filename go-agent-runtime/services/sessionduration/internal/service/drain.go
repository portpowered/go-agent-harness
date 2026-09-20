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
	return c.drainUntilQuiet(ctx, loop.Deltas(), policy.Clock, timer, quietPeriod, wallSafety)
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

func (c *controller) drainUntilQuiet(ctx context.Context, deltas *messages.TypedBuffer[messages.StreamMessage], clock sessionduration.TimerScheduler, timer sessionduration.Timer, quietPeriod, wallSafety time.Duration) error {
	defer timer.Stop()
	wallTimer := time.NewTimer(wallSafety)
	defer wallTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C():
			return nil
		case <-wallTimer.C:
			return nil
		case msg, ok := <-deltas.Chan():
			if !ok {
				return nil
			}
			if err := c.admitAndPublishDrain(msg); err != nil {
				return err
			}
			next, err := resetDrainTimer(clock, timer, quietPeriod)
			if err != nil {
				return err
			}
			timer = next
		}
	}
}

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
