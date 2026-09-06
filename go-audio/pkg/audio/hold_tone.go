package audio

import (
	"math"
	"time"
)

// HoldToneConfig controls the synthesized "still here" cue played into a
// customer-facing audio gap once it has run longer than an ordinary
// conversational pause. See HoldToneFiller for how the threshold and pulse
// cadence are applied to a live PCM stream.
type HoldToneConfig struct {
	// GapThreshold is how long real assistant audio must be absent before
	// the cue may start. It is set comfortably above an ordinary mid-turn
	// or turn-taking pause, so ordinary conversation never triggers it, but
	// well under the multi-second dead air this cue exists to mask.
	GapThreshold time.Duration
	// PulseInterval is how often a new pulse repeats while the gap
	// continues past GapThreshold.
	PulseInterval time.Duration
	// PulseDuration is the length of one pulse.
	PulseDuration time.Duration
	// Amplitude is the peak int16 sample magnitude at a pulse's loudest
	// point. It is deliberately far below full scale and below ordinary
	// speech level, so the cue reads as a background "still here" signal
	// and is never mistaken for the assistant talking.
	Amplitude int16
	// ToneHz1 and ToneHz2 are mixed to build a soft, fixed-pitch two-tone
	// chime. A stationary two-tone signal has no formant structure,
	// consonant transients, or cadence variation -- unlike speech -- so it
	// cannot be mistaken for the assistant talking even at conversational
	// volume, and it stays trivially distinguishable from voice energy for
	// any downstream classifier (VAD, barge-in, self-hearing detector).
	ToneHz1 float64
	ToneHz2 float64
}

// DefaultHoldToneConfig is the production cue: a soft two-tone chime that
// starts 2.5s after the last real assistant audio (comfortably above a
// natural conversational pause, but well under the multi-second dead air
// measured for both the turn-transition and tool-round-trip defects) and
// repeats every 2s while the gap continues.
func DefaultHoldToneConfig() HoldToneConfig {
	return HoldToneConfig{
		GapThreshold:  2500 * time.Millisecond,
		PulseInterval: 2000 * time.Millisecond,
		PulseDuration: 220 * time.Millisecond,
		Amplitude:     2200,
		ToneHz1:       440,
		ToneHz2:       660,
	}
}

func (c HoldToneConfig) withDefaults() HoldToneConfig {
	d := DefaultHoldToneConfig()
	if c.GapThreshold <= 0 {
		c.GapThreshold = d.GapThreshold
	}
	if c.PulseInterval <= 0 {
		c.PulseInterval = d.PulseInterval
	}
	if c.PulseDuration <= 0 {
		c.PulseDuration = d.PulseDuration
	}
	if c.Amplitude <= 0 {
		c.Amplitude = d.Amplitude
	}
	if c.ToneHz1 <= 0 {
		c.ToneHz1 = d.ToneHz1
	}
	if c.ToneHz2 <= 0 {
		c.ToneHz2 = d.ToneHz2
	}
	return c
}

// holdTonePulse synthesizes one self-contained cue pulse at sampleRate. It
// is enveloped with a raised-cosine (Hann) window so the first and last
// sample are exactly zero: the pulse can be spliced into a PCM stream at any
// point, including immediately before real assistant audio, without an
// amplitude discontinuity ("click") at either edge.
func holdTonePulse(cfg HoldToneConfig, sampleRate int) []int16 {
	cfg = cfg.withDefaults()
	if sampleRate <= 0 {
		sampleRate = SampleRate
	}
	n := int(cfg.PulseDuration.Seconds() * float64(sampleRate))
	if n < 2 {
		n = 2
	}
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(sampleRate)
		window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		signal := math.Sin(2*math.Pi*cfg.ToneHz1*t) + 0.6*math.Sin(2*math.Pi*cfg.ToneHz2*t)
		signal /= 1.6 // normalize the combined two-tone peak back to [-1, 1]
		out[i] = int16(math.Round(window * signal * float64(cfg.Amplitude)))
	}
	return out
}
