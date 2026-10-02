package participants

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants/internal/sessionstate"
)

// Session control plane: the ordering rules for user and tool control events
// (RESPONSE.CREATE, MESSAGE.END, RESPONSE.CANCEL, TOOLCALL.END, ...) relative
// to the provider's response lifecycle.

// forwardQueuedSessionEvent applies the session's control-plane ordering
// rules. A normal continuation is held while an acknowledgement response is
// active; tool results themselves remain deliverable so the provider can use
// them as soon as the acknowledgement has ended.
func (r *ModelRunner) forwardQueuedSessionEvent(ctx context.Context, session messages.Session, state *sessionRunState, evt messages.StreamMessage) {
	// Held onset audio precedes any later control, except an explicit cancel:
	// that is the interrupt the held audio was waiting to decide.
	if evt.Type == messages.StreamTypeResponseCancel {
		defer r.flushHeldAudio(ctx, session, state)
	} else {
		r.flushHeldAudio(ctx, session, state)
	}
	if r.suppressRejectedBatchContinuation(ctx, state, evt) {
		return
	}
	if state.HoldForAcknowledgement(evt) {
		// A held continuation replays from Deferred and is consumed then. A
		// held acknowledgement is dropped and never replays, so its pending
		// tool boundary is released now.
		if sessionstate.IsAcknowledgementCreate(evt) {
			r.markSessionToolEventConsumed(evt)
		}
		return
	}
	// A request for a new response waits only while a cancel is actually
	// outstanding (or, for a continuation, while any response is live). The
	// customer's own end-of-turn boundary for a response that is streaming
	// with no cancel sent (server-VAD auto-response) must still reach the
	// wire immediately, or the session hangs waiting for a terminal event that
	// will never arrive. Deferred events replay from flushDeferredSessionEvents.
	if sessionstate.IsContinuationCreate(evt) {
		r.syncProviderMessages(ctx, session, state)
	}
	if state.DeferResponseRequest(evt) {
		state.Deferred = append(state.Deferred, evt)
		return
	}
	defer r.markSessionToolEventConsumed(evt)

	failure, deferred, responseAccepted, admitted := r.forwardSessionEventOutcome(ctx, session, evt)
	switch {
	case deferred:
		r.noteDeferredSessionFailure(state, evt, failure)
	case responseAccepted:
		r.noteAcceptedSessionResponse(state, evt)
	case evt.Type == messages.StreamTypeResponseCancel && admitted:
		// Live hosts send explicit cancellation through the same ordered
		// control path as audio admission. Match the automatic barge-in state
		// so late untagged provider deltas cannot escape from the cancelled
		// response.
		state.NoteCancelSent()
	case failure.Type != "" && evt.Type == messages.StreamTypeResponseCreate && !sessionstate.IsAcknowledgementCreate(evt):
		r.sessionToolContinuation = sessionToolContinuationSuppressed
	}
}

// suppressRejectedBatchContinuation consumes the continuation request of a
// tool-result batch in which a result was rejected at the provider boundary.
// Asking the provider to continue from a partially delivered batch would
// answer an obligation that was never delivered; the accepted sibling remains
// pending and the deferred result error names the rejected call.
func (r *ModelRunner) suppressRejectedBatchContinuation(ctx context.Context, state *sessionRunState, evt messages.StreamMessage) bool {
	if evt.Type != messages.StreamTypeResponseCreate || sessionstate.IsAcknowledgementCreate(evt) || !state.ToolBatch.Rejected {
		return false
	}
	r.markSessionToolEventConsumed(evt)
	state.ToolBatch.Rejected = false
	r.sessionToolContinuation = sessionToolContinuationSuppressed
	r.flushPendingSessionSendErrors(ctx, state.ToolBatch.TakeFailures())
	return true
}

func (r *ModelRunner) markSessionToolEventConsumed(evt messages.StreamMessage) {
	if isSessionToolEvent(evt) {
		r.ingress.toolEvents.consumed()
	}
}

func (r *ModelRunner) hasPendingSessionToolEvents() bool {
	return r.ingress.toolEvents.pending()
}

func (r *ModelRunner) noteAcceptedSessionResponse(state *sessionRunState, evt messages.StreamMessage) {
	if sessionstate.IsAcknowledgementCreate(evt) {
		state.Ack.Request()
		state.Response.ClearCancel()
		return
	}
	if sessionstate.IsContinuationCreate(evt) {
		state.Continuation.Request()
	}
	r.sessionToolContinuation = sessionToolContinuationAccepted
}

func (r *ModelRunner) noteDeferredSessionFailure(state *sessionRunState, evt, failure messages.StreamMessage) {
	if failure.Type != "" {
		state.ToolBatch.Failures = append(state.ToolBatch.Failures, failure)
	}
	if evt.Type == messages.StreamTypeToolCallEnd {
		state.ToolBatch.Rejected = true
		r.sessionToolContinuation = sessionToolContinuationSuppressed
	}
}

func (r *ModelRunner) flushDeferredSessionEvents(ctx context.Context, session messages.Session, state *sessionRunState) {
	for _, evt := range state.TakeDeferred() {
		r.forwardQueuedSessionEvent(ctx, session, state, evt)
	}
}

// syncProviderMessages observes every provider message queued before a tool
// continuation request. Without a purpose echo the continuation binds to the
// first response opened after its request, so a server-VAD response already
// open at the provider but still behind the relay must be seen first: the
// request is then deferred until that response ends. The relay may block
// while Receive is full, so Receive is drained while the barrier completes.
// A response the provider opens after this point races the request itself;
// the provider rejects the request and the guessed binding is released.
func (r *ModelRunner) syncProviderMessages(ctx context.Context, session messages.Session, state *sessionRunState) {
	syncer, ok := session.(messages.SessionReceiveSyncer)
	if !ok {
		return
	}
	synced := make(chan struct{})
	go func() {
		defer close(synced)
		syncer.SyncReceive(ctx)
	}()
	for {
		select {
		case <-synced:
			r.forwardPendingSessionMessages(ctx, session, state)
			return
		case msg, open := <-session.Receive().Chan():
			if !open {
				<-synced
				return
			}
			r.forwardSessionMessageState(ctx, session, state, msg)
		}
	}
}

// forwardSessionEvent preserves the legacy best-effort behavior for ordinary
// user events, but turns a rejected tool-result or continuation send into an
// observable stream error. The session lifecycle can then report the still-
// unresolved obligation instead of allowing a false clean close.
func (r *ModelRunner) forwardSessionEvent(ctx context.Context, session messages.Session, msg messages.StreamMessage) (messages.StreamMessage, bool, bool) {
	failure, deferred, responseAccepted, _ := r.forwardSessionEventOutcome(ctx, session, msg)
	return failure, deferred, responseAccepted
}

// forwardSessionEventOutcome sends one control event and reports, in order:
// a failure to defer or publish, whether that failure is deferred to the
// batch boundary, whether a RESPONSE.CREATE was accepted, and whether the
// event was admitted at all (distinguishing an accepted RESPONSE.CANCEL from
// an ordinary event rejected silently).
func (r *ModelRunner) forwardSessionEventOutcome(ctx context.Context, session messages.Session, msg messages.StreamMessage) (messages.StreamMessage, bool, bool, bool) {
	if sessionAdmissionClosed(session) && sessionEventBlockedByAdmission(session, msg) {
		// The room has already recorded its bound and is draining an existing
		// response. Tool results, continuations, and configuration updates that
		// cross this boundary are not admitted and are not session failures.
		return messages.StreamMessage{}, false, false, false
	}
	outcome := messages.SendSessionWithOutcome(ctx, session, msg)
	if outcome.OK() {
		return messages.StreamMessage{}, false, msg.Type == messages.StreamTypeResponseCreate, true
	}
	failure, ok := unresolvedSendFailure(msg, outcome)
	if !ok {
		return messages.StreamMessage{}, false, false, false
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

// unresolvedSendFailure builds the stream error for a rejected send that
// leaves a tool obligation or session update unresolved. Other rejected
// events stay best-effort.
func unresolvedSendFailure(msg messages.StreamMessage, outcome messages.SessionSendOutcome) (messages.StreamMessage, bool) {
	var classification, message string
	switch msg.Type { //nolint:exhaustive // Only obligation-carrying sends become failures; other rejected events stay best-effort.
	case messages.StreamTypeToolCallEnd:
		callID := ""
		if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
			callID = value.ToolCallID
		}
		classification = unresolvedToolResultClassification
		message = fmt.Sprintf("tool result %q was not delivered: session send status %q", callID, outcome.Status)
	case messages.StreamTypeSessionUpdate:
		classification = unresolvedSessionUpdateClassification
		message = fmt.Sprintf("session tool definition update was not delivered: session send status %q", outcome.Status)
	case messages.StreamTypeResponseCreate:
		classification = unresolvedToolContinuationClassification
		message = fmt.Sprintf("tool continuation was not requested: session send status %q", outcome.Status)
	default:
		return messages.StreamMessage{}, false
	}
	value := messages.NewErrorValueWithTerminal(
		message,
		classification,
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputNone,
	)
	value.Err = outcome.Err
	return messages.StreamMessage{Type: messages.StreamTypeError, Value: value}, true
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

// sessionEventBlockedByAdmission reports whether a closed admission boundary
// blocks msg. Without a session policy only RESPONSE.CANCEL and SESSION.CLOSE
// still cross it.
func sessionEventBlockedByAdmission(session messages.Session, msg messages.StreamMessage) bool {
	if policy, ok := session.(sessionAdmissionPolicy); ok {
		return !policy.SessionAdmissionAllows(msg)
	}
	return msg.Type != messages.StreamTypeResponseCancel && msg.Type != messages.StreamTypeSessionClose
}

func forwardSessionCompleteMessage(ctx context.Context, session messages.Session, msg messages.Message, requestResponse bool) error {
	if requestResponse {
		if !messages.SupportsSessionMessages(session) {
			return errors.New("session does not support complete messages")
		}
		if !messages.SendSessionMessage(ctx, session, msg) {
			return errors.New("session rejected complete message")
		}
		return nil
	}
	if !messages.SupportsSessionMessagesWithoutResponse(session) {
		return errors.New("session does not support complete messages without response")
	}
	if !messages.SendSessionMessageWithoutResponse(ctx, session, msg) {
		return errors.New("session rejected complete message without response")
	}
	return nil
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

func (r *ModelRunner) flushPendingSessionSendErrors(ctx context.Context, failures []messages.StreamMessage) {
	for _, failure := range failures {
		r.DeltaOutbox.Write(ctx, failure)
	}
}

// SessionSendError reports a user audio frame or barge-in cancel that the
// provider session did not admit. It ends the session runner; match it with
// errors.As to read the send status, and errors.Is reaches the cause.
type SessionSendError struct {
	// Operation is what was being sent: "audio" or "response cancel".
	Operation string
	Status    messages.SessionSendStatus
	Err       error
}

func (e *SessionSendError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("session %s send failed with status %q: %v", e.Operation, e.Status, e.Err)
	}
	return fmt.Sprintf("session %s send failed with status %q", e.Operation, e.Status)
}

func (e *SessionSendError) Unwrap() error { return e.Err }

func sessionAudioSendError(operation string, outcome messages.SessionSendOutcome) error {
	return &SessionSendError{Operation: operation, Status: outcome.Status, Err: outcome.Err}
}
