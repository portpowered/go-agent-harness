package participants

import (
	"context"
	"math"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// Local barge-in detection.
//
// The runner detects user speech from input energy: a frame is speech when its
// RMS clears the shared energy VAD threshold, and onset needs at least
// bargeInMinSpeechSamples of such audio; a quiet gap shorter than the
// bargeInHangoverSamples hangover does not reset it. Speech cancels the active
// response before the frame reaches the provider -- the barge-in contract the
// live customer simulations verify.
//
// When the provider runs its own turn detection (server VAD) it stops local
// playback on speech_started and truncates the heard item, so the runner's
// cancel then only stops generation (KeepPlayback). Echo protection and local
// playback interruption are the provider's there.
//
// Without provider turn detection (client-owned turns, scheduled audio) nothing
// else reacts to speech, so the runner also owns playback: the frame must also
// clear the audible playback level by bargeInEchoMargin -- on a device without
// echo cancellation the microphone hears the playback at roughly its own
// level, and that echo must never interrupt the agent -- and a barge-in stops
// local playback. When no response is active but its audio is still playing
// (the provider delivers faster than real time, so response.done arrives while
// seconds remain audible), speech interrupts local playback directly.
const (
	bargeInEchoMargin       = 2.0            // 6 dB above the audible playback level
	bargeInMinSpeechSamples = 160            // 10 ms at 16 kHz
	bargeInHangoverSamples  = 16000 * 3 / 10 // 300 ms at 16 kHz
)

type bargeInDetector struct {
	speechSamples int
	quietSamples  int
}

// observe reports whether pcm is user speech that should barge in.
func (d *bargeInDetector) observe(pcm []byte, playback messages.LocalPlaybackState) bool {
	level, samples := pcm16LevelFromBytes(pcm)
	if samples == 0 {
		return false
	}
	// Level includes the acoustic tail of audio that just finished playing.
	threshold := math.Max(audio.DefaultVADConfig.EnergyThreshold, bargeInEchoMargin*playback.Level)
	if level < threshold {
		d.quietSamples += samples
		if d.quietSamples > bargeInHangoverSamples {
			d.speechSamples = 0
		}
		return false
	}
	d.quietSamples = 0
	d.speechSamples += samples
	return d.speechSamples >= bargeInMinSpeechSamples
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
	if providerVAD {
		playing = messages.LocalPlaybackState{}
	}
	if !state.bargeIn.observe(pcm, playing) {
		return nil
	}
	responseActive := state.responseInFlight || state.acknowledgementOutstanding
	if responseActive && !state.continuationInFlight && !state.responseCancelSent {
		return r.sendBargeInCancel(ctx, session, state, providerVAD)
	}
	if !responseActive && playing.Active {
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
