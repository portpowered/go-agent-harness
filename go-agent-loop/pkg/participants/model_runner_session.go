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
		if audioErr != nil {
			r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
			return audioErr
		}
		if closed {
			r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
			return nil
		}
		if handled {
			continue
		}
		select {
		case <-ctx.Done():
			r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
			return ctx.Err()
		case <-session.Done():
			for {
				msg, ok := session.Receive().Read()
				if !ok {
					break
				}
				r.forwardSessionMessageState(ctx, session, &state, msg)
			}
			r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
			if !state.sessionClosed {
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
			return nil
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
				return nil
			}
			// The provider may have queued its terminal boundary after the
			// preflight but before this select chose the audio branch. Observe
			// those messages once more before evaluating barge-in state.
			r.forwardPendingSessionMessages(ctx, session, &state)
			if err := r.forwardSessionAudioWithState(ctx, session, pcm, &state); err != nil {
				r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
				return err
			}
		case evt, ok := <-r.UserEventInbox:
			if !ok {
				r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
				return nil
			}
			r.forwardPendingSessionMessages(ctx, session, &state)
			if err := r.drainSessionAudioWithState(ctx, session, &state); err != nil {
				r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
				return err
			}
			r.forwardQueuedSessionEvent(ctx, session, &state, evt)
		case req, ok := <-r.Inbox.Chan():
			if !ok {
				r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
				return nil
			}
			r.sendLatestUserText(ctx, session, req)
		case msg, ok := <-session.Receive().Chan():
			if !ok {
				r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
				return nil
			}
			r.forwardSessionMessageState(ctx, session, &state, msg)
		}
	}
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
	outcome := messages.SendSessionWithOutcome(ctx, session, msg)
	if outcome.OK() {
		return messages.StreamMessage{}, false, msg.Type == messages.StreamTypeResponseCreate
	}
	callID := ""
	classification := "unresolved_tool_result"
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		callID = value.ToolCallID
	}
	message := fmt.Sprintf("tool result %q was not delivered: session send status %q", callID, outcome.Status)
	if msg.Type == messages.StreamTypeSessionUpdate {
		classification = "unresolved_session_update"
		message = fmt.Sprintf("session tool definition update was not delivered: session send status %q", outcome.Status)
	} else if msg.Type == messages.StreamTypeResponseCreate {
		classification = "unresolved_tool_continuation"
		message = fmt.Sprintf("tool continuation was not requested: session send status %q", outcome.Status)
	}
	if msg.Type != messages.StreamTypeSessionUpdate && msg.Type != messages.StreamTypeToolCallEnd && msg.Type != messages.StreamTypeResponseCreate {
		return messages.StreamMessage{}, false, false
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
		return failure, true, false
	}
	r.DeltaOutbox.Write(ctx, failure)
	return messages.StreamMessage{}, false, false
}

// forwardQueuedSessionEvent applies the session's control-plane ordering
// rules. A normal continuation is held while an acknowledgement response is
// active; tool results themselves remain deliverable so the provider can use
// them as soon as the acknowledgement has ended.
func (r *ModelRunner) forwardQueuedSessionEvent(ctx context.Context, session messages.Session, state *sessionRunState, evt messages.StreamMessage) {
	if evt.Type == messages.StreamTypeResponseCreate && !isToolAcknowledgementResponseCreate(evt) && state.suppressContinuation {
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
	if evt.Type == messages.StreamTypeResponseCreate && !isToolAcknowledgementResponseCreate(evt) && state.acknowledgementOutstanding {
		state.deferredSessionEvents = append(state.deferredSessionEvents, evt)
		return
	}

	failure, deferred, responseAccepted := r.forwardSessionEvent(ctx, session, evt)
	if deferred {
		if failure.Type != "" {
			state.pendingSendErrors = append(state.pendingSendErrors, failure)
		}
		if evt.Type == messages.StreamTypeToolCallEnd {
			state.suppressContinuation = true
			r.sessionToolContinuation = sessionToolContinuationSuppressed
		}
	}
	if responseAccepted {
		if isToolAcknowledgementResponseCreate(evt) {
			state.acknowledgementOutstanding = true
			state.acknowledgementCancelled = false
			state.responseCancelSent = false
		} else {
			state.awaitingContinuation = true
			r.sessionToolContinuation = sessionToolContinuationAccepted
		}
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

func isToolAcknowledgementResponseCreate(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeResponseCreate {
		return false
	}
	value, ok := msg.Value.(*messages.ResponseCreateValue)
	return ok && value.IsToolAcknowledgement()
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
