package participants

import (
	"context"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func (r *ModelRunner) runSession(ctx context.Context) error {
	session, err := r.sessionInferencer.ConnectSession(ctx)
	if err != nil {
		return fmt.Errorf("session connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	state := sessionRunState{}
	state.ensureMaps()

	for {
		// Observe already-queued provider lifecycle messages before admitting
		// pending user input. In particular, MESSAGE.END is authoritative for
		// the response that just completed; peer audio queued in the same
		// scheduling step must not cause a late RESPONSE.CANCEL for it. Once the
		// provider queue is empty, preserve the deterministic user-input turn so
		// a scheduled audio frame remains ordered before its own commit and
		// response.create boundary.
		handled, closed, audioErr := r.forwardPendingSessionInputs(ctx, session, &state)
		if audioErr != nil || closed {
			return r.endSession(ctx, &state, audioErr)
		}
		if handled {
			continue
		}
		if done, err := r.awaitSessionStep(ctx, session, &state); done {
			return err
		}
	}
}

// awaitSessionStep blocks for the next provider, user, or lifecycle event and
// reports whether the session loop has finished along with its result.
func (r *ModelRunner) awaitSessionStep(ctx context.Context, session messages.Session, state *sessionRunState) (bool, error) {
	select {
	case <-ctx.Done():
		return true, r.endSession(ctx, state, ctx.Err())
	case <-session.Done():
		r.finishClosedSession(ctx, session, state)
		return true, nil
	case input, ok := <-r.sessionInputInbox:
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		return r.awaitedSessionInput(ctx, session, state, input)
	case pcm, ok := <-r.UserAudioInbox:
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		// The provider may have queued its terminal boundary after the
		// preflight but before this select chose the audio branch. Observe
		// those messages once more before evaluating barge-in state.
		r.forwardPendingSessionMessages(ctx, session, state)
		return r.sessionStepResult(ctx, state, r.forwardSessionAudioWithState(ctx, session, pcm, state))
	case evt, ok := <-r.UserEventInbox:
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		return r.awaitedSessionEvent(ctx, session, state, evt)
	case req, ok := <-r.Inbox.Chan():
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		r.sendLatestUserText(ctx, session, req)
	case msg, ok := <-session.Receive().Chan():
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		r.forwardSessionMessageState(ctx, session, state, msg)
	}
	return false, nil
}

func (r *ModelRunner) awaitedSessionInput(ctx context.Context, session messages.Session, state *sessionRunState, input sessionInput) (bool, error) {
	if input.kind == sessionInputAudio {
		// Observe provider completion queued after the preflight, before barge-in.
		r.forwardPendingSessionMessages(ctx, session, state)
	}
	return r.sessionStepResult(ctx, state, r.forwardSessionInput(ctx, session, state, input))
}

func (r *ModelRunner) awaitedSessionEvent(ctx context.Context, session messages.Session, state *sessionRunState, evt messages.StreamMessage) (bool, error) {
	r.forwardPendingSessionMessages(ctx, session, state)
	if err := r.drainSessionAudioWithState(ctx, session, state); err != nil {
		return true, r.endSession(ctx, state, err)
	}
	r.forwardQueuedSessionEvent(ctx, session, state, evt)
	return false, nil
}

// forwardPendingSessionInputs first drains provider messages that are already
// queued, so an observed response terminal boundary wins over queued peer
// audio. It then gives queued user audio/control messages a deterministic
// transport turn. It returns whether it forwarded anything and whether either
// user inbox was closed.
func (r *ModelRunner) forwardPendingSessionInputs(ctx context.Context, session messages.Session, state *sessionRunState) (handled, closed bool, audioErr error) {
	state.ensureMaps()
	for {
		if r.forwardPendingSessionMessages(ctx, session, state) {
			handled = true
			continue
		}
		select {
		case input, ok := <-r.sessionInputInbox:
			if !ok {
				return true, true, nil
			}
			if err := r.forwardSessionInput(ctx, session, state, input); err != nil {
				return true, false, err
			}
			handled = true
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return true, true, nil
			}
			if err := r.forwardSessionAudioWithState(ctx, session, pcm, state); err != nil {
				return true, false, err
			}
			handled = true
		case evt, ok := <-r.UserEventInbox:
			if !ok {
				return true, true, nil
			}
			if err := r.drainSessionAudioWithState(ctx, session, state); err != nil {
				return true, false, err
			}
			r.forwardQueuedSessionEvent(ctx, session, state, evt)
			handled = true
		default:
			return handled, false, nil
		}
	}
}

func (r *ModelRunner) drainSessionAudio(ctx context.Context, session messages.Session, responseInFlight, responseCancelSent *bool) error {
	state := newSessionResponseState()
	state.responseInFlight = responseInFlight != nil && *responseInFlight
	state.responseCancelSent = responseCancelSent != nil && *responseCancelSent
	err := r.drainSessionAudioWithState(ctx, session, state)
	if responseInFlight != nil {
		*responseInFlight = state.responseInFlight
	}
	if responseCancelSent != nil {
		*responseCancelSent = state.responseCancelSent
	}
	return err
}

func (r *ModelRunner) forwardSessionAudioInputWithState(ctx context.Context, session messages.Session, input messages.SessionAudioInput, state *sessionResponseState) error {
	return r.forwardSessionAudioWithPolicyWithState(ctx, session, input.PCM, input.InterruptionPolicy, state)
}

func (r *ModelRunner) forwardSessionAudio(ctx context.Context, session messages.Session, pcm []byte, responseInFlight, responseCancelSent *bool) error {
	state := newSessionResponseState()
	state.responseInFlight = responseInFlight != nil && *responseInFlight
	state.responseCancelSent = responseCancelSent != nil && *responseCancelSent
	err := r.forwardSessionAudioWithState(ctx, session, pcm, state)
	if responseInFlight != nil {
		*responseInFlight = state.responseInFlight
	}
	if responseCancelSent != nil {
		*responseCancelSent = state.responseCancelSent
	}
	return err
}

func sessionAudioSendError(operation string, outcome messages.SessionSendOutcome) error {
	if outcome.Err != nil {
		return fmt.Errorf("session %s send failed with status %q: %w", operation, outcome.Status, outcome.Err)
	}
	return fmt.Errorf("session %s send failed with status %q", operation, outcome.Status)
}

// forwardSessionEvent preserves the legacy best-effort behavior for ordinary
// user events, but turns a rejected tool-result or continuation send into an
// observable stream error. The session lifecycle can then report the still-
// unresolved obligation instead of allowing a false clean close.
func (r *ModelRunner) forwardSessionEvent(ctx context.Context, session messages.Session, msg messages.StreamMessage) (messages.StreamMessage, bool, bool) {
	failure, deferred, responseAccepted, _ := r.forwardSessionEventOutcome(ctx, session, msg)
	return failure, deferred, responseAccepted
}

// forwardSessionEventOutcome is the control-plane variant used by the session
// state machine. The historical three-value helper deliberately preserves its
// response-create-only meaning for callers and tests; this additional admitted
// result lets callers distinguish a successful RESPONSE.CANCEL from an
// ordinary rejected/best-effort event. A zero failure is not sufficient for
// that distinction because ordinary rejected events intentionally remain
// silent.
func (r *ModelRunner) forwardSessionEventOutcome(ctx context.Context, session messages.Session, msg messages.StreamMessage) (messages.StreamMessage, bool, bool, bool) {
	if sessionAdmissionClosed(session) && sessionEventBlockedByAdmissionForSession(session, msg) {
		// The room has already recorded its bound and is draining an existing
		// response. Tool results, continuations, and configuration updates that
		// cross this boundary are not admitted and are not session failures.
		return messages.StreamMessage{}, false, false, false
	}
	outcome := messages.SendSessionWithOutcome(ctx, session, msg)
	if outcome.OK() {
		return messages.StreamMessage{}, false, msg.Type == messages.StreamTypeResponseCreate, true
	}
	callID := ""
	classification := unresolvedToolResultClassification
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		callID = value.ToolCallID
	}
	message := fmt.Sprintf("tool result %q was not delivered: session send status %q", callID, outcome.Status)
	if msg.Type == messages.StreamTypeSessionUpdate {
		classification = unresolvedSessionUpdateClassification
		message = fmt.Sprintf("session tool definition update was not delivered: session send status %q", outcome.Status)
	}
	if msg.Type == messages.StreamTypeResponseCreate {
		classification = unresolvedToolContinuationClassification
		message = fmt.Sprintf("tool continuation was not requested: session send status %q", outcome.Status)
	}
	if msg.Type != messages.StreamTypeSessionUpdate && msg.Type != messages.StreamTypeToolCallEnd && msg.Type != messages.StreamTypeResponseCreate {
		return messages.StreamMessage{}, false, false, false
	}
	value := messages.NewErrorValueWithTerminal(
		message,
		classification,
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputNone,
	)
	value.Err = outcome.Err
	failure := messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: value,
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		// A batch may contain another result that was accepted. Keep this
		// failure until the batch's continuation boundary so the caller can
		// suppress that invalid continuation and then report every remaining
		// per-call obligation together.
		return failure, true, false, false
	}
	r.DeltaOutbox.Write(ctx, failure)
	return messages.StreamMessage{}, false, false, false
}

type sessionAdmissionController interface {
	SessionAdmissionClosed() bool
}

type sessionAdmissionPolicy interface {
	SessionAdmissionAllows(messages.StreamMessage) bool
}

func sessionAdmissionClosed(session messages.Session) bool {
	controller, ok := session.(sessionAdmissionController)
	return ok && controller.SessionAdmissionClosed()
}

func sessionEventBlockedByAdmission(msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeResponseCancel, messages.StreamTypeSessionClose:
		return false
	default:
		return true
	}
}

func sessionEventBlockedByAdmissionForSession(session messages.Session, msg messages.StreamMessage) bool {
	if policy, ok := session.(sessionAdmissionPolicy); ok {
		return !policy.SessionAdmissionAllows(msg)
	}
	return sessionEventBlockedByAdmission(msg)
}

// forwardQueuedSessionEvent applies the session's control-plane ordering
// rules. A normal continuation is held while an acknowledgement response is
// active; tool results themselves remain deliverable so the provider can use
// them as soon as the acknowledgement has ended.
func (r *ModelRunner) forwardQueuedSessionEvent(ctx context.Context, session messages.Session, state *sessionRunState, evt messages.StreamMessage) {
	if evt.Type == messages.StreamTypeResponseCreate && !isToolAcknowledgementResponseCreate(evt) && state.suppressContinuation {
		r.markSessionToolEventConsumed(evt)
		// A result in this batch was rejected at the provider boundary. Do not
		// ask the provider to continue from a partially delivered batch; the
		// accepted sibling remains pending and the deferred result error names
		// the rejected call when the session reaches a terminal path.
		state.suppressContinuation = false
		r.sessionToolContinuation = sessionToolContinuationSuppressed
		r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
		state.pendingSendErrors = nil
		return
	}
	if holdForAcknowledgement(state, evt) {
		return
	}
	// A control-plane event that asks the provider to open a new response
	// (an ordinary continuation, or MESSAGE.END's commit-then-create
	// end-of-turn boundary) must never be sent while a RESPONSE.CANCEL for
	// the currently active response is still unacknowledged: the provider
	// can then see a request for a second response before it has finished
	// (or even acknowledged cancelling) the first, which is the
	// state/timing mismatch behind the provider's response_cancel_not_active
	// rejection. This is deliberately narrower than "any in-flight
	// response" -- a response can legitimately still be in flight with no
	// cancel ever sent (the customer's own end-of-turn boundary for the very
	// utterance that response is answering, e.g. under server-side VAD
	// auto-response), and that boundary must still reach the wire
	// immediately or the session hangs waiting for a terminal event that
	// will never arrive. Hold the event only when a cancel is actually
	// outstanding, then replay it from flushDeferredSessionEvents once that
	// boundary is observed.
	requestsNewResponse := evt.Type == messages.StreamTypeMessageEnd ||
		(evt.Type == messages.StreamTypeResponseCreate && !isToolAcknowledgementResponseCreate(evt))
	if requestsNewResponse && state.responseCancelSent && (state.responseInFlight || state.acknowledgementOutstanding) {
		state.deferredSessionEvents = append(state.deferredSessionEvents, evt)
		return
	}
	defer r.markSessionToolEventConsumed(evt)

	failure, deferred, responseAccepted, admitted := r.forwardSessionEventOutcome(ctx, session, evt)
	if deferred {
		r.noteDeferredSessionFailure(state, evt, failure)
	}
	if responseAccepted {
		r.noteAcceptedSessionResponse(state, evt)
	} else if evt.Type == messages.StreamTypeResponseCancel && admitted {
		r.noteAcceptedSessionCancel(state)
	} else if failure.Type != "" && evt.Type == messages.StreamTypeResponseCreate && !isToolAcknowledgementResponseCreate(evt) {
		r.sessionToolContinuation = sessionToolContinuationSuppressed
	}
}

func (r *ModelRunner) flushDeferredSessionEvents(ctx context.Context, session messages.Session, state *sessionRunState) {
	deferred := state.deferredSessionEvents
	state.deferredSessionEvents = nil
	for _, evt := range deferred {
		r.forwardQueuedSessionEvent(ctx, session, state, evt)
	}
}

func (r *ModelRunner) flushPendingSessionSendErrors(ctx context.Context, failures []messages.StreamMessage) {
	for _, failure := range failures {
		r.DeltaOutbox.Write(ctx, failure)
	}
}

// forwardSessionMessageWithState forwards one provider event and updates the
// identity-aware response lifecycle. The return value is true only when this
// event is the terminal MESSAGE.END for the currently owned response.
func normalizeSessionCloseMessage(msg messages.StreamMessage) messages.StreamMessage {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return msg
	}
	if value.TerminalReason == "" {
		if value.Reason == "provider_closed" {
			value.TerminalReason = messages.TerminalReasonProviderClose
		} else {
			value.TerminalReason = messages.TerminalReasonSessionClose
		}
	}
	if value.Classification == "" {
		// The gateway public taxonomy classifies a provider transport close
		// without completion as transport; clean session closes keep their
		// descriptive reason.
		if value.TerminalReason == messages.TerminalReasonProviderClose {
			value.Classification = "transport"
		} else {
			value.Classification = string(value.TerminalReason)
		}
	}
	if value.TerminalProvenance == "" {
		value.TerminalProvenance = messages.TerminalProvenanceSession
	}
	if value.OutputState == "" {
		value.OutputState = messages.TerminalOutputNotApplicable
	}
	return msg
}

func hasPCM16Signal(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}
