package participants

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type initialSessionConfigSentMarker interface {
	InitialSessionConfigSent() bool
}

func providerSentInitialSessionConfig(session messages.Session) bool {
	marker, ok := session.(initialSessionConfigSentMarker)
	return ok && marker.InitialSessionConfigSent()
}

func (r *ModelRunner) forwardInitialSessionConfig(ctx context.Context, session messages.Session, msg messages.StreamMessage) {
	if msg.Type != messages.StreamTypeSessionCreated || r.sessionConfig == nil || providerSentInitialSessionConfig(session) {
		return
	}
	r.forwardSessionEvent(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdate,
		Value: messages.NewSessionUpdateValue(r.sessionConfig),
	})
}

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

func (r *ModelRunner) EnqueueSessionAudioInput(ctx context.Context, pcm []byte) error {
	return r.enqueueSessionAudioInput(ctx, pcm, messages.SessionAudioInputPolicyDefault, "EnqueueSessionAudioInput")
}

func (r *ModelRunner) EnqueueSessionAudioInputWithPolicy(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	return r.enqueueSessionAudioInput(ctx, pcm, policy, "EnqueueSessionAudioInputWithPolicy")
}

func (r *ModelRunner) EnqueueSessionAudioInputWithPolicyWaiting(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	return r.enqueueSessionAudioInputWaiting(ctx, pcm, policy, "EnqueueSessionAudioInputWithPolicyWaiting")
}

func (r *ModelRunner) enqueueSessionAudioInputWaiting(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy, operation string) error {
	if r == nil || r.sessionInputInbox == nil {
		return fmt.Errorf("%s: not in session mode", operation)
	}
	if ctx == nil {
		return fmt.Errorf("%s: nil context", operation)
	}
	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.enqueueSessionAudioInputLocked(ctx, pcm, policy, true)
}

func (r *ModelRunner) enqueueSessionAudioInputLocked(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy, waitForCapacity bool) error {
	input := sessionInput{kind: sessionInputAudio, audio: messages.SessionAudioInput{PCM: pcm, InterruptionPolicy: policy}}
	if waitForCapacity {
		select {
		case r.sessionInputInbox <- input:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case r.sessionInputInbox <- input:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrSessionInputQueueFull
	}
}
