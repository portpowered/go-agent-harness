package fileports

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// pacingScheduler returns the scheduler file inputs pace on. Real time and
// unpaced selections keep the host scheduler (unpaced inputs never wait on
// it); a speed multiplier wraps it so paced waits shrink by that factor while
// remaining on the host's time domain, which keeps deterministic schedulers
// deterministic.
func pacingScheduler(base clock.Scheduler, pacing devices.FilePacing) clock.Scheduler {
	if base == nil || pacing.Unpaced || pacing.Realtime() {
		return base
	}
	return &acceleratedScheduler{base: base, origin: base.Now(), speed: pacing.Speed}
}

// acceleratedScheduler is a view of a base scheduler whose time runs speed
// times faster from origin. Durations and deadlines are converted to the base
// domain before the base scheduler waits on them. Timer channels deliver the
// base scheduler's fire time; pacing only uses them as signals.
type acceleratedScheduler struct {
	base   clock.Scheduler
	origin time.Time
	speed  float64
}

var _ clock.Scheduler = (*acceleratedScheduler)(nil)

func (s *acceleratedScheduler) Now() time.Time {
	return s.origin.Add(time.Duration(float64(s.base.Now().Sub(s.origin)) * s.speed))
}

func (s *acceleratedScheduler) NewTimer(duration time.Duration) clock.Timer {
	return s.base.NewTimer(s.baseDuration(duration))
}

func (s *acceleratedScheduler) Wait(ctx context.Context, duration time.Duration) error {
	return s.base.Wait(ctx, s.baseDuration(duration))
}

func (s *acceleratedScheduler) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return s.base.WithTimeout(parent, s.baseDuration(timeout))
}

func (s *acceleratedScheduler) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return s.base.WithDeadline(parent, s.origin.Add(s.baseDuration(deadline.Sub(s.origin))))
}

// baseDuration converts an accelerated duration to the base domain. A
// positive duration never rounds down to an immediate fire.
func (s *acceleratedScheduler) baseDuration(duration time.Duration) time.Duration {
	if duration <= 0 {
		return duration
	}
	scaled := time.Duration(float64(duration) / s.speed)
	if scaled <= 0 {
		return 1
	}
	return scaled
}
