package participants

import (
	"context"
	"slices"
	"sync/atomic"

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
	value, ok := evt.Value.(*messages.ResponseCreateValue)
	return evt.Type == messages.StreamTypeResponseCreate && ok && value.IsToolContinuation()
}

func clearSessionContinuation(state *sessionRunState) {
	state.responseRequests = responseRequestQueue{}
	state.continuationInFlight = false
	state.continuationResponseID = ""
	state.continuationEnded = false
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

func beginSessionResponse(state *sessionResponseState, msgID string) bool {
	if msgID != "" {
		if state.retiredResponseIDs.has(msgID) {
			return false
		}
		if state.terminalResponseIDs.has(msgID) {
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
		if state.terminalResponseIDs.has(msgID) {
			return false
		}
		if state.retiredResponseIDs.has(msgID) {
			return false
		}
		if state.cancelledResponseIDs.has(msgID) && state.currentResponseID != msgID {
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
		if state.cancelledResponseIDs.has(msgID) {
			return true
		}
		if state.retiredResponseIDs.has(msgID) {
			return true
		}
		if state.terminalResponseIDs.has(msgID) {
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
	state.retiredResponseIDs.add(state.currentResponseID)
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

func isSessionToolEvent(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeToolCallEnd || msg.Type == messages.StreamTypeResponseCreate
}

func (r *ModelRunner) markSessionToolEventQueued(msg messages.StreamMessage) {
	if !isSessionToolEvent(msg) {
		return
	}
	r.sessionToolEventMu.Lock()
	r.pendingSessionToolEvents++
	r.sessionToolEventMu.Unlock()
}

func (r *ModelRunner) markSessionToolEventConsumed(msg messages.StreamMessage) {
	if !isSessionToolEvent(msg) {
		return
	}
	r.sessionToolEventMu.Lock()
	if r.pendingSessionToolEvents > 0 {
		r.pendingSessionToolEvents--
	}
	r.sessionToolEventMu.Unlock()
}

func (r *ModelRunner) hasPendingSessionToolEvents() bool {
	r.sessionToolEventMu.Lock()
	defer r.sessionToolEventMu.Unlock()
	return r.pendingSessionToolEvents > 0
}

// sessionCancelLaneCapacity bounds interrupts waiting for the runner; a full
// lane falls back to the ordered ingress.
const sessionCancelLaneCapacity = 4

// sessionCancelLane gives an explicit RESPONSE.CANCEL priority over bulk audio
// already waiting in the ordered session ingress, so an interrupt is not
// delayed by (or parked behind a waiting admission of) queued audio frames.
// A cancel never overtakes a queued control input: while one is queued the
// cancel keeps its FIFO position relative to that turn boundary.
type sessionCancelLane struct {
	inbox          chan messages.StreamMessage
	queuedControls atomic.Int64
}

func (r *ModelRunner) admitPriorityCancel(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeResponseCancel || r.cancelLane.inbox == nil ||
		r.cancelLane.queuedControls.Load() > 0 || r.ingressStop.stopped() {
		return false
	}
	select {
	case r.cancelLane.inbox <- msg:
		return true
	default:
		return false
	}
}

// forwardPendingSessionMessagesAndCancels observes queued provider messages,
// then forwards any priority cancel before queued user input is admitted.
func (r *ModelRunner) forwardPendingSessionMessagesAndCancels(ctx context.Context, session messages.Session, state *sessionRunState) bool {
	if r.forwardPendingSessionMessages(ctx, session, state) {
		return true
	}
	select {
	case evt := <-r.cancelLane.inbox:
		r.forwardQueuedSessionEvent(ctx, session, state, evt)
		return true
	default:
		return false
	}
}

// responseIDRetention bounds each response identity set. Only recent
// responses can still deliver a late event; a provider never interleaves
// output from dozens of responses back.
const responseIDRetention = 64

// responseIDSet remembers the most recent response identities, evicting the
// oldest so session bookkeeping does not grow with session length.
type responseIDSet struct {
	ids   map[string]struct{}
	order []string
}

func (s *responseIDSet) add(id string) {
	if id == "" || s.has(id) {
		return
	}
	if s.ids == nil {
		s.ids = make(map[string]struct{}, responseIDRetention+1)
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > responseIDRetention {
		delete(s.ids, s.order[0])
		s.order = append(s.order[:0], s.order[1:]...)
	}
}

func (s *responseIDSet) has(id string) bool {
	_, ok := s.ids[id]
	return ok
}

func (s *responseIDSet) len() int { return len(s.ids) }

// responseRequest is the kind of request that asked the provider for a
// response.
type responseRequest uint8

const (
	requestNone responseRequest = iota
	requestUserTurn
	requestContinuation
	requestOther
)

// maxPendingResponseRequests bounds the queue; a request the provider never
// answers must not accumulate for the whole session.
const maxPendingResponseRequests = 16

// responseRequestQueue binds each response the provider opens to the request
// that asked for it, so only the response opened for a tool continuation is
// treated as that continuation. The provider answers requests in the order it
// received them; a response it opens on its own (server VAD) has no request.
// When the provider rejects a request because such a response was already
// active, the response that consumed the request was that provider-owned one:
// the request goes back to the head of the queue, and the provider adapter --
// the single owner of that retry -- requests it again once the colliding
// response ends.
type responseRequestQueue struct {
	pending []responseRequest
	// opened is the request consumed by the current response, or requestNone
	// when that response was unrequested or has ended.
	opened responseRequest
}

func requestForCreate(evt messages.StreamMessage) responseRequest {
	if isSessionContinuationCreate(evt) {
		return requestContinuation
	}
	return requestOther
}

func (q *responseRequestQueue) push(request responseRequest) {
	q.pending = append(q.pending, request)
	if len(q.pending) > maxPendingResponseRequests {
		q.pending = append(q.pending[:0], q.pending[1:]...)
	}
}

func (q *responseRequestQueue) has(request responseRequest) bool {
	return slices.Contains(q.pending, request)
}

// open binds a newly opened response to the oldest pending request.
func (q *responseRequestQueue) open() responseRequest {
	q.opened = requestNone
	if len(q.pending) > 0 {
		q.opened = q.pending[0]
		q.pending = append(q.pending[:0], q.pending[1:]...)
	}
	return q.opened
}

// ended records that the current response reached its terminal boundary.
func (q *responseRequestQueue) ended() { q.opened = requestNone }

// rejected handles a provider rejection of a response request because another
// response was active.
func (q *responseRequestQueue) rejected(state *sessionRunState) {
	if q.opened == requestNone {
		return // the rejected request is still pending; the adapter retries it
	}
	q.pending = append([]responseRequest{q.opened}, q.pending...)
	if q.opened == requestContinuation && state.continuationInFlight {
		// The provider's own response was bound as the continuation.
		state.continuationInFlight = false
		state.continuationResponseID = ""
	}
	q.opened = requestNone
}
