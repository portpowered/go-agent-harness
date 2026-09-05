package participants

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// sessionAudioSendFailureClassification is the stable stream classification
// for a provider-bound audio write that could not be admitted. The runner
// still returns the original error to its owner; this companion ERROR delta is
// what wakes the engine's ordering loop when the participant is running in a
// background goroutine.
const sessionAudioSendFailureClassification = "session_audio_send_failed"

// publishSessionAudioFailure makes a fatal audio forwarding error observable
// to the engine before runSession returns it. ActiveParticipant intentionally
// owns runner lifecycle and does not consume Run's error return, so returning
// alone would leave GlobalOrdering waiting on an open DeltaOutbox forever.
//
// WriteTerminal is deliberate: the caller may already be shutting down its
// context, and a terminal diagnostic must survive a full ordinary outbox.
// The original error is retained in ErrorValue.Err for errors.Is/errors.As;
// callers still return that same error from Run.
func (r *ModelRunner) publishSessionAudioFailure(err error, hasOutput bool) {
	if r == nil || err == nil || r.DeltaOutbox == nil {
		return
	}
	value := messages.NewErrorValueWithError(err)
	value.Classification = sessionAudioSendFailureClassification
	value.TerminalReason = messages.TerminalReasonTerminalFailure
	value.TerminalProvenance = messages.TerminalProvenanceLoop
	value.OutputState = outputState(hasOutput)
	r.DeltaOutbox.WriteTerminal(messages.StreamMessage{
		Type:       messages.StreamTypeError,
		Role:       messages.RoleAssistant,
		ActorID:    messages.Model,
		LoopPassID: r.currentPassID,
		Value:      value,
	})
}
