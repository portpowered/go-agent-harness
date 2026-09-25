package participants

import (
	"context"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type initialSessionConfigSentMarker interface {
	InitialSessionConfigSent() bool
}

func providerSentInitialSessionConfig(session messages.Session) bool {
	marker, ok := session.(initialSessionConfigSentMarker)
	return ok && marker.InitialSessionConfigSent()
}

func (r *ModelRunner) forwardInitialSessionConfig(ctx context.Context, session messages.Session, state *sessionRunState, msg messages.StreamMessage) {
	if (msg.Type != messages.StreamTypeSessionOpen && msg.Type != messages.StreamTypeSessionCreated) ||
		r.sessionConfig == nil || state == nil || state.initialSessionConfigSent || providerSentInitialSessionConfig(session) {
		return
	}
	// Providers may emit both SESSION.OPEN and SESSION.CREATED for one
	// connection. The first lifecycle event owns the initial configuration;
	// do not echo it a second time when the other event arrives.
	state.initialSessionConfigSent = true
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

// Classifications for provider-boundary sends that leave a tool obligation
// or session update unresolved.
const (
	unresolvedToolResultClassification       = "unresolved_tool_result"
	unresolvedSessionUpdateClassification    = "unresolved_session_update"
	unresolvedToolContinuationClassification = "unresolved_tool_continuation"
)

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

// endSession finishes runSession: a live-context failure is published before
// deferred send failures are flushed, and err is returned unchanged.
func (r *ModelRunner) endSession(ctx context.Context, state *sessionRunState, err error) error {
	if err != nil && ctx.Err() == nil {
		r.publishSessionAudioFailure(err, state.hasOutput)
	}
	r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
	return err
}

// SessionIngressError is a constant sentinel error for session ingress
// admission; being a constant, it cannot be reassigned.
type SessionIngressError string

func (e SessionIngressError) Error() string { return string(e) }

// ErrSessionClosed reports that a waiting session admission was abandoned
// because the session runner stopped and will never drain the ingress again.
const ErrSessionClosed SessionIngressError = "session runner stopped; input was not admitted"

// sessionIngressStop is closed once when the session runner returns, so
// admissions parked on a full ingress are released instead of waiting for a
// consumer that no longer exists.
type sessionIngressStop struct {
	init, closeOnce sync.Once
	ch              chan struct{}
}

func (s *sessionIngressStop) done() <-chan struct{} {
	s.init.Do(func() { s.ch = make(chan struct{}) })
	return s.ch
}

func (s *sessionIngressStop) stop() {
	s.done()
	s.closeOnce.Do(func() { close(s.ch) })
}

func (s *sessionIngressStop) stopped() bool {
	select {
	case <-s.done():
		return true
	default:
		return false
	}
}

// EnqueueSessionEvent queues a control-plane event in the same ordered ingress
// as audio admitted by EnqueueSessionAudioInput. It does not wait for ingress
// capacity: a full ingress returns ErrSessionInputQueueFull. It does, however,
// take the FIFO admission lock that orders all session inputs, so it can wait
// behind a waiting admission (EnqueueSessionEventWaiting or
// EnqueueSessionAudioInputWithPolicyWaiting) that is parked on a full ingress,
// until that admission is drained, cancelled, or released by runner shutdown.
func (r *ModelRunner) EnqueueSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	return r.enqueueSessionEvent(ctx, msg, false, "EnqueueSessionEvent")
}

func (r *ModelRunner) EnqueueSessionEventWaiting(ctx context.Context, msg messages.StreamMessage) error {
	return r.enqueueSessionEvent(ctx, msg, true, "EnqueueSessionEventWaiting")
}

func (r *ModelRunner) enqueueSessionEvent(ctx context.Context, msg messages.StreamMessage, waitForCapacity bool, operation string) error {
	if r == nil || r.sessionInputInbox == nil {
		return fmt.Errorf("%s: not in session mode", operation)
	}
	if ctx == nil && waitForCapacity {
		return fmt.Errorf("%s: nil context", operation)
	}
	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	// A nil context on the non-waiting path means "no cancellation"; its nil
	// done channel never becomes ready.
	var done <-chan struct{}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		done = ctx.Done()
	}
	r.markSessionToolEventQueued(msg)
	input := sessionInput{kind: sessionInputEvent, event: msg}
	if waitForCapacity {
		if r.ingressStop.stopped() {
			r.markSessionToolEventConsumed(msg)
			return ErrSessionClosed
		}
		select {
		case r.sessionInputInbox <- input:
			return nil
		case <-done:
			r.markSessionToolEventConsumed(msg)
			return ctx.Err()
		case <-r.ingressStop.done():
			r.markSessionToolEventConsumed(msg)
			return ErrSessionClosed
		}
	}
	select {
	case r.sessionInputInbox <- input:
		return nil
	case <-done:
		r.markSessionToolEventConsumed(msg)
		return ctx.Err()
	default:
		r.markSessionToolEventConsumed(msg)
		return ErrSessionInputQueueFull
	}
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
		if r.ingressStop.stopped() {
			return ErrSessionClosed
		}
		select {
		case r.sessionInputInbox <- input:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-r.ingressStop.done():
			return ErrSessionClosed
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

// sessionStepResult ends the session loop when a forwarding step failed.
func (r *ModelRunner) sessionStepResult(ctx context.Context, state *sessionRunState, err error) (bool, error) {
	if err != nil {
		return true, r.endSession(ctx, state, err)
	}
	return false, nil
}

// finishClosedSession drains the provider's final queued messages and, unless
// the provider already reported its own close, emits the terminal SESSION.CLOSE.
func (r *ModelRunner) finishClosedSession(ctx context.Context, session messages.Session, state *sessionRunState) {
	for {
		msg, ok := session.Receive().Read()
		if !ok {
			break
		}
		r.forwardSessionMessageState(ctx, session, state, msg)
	}
	r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
	if state.sessionClosed {
		return
	}
	terminalProvenance := messages.TerminalProvenanceProvider
	terminalOutputState := outputState(state.hasOutput)
	if state.responseCompleted {
		// Preserve the existing session teardown contract after a
		// completed response. A transport close before any response
		// boundary remains provider-authored and uses observed output.
		terminalProvenance = messages.TerminalProvenanceSession
		terminalOutputState = messages.TerminalOutputNotApplicable
	}
	r.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			"provider_closed",
			"transport",
			messages.TerminalReasonProviderClose,
			terminalProvenance,
			terminalOutputState,
		),
	})
}

func startSessionResponse(state *sessionResponseState, msgID string, acknowledgementResponse bool) {
	if !beginSessionResponse(state, msgID) {
		return
	}
	state.hasOutput = false
	state.responseCompleted = false
	state.responseCancelSent = false
	state.responseInFlight = true
	if acknowledgementResponse && state.acknowledgementCancelled {
		state.responseCancelSent = true
		if msgID != "" {
			state.cancelledResponseIDs[msgID] = struct{}{}
		}
	}
}

// endSessionResponse applies an owned MESSAGE.END to the response lifecycle
// and reports whether this message ended the current response.
func endSessionResponse(state *sessionResponseState, msg *messages.StreamMessage, msgID string, acknowledgementResponse bool) bool {
	if !ownsSessionResponseEnd(state, msgID) {
		return false
	}
	ownedID := msgID
	if ownedID == "" {
		ownedID = state.currentResponseID
	}
	state.responseInFlight = false
	switch {
	case state.responseCancelSent:
		// Realtime providers normally acknowledge RESPONSE.CANCEL with a
		// response.done event. Preserve that wire boundary so the next
		// input can proceed, but mark it as interrupted rather than a
		// normally completed assistant turn.
		msg.Value = interruptedMessageEndValue(msg.Value, state.hasOutput)
		state.responseCompleted = false
	case acknowledgementResponse:
		// A progress acknowledgement is never the assistant turn that
		// satisfies a user input or a tool continuation.
		state.responseCompleted = false
	default:
		state.responseCompleted = true
	}
	if ownedID != "" {
		state.terminalResponseIDs[ownedID] = struct{}{}
	}
	state.currentResponseID = ""
	if acknowledgementResponse {
		state.acknowledgementOutstanding = false
		state.acknowledgementCancelled = false
		state.acknowledgementEnded = true
		state.responseCancelSent = false
		state.hasOutput = false
	}
	return true
}
