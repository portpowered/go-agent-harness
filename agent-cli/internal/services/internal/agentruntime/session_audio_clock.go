package agentruntime

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func newSessionTimer(source platformclock.Source, duration time.Duration) (platformclock.Timer, error) {
	return wire.NewService().NewTimer(source, duration)
}

type sessionDurationServiceClock struct {
	source platformclock.Source
}

func (c sessionDurationServiceClock) NewTimer(duration time.Duration) platformclock.Timer {
	timer, err := wire.NewService().NewTimer(c.source, duration)
	if err != nil {
		return nil
	}
	return timer
}

func newSessionDurationClock(source platformclock.Source) (SessionDurationClock, error) {
	if source == nil {
		source = platformclock.Real{}
	}
	probe, err := wire.NewService().NewTimer(source, time.Nanosecond)
	if err != nil {
		return nil, err
	}
	probe.Stop()
	return sessionDurationServiceClock{source: source}, nil
}
