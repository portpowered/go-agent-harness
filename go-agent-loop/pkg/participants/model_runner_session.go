package participants

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants/internal/sessionstate"
)

// sessionRunState is the lifecycle state owned by the session goroutine.
type sessionRunState = sessionstate.State

// Session event loop.
//
// In session mode the runner owns its loop: one goroutine observes provider
// messages, user input from the ingress, the coordinator's Inbox, and the
// held-onset timer. Every barge-in decision is made on this goroutine between
// observing the provider and sending to it, which is what lets a cancel
// precede the interrupting audio on the wire.

func (r *ModelRunner) runSession(ctx context.Context) (err error) {
	r.ingress.stop.start()
	defer r.ingress.stop.stop()
	session, err := r.sessionInferencer.ConnectSession(ctx)
	if err != nil {
		return fmt.Errorf("session connect: %w", err)
	}
	defer func() { err = joinOnFailure(err, session.Close()) }()
	// Deltas the session produced reach DeltaOutbox before Run returns.
	defer func() { r.sessionOut.flush(ctx) }()

	state := sessionRunState{}
	for {
		// Observe already-queued provider lifecycle messages before admitting
		// pending user input. In particular, MESSAGE.END is authoritative for
		// the response that just completed; peer audio queued in the same
		// scheduling step must not cause a late RESPONSE.CANCEL for it. Once the
		// provider queue is empty, preserve the deterministic user-input turn so
		// a scheduled audio frame remains ordered before its own commit and
		// response.create boundary.
		handled, inputErr := r.forwardPendingSessionInputs(ctx, session, &state)
		if inputErr != nil {
			return r.endSession(ctx, &state, inputErr)
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
	case input := <-r.ingress.ordered:
		if err := r.forwardSessionInput(ctx, session, state, input); err != nil {
			return true, r.endSession(ctx, state, err)
		}
	case evt := <-r.ingress.priority:
		r.forwardPendingSessionMessages(ctx, session, state)
		r.forwardQueuedSessionEvent(ctx, session, state, evt)
	case req, ok := <-r.Inbox.Chan():
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		// After the provider's SESSION.CLOSE no response can follow, so a late
		// request must not reach the closed wire.
		if state.Session != sessionstate.SessionClosed {
			r.sendLatestUserText(ctx, session, req)
		}
	case <-state.Onset.Expiry():
		// Onset can no longer be reached: release the held frames.
		r.flushHeldAudio(ctx, session, state)
	case msg, ok := <-session.Receive().Chan():
		if !ok {
			return true, r.endSession(ctx, state, nil)
		}
		r.forwardSessionMessageState(ctx, session, state, msg)
	}
	return false, nil
}

// forwardPendingSessionInputs first drains provider messages that are already
// queued, so an observed response terminal boundary wins over queued peer
// audio, and forwards any priority cancel. It then gives queued user input a
// deterministic transport turn. It reports whether it forwarded anything.
func (r *ModelRunner) forwardPendingSessionInputs(ctx context.Context, session messages.Session, state *sessionRunState) (bool, error) {
	handled := false
	for {
		if r.forwardPendingSessionMessagesAndCancels(ctx, session, state) {
			handled = true
			continue
		}
		select {
		case input := <-r.ingress.ordered:
			if err := r.forwardSessionInput(ctx, session, state, input); err != nil {
				return true, err
			}
			handled = true
		default:
			return handled, nil
		}
	}
}

// forwardPendingSessionMessagesAndCancels observes queued provider messages,
// then forwards any priority cancel before queued user input is admitted.
func (r *ModelRunner) forwardPendingSessionMessagesAndCancels(ctx context.Context, session messages.Session, state *sessionRunState) bool {
	if r.forwardPendingSessionMessages(ctx, session, state) {
		return true
	}
	select {
	case evt := <-r.ingress.priority:
		r.forwardQueuedSessionEvent(ctx, session, state, evt)
		return true
	default:
		return false
	}
}

// forwardSessionInput dispatches one ordered ingress input.
func (r *ModelRunner) forwardSessionInput(ctx context.Context, session messages.Session, state *sessionRunState, input SessionInput) error {
	r.ingress.consumed(input)
	switch input.kind {
	case sessionInputAudio:
		// The provider may have queued its terminal boundary after the last
		// observation; see it before evaluating barge-in state.
		r.forwardPendingSessionMessages(ctx, session, state)
		return r.forwardSessionAudio(ctx, session, state, input.audio)
	case sessionInputEvent:
		r.forwardQueuedSessionEvent(ctx, session, state, input.event) // orders held audio around the event
	case sessionInputMessage:
		r.flushHeldAudio(ctx, session, state)
		return forwardSessionCompleteMessage(ctx, session, input.message, input.requestResponse)
	}
	return nil
}

func (r *ModelRunner) forwardPendingSessionMessages(ctx context.Context, session messages.Session, state *sessionRunState) (handled bool) {
	for {
		msg, ok := session.Receive().Read()
		if !ok {
			return handled
		}
		r.forwardSessionMessageState(ctx, session, state, msg)
		handled = true
	}
}

// forwardSessionMessageState forwards one provider message and then applies
// the work its lifecycle effects released: held onset audio, deferred control
// events, and deferred tool-batch failures.
func (r *ModelRunner) forwardSessionMessageState(ctx context.Context, session messages.Session, state *sessionRunState, msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeSessionClose {
		r.flushPendingSessionSendErrors(ctx, state.ToolBatch.TakeFailures())
		state.Continuation.Reset()
		r.sessionToolContinuation = sessionToolContinuationNone
	}
	messageEnded := r.forwardSessionMessageWithState(ctx, session, msg, state)
	ackEnded := state.Ack.TakeEnded()
	continuationDone := state.Continuation.TakeEnded()
	if messageEnded {
		// Held onset audio has nothing left to interrupt.
		r.flushHeldAudio(ctx, session, state)
	}
	if ackEnded || messageEnded {
		// Either this response's own terminal boundary was just observed, or
		// an outstanding acknowledgement was just finalized (possibly by a
		// replacement response retiring it before its own MESSAGE.END could
		// be owned). Either way, the response that deferred events were
		// waiting on is no longer active, so it is now safe to replay them.
		r.flushDeferredSessionEvents(ctx, session, state)
	}
	if continuationDone {
		r.flushPendingSessionSendErrors(ctx, state.ToolBatch.TakeFailures())
	}
}

// forwardSessionMessageWithState applies one provider message to the
// response lifecycle and forwards it to DeltaOutbox unless it is stale. It
// reports whether the message ended the current response.
func (r *ModelRunner) forwardSessionMessageWithState(ctx context.Context, session messages.Session, msg messages.StreamMessage, state *sessionRunState) bool {
	if rejectsActiveResponseCreate(msg) && !state.Ack.Outstanding() {
		state.Continuation.Rejected()
	}
	acknowledgementResponse := r.tagSessionAcknowledgement(ctx, state, &msg)
	msgID := sessionstate.ResponseID(msg.ResponseID)
	messageEndOwned := false

	// Track the provider response lifecycle for barge-in detection. A response
	// is live from MESSAGE.START through MESSAGE.END; audio start/end alone do
	// not define its terminal boundary. When a provider starts a replacement
	// response before the older one has drained, the older response is retired
	// and can no longer mutate the current lifecycle.
	switch msg.Type { //nolint:exhaustive // Only lifecycle boundaries change state; every other type is tagged in default.
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		state.StartResponse(msgID, acknowledgementResponse, msg.ResponsePurpose == messages.ResponsePurposeToolContinuation)
		state.TagContinuation(&msg, msgID)
	case messages.StreamTypeMessageEnd:
		state.TagContinuation(&msg, msgID)
		messageEndOwned = state.EndResponse(&msg, msgID, acknowledgementResponse)
	case messages.StreamTypeSessionClose:
		state.Session = sessionstate.SessionClosed
		msg = normalizeSessionCloseMessage(msg)
	default:
		state.TagContinuation(&msg, msgID)
	}
	// A provider may have already queued output when RESPONSE.CANCEL reaches
	// it. The wire adapter cannot retract those frames, but they must not cross
	// the customer-facing session boundary after the local cancellation. Keep
	// MESSAGE.END so the cancelled response can still close and the next turn
	// can be admitted. An identified event is admitted only for its current
	// response owner; an old terminal event cannot clear a replacement.
	if state.StaleCustomerOutput(msg) {
		return messageEndOwned
	}
	if isOutputDelta(msg) {
		state.Response.HasOutput = true
	}
	r.forwardInitialSessionConfig(ctx, session, state, msg)
	r.writeSessionDelta(ctx, msg)
	return messageEndOwned
}

func providerSentInitialSessionConfig(session messages.Session) bool {
	marker, ok := session.(messages.SessionInitialConfigMarker)
	return ok && marker.InitialSessionConfigSent()
}

// forwardInitialSessionConfig sends the configured SESSION.UPDATE once.
// Providers may emit both SESSION.OPEN and SESSION.CREATED for one
// connection; the first lifecycle event owns the initial configuration.
func (r *ModelRunner) forwardInitialSessionConfig(ctx context.Context, session messages.Session, state *sessionRunState, msg messages.StreamMessage) {
	if msg.Type != messages.StreamTypeSessionOpen && msg.Type != messages.StreamTypeSessionCreated {
		return
	}
	if !state.Session.ObserveOpen() || r.sessionConfig == nil || providerSentInitialSessionConfig(session) {
		return
	}
	r.forwardSessionEvent(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdate,
		Value: messages.NewSessionUpdateValue(r.sessionConfig),
	})
}

// endSession finishes runSession: a live-context failure is published before
// deferred send failures are flushed, and err is returned unchanged.
func (r *ModelRunner) endSession(ctx context.Context, state *sessionRunState, err error) error {
	if err != nil && ctx.Err() == nil {
		// The terminal failure must follow every queued delta.
		r.sessionOut.flush(ctx)
		r.publishSessionAudioFailure(err, state.Response.HasOutput)
	}
	r.flushPendingSessionSendErrors(ctx, state.ToolBatch.TakeFailures())
	return err
}

// finishClosedSession drains the provider's final queued messages and, unless
// the provider already reported its own close, emits the terminal SESSION.CLOSE.
func (r *ModelRunner) finishClosedSession(ctx context.Context, session messages.Session, state *sessionRunState) {
	r.forwardPendingSessionMessages(ctx, session, state)
	r.flushPendingSessionSendErrors(ctx, state.ToolBatch.TakeFailures())
	if state.Session == sessionstate.SessionClosed {
		return
	}
	terminalProvenance := messages.TerminalProvenanceProvider
	terminalOutputState := outputState(state.Response.HasOutput)
	if state.Response.Completed() {
		// Preserve the existing session teardown contract after a
		// completed response. A transport close before any response
		// boundary remains provider-authored and uses observed output.
		terminalProvenance = messages.TerminalProvenanceSession
		terminalOutputState = messages.TerminalOutputNotApplicable
	}
	r.writeSessionDelta(ctx, messages.StreamMessage{
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

// tagSessionAcknowledgement marks the provider output that belongs to an
// outstanding acknowledgement request and reports whether msg belongs to it.
// Ordinary response requests are held while an acknowledgement is
// outstanding, so an active-response rejection in that window rejects the
// acknowledgement. The provider may already have started its own response,
// which was attributed to the acknowledgement; the rejection reclassifies it
// by re-announcing its start without the acknowledgement purpose, so its
// later output keeps ordinary accounting.
func (r *ModelRunner) tagSessionAcknowledgement(ctx context.Context, state *sessionRunState, msg *messages.StreamMessage) bool {
	ack := &state.Ack
	if msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		ack.ObserveTagged()
	} else if ack.Outstanding() && ack.Start == nil && isSessionResponseStart(msg.Type) {
		start := *msg
		ack.Start = &start
	}
	if rejectsActiveResponseCreate(*msg) && ack.Outstanding() {
		ack.Reject()
		if start := ack.Start; start != nil && start.ResponseID == state.Response.ID {
			r.writeSessionDelta(ctx, *start)
		}
	}
	if !ack.Outstanding() {
		ack.Start = nil
		return false
	}
	if sessionstate.IsResponseStreamType(msg.Type) {
		msg.ResponsePurpose = messages.ResponsePurposeToolAcknowledgement
	}
	return true
}

func isSessionResponseStart(kind messages.StreamMessageType) bool {
	return kind == messages.StreamTypeMessageStart || kind == messages.StreamTypeAudioStart
}

func rejectsActiveResponseCreate(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.ErrorValue)
	return ok && value.IsNonTerminal() && value.Classification == messages.ErrorClassificationResponseCreateActive
}

// normalizeSessionCloseMessage fills the terminal fields of a provider
// SESSION.CLOSE that omitted them.
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
