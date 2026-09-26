package participants

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Session response lifecycle: response identity, retirement, stale output,
// and tool-continuation response scoping.
//
// Barge-in decision (OpenAI Realtime semantics): when the user speaks over a
// response that is playing while a tool continuation is pending, that response
// is cancelled (RESPONSE.CANCEL; the host stops and truncates the unplayed
// audio). The accepted tool result stays in the provider conversation --
// response.cancel never removes conversation items -- and the continuation is
// still requested once the cancelled response has ended, because the tool
// obligation is only resolved by the continuation response. Only that
// continuation response is exempt from local barge-in.

func isSessionContinuationCreate(evt messages.StreamMessage) bool {
	return evt.Type == messages.StreamTypeResponseCreate && !isToolAcknowledgementResponseCreate(evt)
}

func clearSessionContinuation(state *sessionRunState) {
	state.continuationRequested = false
	state.continuationInFlight = false
	state.continuationResponseID = ""
	state.continuationEnded = false
	state.continuationCreate = messages.StreamMessage{}
}

// deferSessionResponseRequest reports whether evt, which may ask the provider
// for a new response, must wait for the active response's terminal boundary.
// A tool continuation is never requested over an active response: the
// provider rejects a second active response, and the response that is playing
// must stay interruptible. Deferred, it binds to the next response opened.
func deferSessionResponseRequest(state *sessionRunState, evt messages.StreamMessage) bool {
	requestsNewResponse := evt.Type == messages.StreamTypeMessageEnd || isSessionContinuationCreate(evt)
	if requestsNewResponse && state.responseCancelSent && (state.responseInFlight || state.acknowledgementOutstanding) {
		return true
	}
	return isSessionContinuationCreate(evt) && state.responseInFlight
}

// tagSessionContinuation marks provider output that belongs to the bound
// continuation response so downstream tool-obligation accounting resolves the
// obligation against that response and never against an unrelated one.
func tagSessionContinuation(state *sessionRunState, msg *messages.StreamMessage, msgID string) {
	if !state.continuationInFlight || !isSessionResponseStreamType(msg.Type) || msg.ResponsePurpose != "" {
		return
	}
	if msgID != "" && msgID != state.continuationResponseID {
		return
	}
	msg.ResponsePurpose = messages.ResponsePurposeToolContinuation
}

// retrySessionContinuationOnRejection re-requests a continuation the provider
// rejected because another response became active first. Without the retry
// the continuation would be lost and its tool obligation left unresolved. The
// provider-owned response it collided with is not the continuation and stays
// interruptible.
func (r *ModelRunner) retrySessionContinuationOnRejection(ctx context.Context, session messages.Session, state *sessionRunState, msg messages.StreamMessage) {
	if !rejectsActiveResponseCreate(msg) || state.acknowledgementOutstanding {
		return
	}
	switch {
	case state.continuationRequested:
		state.continuationRequested = false
	case state.continuationInFlight:
		// The provider's own response started before the rejection and was
		// bound as the continuation; release that binding.
		state.continuationInFlight = false
		state.continuationResponseID = ""
	default:
		return
	}
	evt := state.continuationCreate
	if evt.Type == "" {
		evt = messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}
	}
	r.markSessionToolEventQueued(evt)
	if state.responseInFlight {
		state.deferredSessionEvents = append(state.deferredSessionEvents, evt)
		return
	}
	r.forwardQueuedSessionEvent(ctx, session, state, evt)
}

func beginSessionResponse(state *sessionResponseState, msgID string) bool {
	if msgID != "" {
		if _, retired := state.retiredResponseIDs[msgID]; retired {
			return false
		}
		if _, terminal := state.terminalResponseIDs[msgID]; terminal {
			return false
		}
	}
	if state.responseInFlight {
		if state.currentResponseID == msgID {
			// Duplicate starts for the same response must not reset cancellation
			// or output state.
			return false
		}
		if state.currentResponseID != "" && msgID == "" {
			// An untagged start cannot claim an identified response.
			return false
		}
		if state.currentResponseID != "" && msgID != state.currentResponseID {
			retireSessionResponse(state)
		}
	}
	state.currentResponseID = msgID
	return true
}

func ownsSessionResponseEnd(state *sessionResponseState, msgID string) bool {
	if msgID != "" {
		if _, terminal := state.terminalResponseIDs[msgID]; terminal {
			return false
		}
		if _, retired := state.retiredResponseIDs[msgID]; retired {
			return false
		}
		if _, cancelled := state.cancelledResponseIDs[msgID]; cancelled && state.currentResponseID != msgID {
			return false
		}
		if state.responseInFlight {
			return msgID == "" || state.currentResponseID == msgID
		}
		// A response.done without response.created is accepted once for
		// compatibility with providers that omit the opening event.
		return !state.responseCompleted
	}
	if state.responseInFlight {
		// Compatible providers may omit response_id on response.done. The
		// sole active identified response owns that terminal event unless a
		// non-empty competing ID is supplied.
		return true
	}
	return !state.responseCompleted
}

func staleSessionCustomerOutput(state *sessionResponseState, msg messages.StreamMessage) bool {
	if !isCustomerOutputDelta(msg) {
		return false
	}
	msgID := responseID(msg.ResponseID)
	if msgID != "" {
		if _, cancelled := state.cancelledResponseIDs[msgID]; cancelled {
			return true
		}
		if _, retired := state.retiredResponseIDs[msgID]; retired {
			return true
		}
		if _, terminal := state.terminalResponseIDs[msgID]; terminal {
			return true
		}
		if state.currentResponseID != "" && state.currentResponseID != msgID {
			return true
		}
		return false
	}
	return state.responseCancelSent
}

// retireSessionResponse retires the current response when a replacement
// response starts before its terminal boundary was observed.
func retireSessionResponse(state *sessionResponseState) {
	state.retiredResponseIDs[state.currentResponseID] = struct{}{}
	// The retired response's own MESSAGE.END, whenever it eventually
	// arrives, will never be "owned" again (ownsSessionResponseEnd
	// rejects retired ids), so the reset that normally happens there
	// would never run. Finalize any acknowledgement bookkeeping for it
	// here instead of leaving acknowledgementOutstanding stuck true:
	// left stuck, every later non-silent audio frame would look like a
	// live barge-in target (state.responseInFlight || state.
	// acknowledgementOutstanding) even once nothing is actually
	// active, producing a RESPONSE.CANCEL the provider rejects with
	// response_cancel_not_active.
	if state.acknowledgementOutstanding {
		state.acknowledgementOutstanding = false
		state.acknowledgementCancelled = false
		state.acknowledgementEnded = true
	}
	if state.continuationInFlight {
		// A replacement response retired the continuation before its
		// terminal boundary; the replacement is not the continuation.
		state.continuationInFlight = false
		state.continuationResponseID = ""
		state.continuationEnded = true
	}
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

func hasPCM16Signal(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}
