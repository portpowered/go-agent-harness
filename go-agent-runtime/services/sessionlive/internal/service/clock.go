package service

import (
	"context"
	"errors"
	"time"

	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func newDeadline(source platformclock.Source, duration time.Duration) (<-chan time.Time, func(), error) {
	if duration <= 0 {
		return nil, func() {}, nil
	}
	timer, err := newTimer(source, duration)
	if err != nil {
		return nil, nil, err
	}
	return timer.C(), func() { timer.Stop() }, nil
}

func newTimer(source platformclock.Source, duration time.Duration) (platformclock.Timer, error) {
	if source == nil {
		source = platformclock.Real{}
	}
	timerSource, err := platformclock.RequireTimerSource(source)
	if err != nil {
		return nil, err
	}
	timer := timerSource.NewTimer(duration)
	if timer == nil {
		return nil, errors.New("session live clock returned a nil timer")
	}
	return timer, nil
}

func waitForFirstTurn(ctx context.Context, ack <-chan error, source platformclock.Source, timeout time.Duration) error {
	timer, err := newTimer(source, effectiveTimeout(timeout))
	if err != nil {
		return err
	}
	defer timer.Stop()
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return errors.New("timed out awaiting session first user turn acceptance")
	}
}

func effectiveTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultFirstTurnTimeout
	}
	return timeout
}
