package audio

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// PlaybackProcessor owns DSP state between response and interruption boundaries.
// A blocked generation discards its pending tail; a normally completed response
// flushes its exact tail. Its owner serializes calls on the media worker.
type PlaybackProcessor struct {
	processor  *Processor
	generation uint64
	blocked    bool
	last       PCMFrame
	started    bool
}

func NewPlaybackProcessor(input, output DeviceFormat, quantum int) (*PlaybackProcessor, error) {
	p, err := NewProcessor(input, output, quantum)
	if err != nil {
		return nil, err
	}
	return &PlaybackProcessor{processor: p}, nil
}

func (p *PlaybackProcessor) Process(frame PCMFrame, generation uint64, blocked bool) ([]PCMFrame, error) {
	if generation != p.generation || blocked || p.blocked || p.processor.ended {
		if _, err := p.processor.Reset(); err != nil {
			return nil, err
		}
		p.started = false
	}
	p.generation, p.blocked = generation, blocked
	if blocked {
		return nil, nil
	}
	frame.Epoch = generation
	p.last, p.started = frame, true
	return p.processor.Process(frame)
}

func (p *PlaybackProcessor) Flush(generation uint64, blocked bool) ([]PCMFrame, error) {
	if blocked || p.blocked || generation != p.generation {
		_, err := p.processor.Reset()
		p.generation, p.blocked, p.started = generation, blocked, false
		return nil, err
	}
	if !p.started || p.processor.ended {
		return nil, nil
	}
	frame := p.last
	frame.Samples, frame.EndOfResponse = nil, true
	return p.processor.Process(frame)
}

// PlaybackActivity describes provider audio that is queued for, or audible on,
// the local playback device.
type PlaybackActivity struct {
	// Active reports that audio is still queued or has not finished playing.
	Active bool
	// Level is the RMS, in PCM16 units, of the audio audible now.
	Level float64
}

// playbackEchoWindow is how long a rendered frame still reaches the
// microphone: device and acoustic latency plus a short room tail.
const playbackEchoWindow = 400 * time.Millisecond

// playbackActivity follows dequeued response audio on a real-time playback
// cursor. Consumers between the response queue and the speaker read ahead,
// but the device renders the audio at its sample rate, so dequeued audio is
// scheduled back to back from the later of now and the previous frame's end.
type playbackActivity struct {
	clock  clock.Source
	end    time.Time
	frames []scheduledPlayback
}

type scheduledPlayback struct {
	start, end time.Time
	level      float64
}

func (a *playbackActivity) now() time.Time {
	if a.clock == nil {
		a.clock = clock.Real{}
	}
	return a.clock.Now()
}

func (a *playbackActivity) dequeued(samples []int16, rate int) {
	if len(samples) == 0 || rate <= 0 {
		return
	}
	now := a.now()
	start := a.end
	if start.Before(now) {
		start = now
	}
	a.end = start.Add(pcm16DeviceDurationAtRate(len(samples), rate))
	a.frames = append(a.frames, scheduledPlayback{start: start, end: a.end, level: PCM16RMSEnergy(samples)})
	a.prune(now)
}

// prune forgets frames that can no longer be heard.
func (a *playbackActivity) prune(now time.Time) {
	horizon := now.Add(-playbackEchoWindow)
	kept := 0
	for kept < len(a.frames) && a.frames[kept].end.Before(horizon) {
		kept++
	}
	a.frames = append(a.frames[:0], a.frames[kept:]...)
}

func (a *playbackActivity) state(queued bool) PlaybackActivity {
	now := a.now()
	a.prune(now)
	activity := PlaybackActivity{Active: queued || now.Before(a.end)}
	for _, frame := range a.frames {
		if frame.start.After(now) {
			break
		}
		activity.Level = max(activity.Level, frame.level)
	}
	return activity
}

// interrupted silences playback: nothing dequeued before it will be heard.
func (a *playbackActivity) interrupted() {
	a.end = time.Time{}
	a.frames = a.frames[:0]
}
