package audio

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// holdToneFadeOutSamples bounds the linear ramp used to abandon an
// in-progress pulse cleanly. It is short enough (a few milliseconds at any
// production sample rate) to be inaudible as a discrete step, but long
// enough to eliminate the amplitude discontinuity a hard stop would leave at
// the exact boundary where real audio takes over.
const holdToneFadeOutSamples = 64

// HoldToneFiller decides, from an explicit timeline of real-audio
// observations, when a hold-tone pulse is due. It performs no I/O and owns
// no goroutine or timer: callers drive it by calling ObserveRealAudio
// whenever genuine assistant PCM is about to reach the customer, and
// NextFrame whenever they are about to write (or skip) a PCM chunk of their
// own cadence. That keeps it deterministically testable and reusable across
// delivery paths with very different real-time cadences -- a room mixer that
// always ticks and emits an explicit all-zero frame during silence, versus a
// raw provider media reader that simply blocks indefinitely between deltas.
type HoldToneFiller struct {
	cfg        HoldToneConfig
	sampleRate int
	pulse      []int16

	lastReal    time.Time
	nextPulseAt time.Time
	// cursor is -1 when no pulse is in progress, or the index of the next
	// unemitted pulse sample while one is.
	cursor int
}

// NewHoldToneFiller starts the gap clock at start (typically the moment the
// caller begins pumping this delivery path), so a slow first response is
// covered by the same threshold as any later turn-transition gap.
func NewHoldToneFiller(cfg HoldToneConfig, sampleRate int, start time.Time) *HoldToneFiller {
	cfg = cfg.withDefaults()
	if sampleRate <= 0 {
		sampleRate = SampleRate
	}
	return &HoldToneFiller{
		cfg:         cfg,
		sampleRate:  sampleRate,
		pulse:       holdTonePulse(cfg, sampleRate),
		lastReal:    start,
		nextPulseAt: start.Add(cfg.GapThreshold),
		cursor:      -1,
	}
}

// ObserveRealAudio must be called whenever genuine (non-filler) assistant
// PCM is about to reach the customer. It resets the gap clock so the cue
// cannot fire again until another full GapThreshold of silence has passed,
// and it stops any in-progress pulse immediately.
//
// If a pulse was mid-flight, it returns a short linear fade-out from the
// last emitted sample down to zero (see holdToneFadeOutSamples). The caller
// must write this immediately before the real audio it just observed, and
// nothing else in between, so the transition never contains an amplitude
// discontinuity ("click") and the cue never overlaps real audio. When no
// pulse was in progress -- the common case, since most gaps never reach
// GapThreshold at all -- it returns nil and there is nothing to flush.
func (f *HoldToneFiller) ObserveRealAudio(now time.Time) []int16 {
	var tail []int16
	if f.cursor > 0 && f.cursor <= len(f.pulse) {
		tail = holdToneFadeOutFrom(f.pulse[f.cursor-1])
	}
	f.cursor = -1
	f.lastReal = now
	f.nextPulseAt = now.Add(f.cfg.GapThreshold)
	return tail
}

// NextFrame returns up to n samples of hold-tone content due at now, or nil
// if the cue should stay silent: either the gap since the last real audio
// has not yet reached GapThreshold (an ordinary conversational pause that
// must never be filled), or a pulse just completed and the next one is not
// due until PulseInterval has elapsed. The returned samples are always a
// contiguous slice of the enveloped pulse, so the stream is at zero
// amplitude both immediately before a pulse starts and immediately after it
// ends.
func (f *HoldToneFiller) NextFrame(now time.Time, n int) []int16 {
	if n <= 0 {
		return nil
	}
	if f.cursor < 0 {
		if now.Before(f.nextPulseAt) {
			return nil
		}
		f.cursor = 0
	}
	end := f.cursor + n
	if end > len(f.pulse) {
		end = len(f.pulse)
	}
	frame := append([]int16(nil), f.pulse[f.cursor:end]...)
	f.cursor = end
	if f.cursor >= len(f.pulse) {
		f.cursor = -1
		f.nextPulseAt = now.Add(f.cfg.PulseInterval)
	}
	return frame
}

// PCM16HasSignal reports whether a little-endian PCM16 payload contains any
// non-zero sample byte. It is intentionally byte-oriented: checking both
// bytes also handles a non-zero high byte without decoding or allocating.
func PCM16HasSignal(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}

// ApplyHoldTonePCM16 applies one hold-tone decision to a cadence-sized raw
// PCM16 frame. A non-silent frame is returned intact after the filler observes
// real audio; if a pulse was in flight, its short fade-out is prefixed so the
// transition remains click-free. Silent frames remain unchanged until the
// configured gap threshold, then receive the next pulse samples. The input
// frame is never retained.
func ApplyHoldTonePCM16(filler *HoldToneFiller, now time.Time, frame []byte) []byte {
	if filler == nil {
		return frame
	}
	if PCM16HasSignal(frame) {
		if tail := filler.ObserveRealAudio(now); len(tail) > 0 {
			return append(codec.EncodePCM16(tail), frame...)
		}
		return frame
	}
	sampleCount := len(frame) / 2
	if sampleCount <= 0 {
		return frame
	}
	fill := filler.NextFrame(now, sampleCount)
	if len(fill) == 0 {
		return frame
	}
	return codec.EncodePCM16(fill)
}

func holdToneFadeOutFrom(last int16) []int16 {
	out := make([]int16, holdToneFadeOutSamples)
	for i := range out {
		factor := 1 - float64(i+1)/float64(holdToneFadeOutSamples)
		out[i] = int16(float64(last) * factor)
	}
	return out
}
