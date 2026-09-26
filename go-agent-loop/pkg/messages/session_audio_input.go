package messages

import "context"

// SessionAudioInputPolicy controls whether contentful session audio is
// allowed to interrupt the response currently owned by the session runner.
// The zero value and every unknown value are intentionally interrupting so
// legacy callers remain safe by default.
type SessionAudioInputPolicy string

const (
	// SessionAudioInputPolicyDefault preserves the legacy interrupting
	// behavior for input whose origin has not been classified.
	SessionAudioInputPolicyDefault SessionAudioInputPolicy = ""
	// SessionAudioInputPolicyInterrupt marks human/customer or otherwise
	// explicitly interrupting input.
	SessionAudioInputPolicyInterrupt SessionAudioInputPolicy = "interrupt"
	// SessionAudioInputPolicyDoNotInterrupt marks audio from peer agents. It
	// still reaches the provider, but does not cancel its ordinary response.
	SessionAudioInputPolicyDoNotInterrupt SessionAudioInputPolicy = "do_not_interrupt"
)

// InterruptsResponse reports whether this policy permits contentful audio to
// cancel an ordinary in-flight response. Unknown policies use the safe,
// interrupting default.
func (p SessionAudioInputPolicy) InterruptsResponse() bool {
	return p != SessionAudioInputPolicyDoNotInterrupt
}

// SessionAudioInput carries one PCM frame and the interruption intent that
// was established at its admission boundary. Keeping the intent beside the
// bytes means a participant runner never has to infer the source from PCM
// contents or room state.
type SessionAudioInput struct {
	PCM                []byte
	InterruptionPolicy SessionAudioInputPolicy
}

// LocalPlaybackState describes provider audio that is still queued for, or
// audible on, the local playback device. Playback outlives the provider's
// response lifecycle: audio arrives faster than real time, so a response can
// be done while seconds of it are still playing.
type LocalPlaybackState struct {
	// Active reports that provider audio is still queued or audible.
	Active bool
	// Level is the RMS, in PCM16 units, of the audio currently audible. It is
	// the level an acoustic echo of the playback has at a unity-gain
	// microphone and serves as the echo reference for local barge-in.
	Level float64
}

// SessionLocalPlayback is implemented by sessions that own local playback of
// provider audio.
type SessionLocalPlayback interface {
	LocalPlayback() LocalPlaybackState
	// InterruptLocalPlayback discards playback that has not been heard and
	// truncates the audible conversation item at the heard position. It
	// reports whether any playback was interrupted.
	InterruptLocalPlayback(ctx context.Context) bool
}

// SessionTurnDetection is implemented by sessions that know whether the
// provider detects user speech itself (server VAD). Such a provider cancels
// the active response and reports speech_started, so a client-side energy
// detector must not second-guess it.
type SessionTurnDetection interface {
	ProviderTurnDetection() bool
}
