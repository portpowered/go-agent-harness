package service

import (
	"sync"
	"time"

	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	evidenceDirectoryMode = 0o700
	evidenceFileMode      = 0o600
	maxJSONLRecordBytes   = 1 << 20
	maxAudioFrameBytes    = 2 << 20
	defaultSampleRate     = 24000
	defaultChannels       = 1
	defaultFrameDuration  = 20 * time.Millisecond
)

type clockState struct {
	start  time.Time
	source platformclock.Source
}

func newClockState(start time.Time, source platformclock.Source) clockState {
	source = platformclock.Ensure(source)
	if start.IsZero() {
		start = source.Now()
	}
	return clockState{start: start.UTC(), source: source}
}

func (c clockState) now() (time.Duration, int64) {
	current := platformclock.Ensure(c.source).Now().UTC()
	return current.Sub(c.start), current.UnixMilli()
}

func offsetMillis(offset time.Duration) float64 { return float64(offset) / float64(time.Millisecond) }

type speechTracker struct {
	mu     sync.Mutex
	active bool
}

func (t *speechTracker) transition(signal bool) string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if signal && !t.active {
		t.active = true
		return "start"
	}
	if !signal && t.active {
		t.active = false
		return "end"
	}
	return ""
}
