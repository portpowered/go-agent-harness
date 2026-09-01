package messages

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
