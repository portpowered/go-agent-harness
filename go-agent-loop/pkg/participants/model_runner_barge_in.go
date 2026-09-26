package participants

import (
	"context"
	"math"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Local barge-in detection.
//
// The runner detects user speech from input energy: a frame is speech when its
// RMS clears BargeInConfig.SpeechLevel, and, unless the capture path is echo
// cancelled, also clears the audible playback level by EchoMargin -- on a
// device without echo cancellation the microphone hears the playback at
// roughly its own level, and that echo must never interrupt the agent. The
// margin is measured against the audio sent to the speaker, not the echo at
// the microphone, so it is conservative; a path that removes the playback from
// the capture (a feedback gate or AEC, reported as EchoCancelled) skips it.
//
// Onset needs MinSpeech of such audio and a quiet gap shorter than Hangover
// does not reset it. Both are durations at the session's input sample rate.
// The default onset is 10 ms -- one ordinary capture frame -- because the live
// barge-in contract requires the cancel to precede the interrupting audio at
// the provider; a longer onset would hold that audio back or let it through
// first. Transient clicks are filtered by the level threshold instead.
//
// Speech cancels the active response before the frame reaches the provider.
// When the provider runs turn detection (server VAD) its speech_started stops
// local playback and truncates the heard item, so the runner's cancel only
// stops generation (KeepPlayback). Otherwise the runner owns playback: its
// cancel stops local playback, and when no response is active but its audio
// is still playing (the provider delivers faster than real time, so
// response.done arrives while seconds remain audible) speech interrupts local
// playback directly.

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
	return BargeInConfig{SpeechLevel: defaultBargeInSpeechLevel, EchoMargin: defaultBargeInEchoMargin, MinSpeech: defaultBargeInMinSpeech, Hangover: defaultBargeInHangover}
}

const (
	defaultBargeInSpeechLevel = 300 // RMS separating mic noise from voiced speech (matches the energy VAD)
	defaultBargeInEchoMargin  = 2   // 6 dB
	defaultBargeInMinSpeech   = 10 * time.Millisecond
	defaultBargeInHangover    = 300 * time.Millisecond
)

// defaultBargeInSampleRate is assumed when the session does not report its
// input rate (the OpenAI Realtime PCM16 default).
const defaultBargeInSampleRate = 24000

// SetBargeInConfig replaces the local barge-in tuning. Call it before Run.
func (r *ModelRunner) SetBargeInConfig(config BargeInConfig) { r.bargeInConfig = &config }

func (r *ModelRunner) bargeInTuning() BargeInConfig {
	if r.bargeInConfig != nil {
		return *r.bargeInConfig
	}
	return DefaultBargeInConfig()
}

type bargeInDetector struct {
	speech time.Duration
	quiet  time.Duration
}

// observe reports whether pcm is user speech that should barge in.
func (d *bargeInDetector) observe(pcm []byte, playback messages.LocalPlaybackState, rate int, config BargeInConfig) bool {
	level, samples := pcm16LevelFromBytes(pcm)
	if samples == 0 {
		return false
	}
	if rate <= 0 {
		rate = defaultBargeInSampleRate
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
		return false
	}
	d.quiet = 0
	d.speech += duration
	return d.speech >= config.MinSpeech
}

// pcm16LevelFromBytes returns the RMS of little-endian PCM16 audio.
func pcm16LevelFromBytes(pcm []byte) (float64, int) {
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

func providerOwnsTurnDetection(session messages.Session) bool {
	detector, ok := session.(messages.SessionTurnDetection)
	return ok && detector.ProviderTurnDetection()
}

func inputSampleRate(session messages.Session) int {
	if format, ok := session.(messages.SessionInputFormat); ok {
		return format.InputAudioSampleRate()
	}
	return 0
}

func localPlayback(session messages.Session) (messages.SessionLocalPlayback, messages.LocalPlaybackState) {
	playback, ok := session.(messages.SessionLocalPlayback)
	if !ok {
		return nil, messages.LocalPlaybackState{}
	}
	return playback, playback.LocalPlayback()
}

// bargeIn applies local barge-in for one interrupting user audio frame.
//
// A response that is still non-terminal is a barge-in target, including the
// interval between response creation and its first output delta. The response
// that is itself the requested continuation of an already accepted tool
// result (state.continuationInFlight) is deliberately excluded: nothing
// re-requests a cancelled tool continuation, so its obligation would be left
// permanently unresolved (a room participant died exactly so, 557 ms into its
// continuation, having produced no audio). A response that was already playing
// when the continuation was queued is not the continuation -- the request is
// deferred until that response ends -- so it stays interruptible.
func (r *ModelRunner) bargeIn(ctx context.Context, session messages.Session, pcm []byte, state *sessionResponseState) error {
	providerVAD := providerOwnsTurnDetection(session)
	playback, playing := localPlayback(session)
	if !state.bargeIn.observe(pcm, playing, inputSampleRate(session), r.bargeInTuning()) {
		return nil
	}
	responseActive := state.responseInFlight || state.acknowledgementOutstanding
	if responseActive && !state.continuationInFlight && !state.responseCancelSent {
		return r.sendBargeInCancel(ctx, session, state, providerVAD)
	}
	if !providerVAD && !responseActive && playing.Active {
		playback.InterruptLocalPlayback(ctx)
	}
	return nil
}

func (r *ModelRunner) sendBargeInCancel(ctx context.Context, session messages.Session, state *sessionResponseState, keepPlayback bool) error {
	value := messages.NewResponseCancelValue()
	value.KeepPlayback = keepPlayback
	cancelOutcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: value,
	})
	if !cancelOutcome.OK() {
		return sessionAudioSendError("response cancel", cancelOutcome)
	}
	// Keep the response in flight until its terminal MESSAGE.END arrives,
	// but never send a second cancel for more audio belonging to the same
	// response.
	state.responseCancelSent = true
	if state.acknowledgementOutstanding {
		state.acknowledgementCancelled = true
	}
	state.cancelledResponseIDs.add(state.currentResponseID)
	return nil
}
