package webmcp

import (
	"time"
)

// wallTimerFactory is the production fallback when the configured clock does
// not also provide deterministic timers.
type wallTimerFactory struct{}

func (wallTimerFactory) NewTimer(duration time.Duration) Timer {
	return wallTimer{timer: time.NewTimer(duration)}
}

type wallTimer struct {
	timer *time.Timer
}

func (t wallTimer) C() <-chan time.Time { return t.timer.C }

func (t wallTimer) Stop() bool { return t.timer.Stop() }

func (t wallTimer) Reset(duration time.Duration) bool { return t.timer.Reset(duration) }

var _ TimerFactory = wallTimerFactory{}
var _ Timer = wallTimer{}
