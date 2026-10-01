package sessionstate

import (
	"math"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Local barge-in onset detection.
//
// A frame is speech when its RMS clears BargeInConfig.SpeechLevel and, unless
// the capture path is echo cancelled, also clears the audible playback level
// by EchoMargin -- on a device without echo cancellation the microphone hears
// the playback at roughly its own level, and that echo must never interrupt
// the agent. The margin is measured against the audio sent to the speaker,
// not the echo at the microphone, so it is conservative; a path that removes
// the playback from the capture (a feedback gate or AEC, reported as
// EchoCancelled) skips it.
//
// Onset needs MinSpeech of such audio (40 ms: two 20 ms frames) and a quiet
// gap shorter than Hangover does not reset it; both are durations at the
// session's input sample rate.

// BargeInConfig tunes local barge-in detection.
type BargeInConfig struct {
	// SpeechLevel is the minimum RMS, in PCM16 units, of a speech frame.
	SpeechLevel float64
	// EchoMargin is the factor by which speech must exceed the audible
	// playback level on a path without echo cancellation (2 = 6 dB).
	EchoMargin float64
	// MinSpeech is the speech needed for onset.
	MinSpeech time.Duration
	// Hangover is the quiet gap that resets a partial onset.
	Hangover time.Duration
}

// DefaultBargeInConfig returns the default detector tuning.
func DefaultBargeInConfig() BargeInConfig {
	return BargeInConfig{SpeechLevel: defaultSpeechLevel, EchoMargin: defaultEchoMargin, MinSpeech: defaultMinSpeech, Hangover: defaultHangover}
}

const (
	defaultSpeechLevel = 300 // RMS separating mic noise from voiced speech (matches the energy VAD)
	defaultEchoMargin  = 2   // 6 dB
	defaultMinSpeech   = 40 * time.Millisecond
	defaultHangover    = 300 * time.Millisecond
	// defaultSampleRate is assumed when the session does not report its input
	// rate (the OpenAI Realtime PCM16 default).
	defaultSampleRate = 24000
)

// Detector accumulates speech toward an onset.
type Detector struct {
	speech time.Duration
	quiet  time.Duration
}

// Observe reports whether pcm completes a speech onset that should barge in,
// and whether it is itself speech-level.
func (d *Detector) Observe(pcm []byte, playback messages.LocalPlaybackState, rate int, config BargeInConfig) (onset, loud bool) {
	level, samples := PCM16Level(pcm)
	if samples == 0 {
		return false, false
	}
	if rate <= 0 {
		rate = defaultSampleRate
	}
	duration := time.Duration(samples) * time.Second / time.Duration(rate)
	threshold := config.SpeechLevel
	if !playback.EchoCancelled {
		// Level includes the acoustic tail of audio that just finished playing.
		threshold = math.Max(threshold, config.EchoMargin*playback.Level)
	}
	if level < threshold {
		d.quiet += duration
		if d.quiet > config.Hangover {
			d.speech = 0
		}
		return false, false
	}
	d.quiet = 0
	d.speech += duration
	return d.speech >= config.MinSpeech, true
}

// PCM16Level returns the RMS of little-endian PCM16 audio and its sample count.
func PCM16Level(pcm []byte) (float64, int) {
	samples := len(pcm) / 2
	if samples == 0 {
		return 0, 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		value := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		sum += value * value
	}
	return math.Sqrt(sum / float64(samples)), samples
}

// HeldOnset holds interrupting frames while barge-in onset is undecided, so
// one loud transient (a cough, a door) does not cancel the response.
type HeldOnset struct {
	Detector Detector
	Frames   [][]byte
	// timer releases Frames once onset can no longer be reached.
	timer clock.Timer
}

// Hold holds pcm, arming the release timer on the first held frame.
func (h *HeldOnset) Hold(pcm []byte, source clock.TimerSource, window time.Duration) {
	if len(h.Frames) == 0 {
		h.timer = source.NewTimer(window)
	}
	h.Frames = append(h.Frames, pcm)
}

// Take returns and clears the held frames and disarms the timer.
func (h *HeldOnset) Take() [][]byte {
	frames := h.Frames
	h.Frames = nil
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	return frames
}

// Expiry fires once held onset audio can no longer reach onset: the onset
// window elapsed without the speech that would complete it. Frames arrive in
// real time, so this bounds how long sparse input (a relay that sends no
// silence) keeps a frame held. It is nil while nothing is held.
func (h *HeldOnset) Expiry() <-chan time.Time {
	if h.timer == nil {
		return nil
	}
	return h.timer.C()
}
