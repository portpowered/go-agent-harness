package agentruntime

import (
	"fmt"
	"time"

	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// sessionTimerSource resolves the timer domain for session-owned scheduling.
// A nil source retains the live compatibility default of the host clock. An
// explicitly supplied source must implement timers; this prevents a custom or
// deterministic timestamp source from silently scheduling against host time.
func sessionTimerSource(source platformclock.Source) (platformclock.TimerSource, error) {
	if source == nil {
		return platformclock.Real{}, nil
	}
	timerSource, err := platformclock.RequireTimerSource(source)
	if err != nil {
		return nil, err
	}
	return timerSource, nil
}

func newSessionTimer(source platformclock.Source, duration time.Duration) (platformclock.Timer, error) {
	timerSource, err := sessionTimerSource(source)
	if err != nil {
		return nil, err
	}
	timer := timerSource.NewTimer(duration)
	if timer == nil {
		return nil, fmt.Errorf("clock: timer source returned nil timer")
	}
	return timer, nil
}
