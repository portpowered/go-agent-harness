package openai

// This file owns the adapter's explicit response state: the provider's single
// response slot, the function-call turn, the single response.create retry and
// the queued response intents, plus the provider lifecycle events that move
// them.
import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// responseSlot is the provider's single active-response slot as the adapter
// sees it.
type responseSlot uint8

const (
	// responseSlotIdle: no response holds the slot.
	responseSlotIdle responseSlot = iota
	// responseSlotActive: a response holds the slot, reserved locally by a
	// dispatched response.create or confirmed by response.created.
	responseSlotActive
	// responseSlotFunctionCall: the active response emitted a function_call
	// item, so its continuation must come from the tool result.
	responseSlotFunctionCall
)

// toolTurn tracks whether a standalone response.create is stale because a
// function call's tool result owns the continuation.
type toolTurn uint8

const (
	// toolTurnNone: no function call constrains response requests.
	toolTurnNone toolTurn = iota
	// toolTurnAwaitingResult: a function_call item was seen and its tool
	// result is not admitted; standalone response requests are stale.
	toolTurnAwaitingResult
	// toolTurnResultAdmitted: the tool result is admitted under suppression;
	// the next standalone response request is its continuation.
	toolTurnResultAdmitted
	// toolTurnResultUnsuppressed: a tool result was admitted while no
	// suppression was in force (after its continuation, or before the
	// provider's function_call item); it keeps a function-call response's
	// standalone requests from being treated as stale.
	toolTurnResultUnsuppressed
)

// resultAdmitted reports whether a tool result was admitted since the last
// function_call item.
func (t toolTurn) resultAdmitted() bool {
	return t == toolTurnResultAdmitted || t == toolTurnResultUnsuppressed
}

// retryPhase is the single-retry ownership of the last reserving
// response.create: the adapter, not the runner, resends a request the
// provider rejected because another response was still active.
type retryPhase uint8

const (
	// retryNone: no request is remembered.
	retryNone retryPhase = iota
	// retryRemembered: the request is dispatched but not yet written.
	retryRemembered
	// retrySent: the request reached the provider.
	retrySent
	// retryArmed: the provider rejected the sent request because a response
	// was active; it is resent once that response ends.
	retryArmed
)

// responseState is guarded by realtimeSession.responseMu.
type responseState struct {
	slot responseSlot
	// id is the active response's provider id; empty while only reserved.
	id   string
	tool toolTurn

	retry      retryPhase
	retryEvent models.SessionEvent

	// generation increments when a cancel or failed dispatch invalidates
	// queued work.
	generation uint64
	// inflight is the intent being written to the wire under
	// responseWireMu; nil when no dispatch is in progress.
	inflight *responseIntent
	pending  []responseIntent
}

// busy reports whether a new intent must queue behind other response work.
func (st *responseState) busy() bool {
	return st.busyExceptQueue() || len(st.pending) > 0
}

// busyExceptQueue reports whether a response holds the slot or a dispatch is
// writing.
func (st *responseState) busyExceptQueue() bool {
	return st.slot != responseSlotIdle || st.inflight != nil
}

// beginDispatch marks intent in flight and, when it reserves a response,
// takes the slot and remembers its request for the single retry.
func (st *responseState) beginDispatch(intent *responseIntent, create models.SessionEvent, reservesResponse bool) {
	st.inflight = intent
	if !reservesResponse {
		return
	}
	if st.slot == responseSlotIdle {
		st.slot = responseSlotActive
	}
	st.retry, st.retryEvent = retryRemembered, create
}

// releaseSlot frees the slot locally and ends its function-call turn.
func (st *responseState) releaseSlot() {
	if st.slot == responseSlotIdle {
		return
	}
	st.endResponse()
	st.tool = toolTurnNone
}

// endResponse frees the slot after the provider finished the response.
func (st *responseState) endResponse() {
	st.slot = responseSlotIdle
	st.id = ""
}

func (st *responseState) forgetRetry() {
	st.retry, st.retryEvent = retryNone, models.SessionEvent{}
}

// markToolResultAdmission records an admitted tool result and ends the
// function-call turn once its continuation is admitted.
func (st *responseState) markToolResultAdmission(hasFunctionCallOutput, continuesToolTurn bool) {
	if hasFunctionCallOutput {
		switch st.tool {
		case toolTurnNone:
			st.tool = toolTurnResultUnsuppressed
		case toolTurnAwaitingResult:
			st.tool = toolTurnResultAdmitted
		case toolTurnResultAdmitted, toolTurnResultUnsuppressed:
		}
	}
	if continuesToolTurn {
		st.tool = toolTurnNone
	}
}

// queue places intent behind the active response. Tool results must precede
// any continuation request already queued for the same response: existing
// tool results keep arrival order, then this result is placed before
// user/audio response intents.
func (st *responseState) queue(intent responseIntent, hasFunctionCallOutput bool) {
	if !hasFunctionCallOutput {
		st.pending = append(st.pending, intent)
		return
	}
	st.dropDeferredAudioResponses()
	insertAt := len(st.pending)
	for index, pending := range st.pending {
		if !responseIntentHasFunctionCallOutput(pending) {
			insertAt = index
			break
		}
	}
	st.pending = append(st.pending, responseIntent{})
	copy(st.pending[insertAt+1:], st.pending[insertAt:])
	st.pending[insertAt] = intent
}

// popPending removes the next dispatchable intent. While a tool result is
// awaited only tool-result intents may go first.
func (st *responseState) popPending() (responseIntent, bool) {
	intentIndex := 0
	if st.tool == toolTurnAwaitingResult {
		intentIndex = -1
		for index, pending := range st.pending {
			if responseIntentHasFunctionCallOutput(pending) {
				intentIndex = index
				break
			}
		}
		if intentIndex < 0 {
			return responseIntent{}, false
		}
	}
	intent := st.pending[intentIndex]
	copy(st.pending[intentIndex:], st.pending[intentIndex+1:])
	st.pending = st.pending[:len(st.pending)-1]
	return intent, true
}

// dropStandaloneResponses retires queued standalone default response
// requests; the other events of their user turn keep their queue position.
func (st *responseState) dropStandaloneResponses() {
	if len(st.pending) == 0 {
		return
	}
	kept := st.pending[:0]
	for _, intent := range st.pending {
		if !standaloneDefaultResponseIntent(intent) || responseIntentHasAudioCommit(intent) {
			kept = append(kept, intent)
			continue
		}
		if events := withoutDefaultResponseCreate(intent.events); len(events) > 0 {
			intent.events = events
			kept = append(kept, intent)
		}
	}
	st.pending = kept
}

func (st *responseState) dropDeferredAudioResponses() {
	if len(st.pending) == 0 {
		return
	}
	kept := st.pending[:0]
	for _, intent := range st.pending {
		if !intent.deferredAudioResponse {
			kept = append(kept, intent)
		}
	}
	st.pending = kept
}

// keepToolWork drops the queued response intents a RESPONSE.CANCEL
// invalidates and keeps tool work: a tool result or a tool continuation
// request queued behind the cancelled response is not work of that response.
// Dropping it would leave the tool obligation unresolved and the runner
// waiting for a continuation that never opens. Kept intents join the new
// generation and dispatch once the cancelled response ends.
func (st *responseState) keepToolWork(pending []responseIntent) []responseIntent {
	kept := pending[:0]
	for _, intent := range pending {
		if !responseIntentIsToolWork(intent) {
			settleResponseIntent(intent, messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled})
			continue
		}
		intent.generation = st.generation
		kept = append(kept, intent)
	}
	return kept
}

// keepContinuationRetry keeps a tool continuation's retry state across a
// cancel. A continuation rejected because another response was active (or
// sent and not yet answered) is retried when that response ends; the cancel
// ends that response, it does not answer the continuation. Any other
// remembered request belongs to the cancelled generation and is forgotten.
func (st *responseState) keepContinuationRetry() {
	if st.retry != retryNone && responseEventIsToolContinuation(st.retryEvent) {
		return
	}
	st.forgetRetry()
}

// releaseResponseAdmission clears a local reservation the provider no longer
// holds, ending its function-call turn.
func (s *realtimeSession) releaseResponseAdmission() {
	s.responseMu.Lock()
	s.response.releaseSlot()
	s.responseMu.Unlock()
}

// markResponseRequestSent records that a remembered response.create reached
// the provider, which arms its single retry on a later active-response error.
func (s *realtimeSession) markResponseRequestSent(event models.SessionEvent) {
	if event.Type != models.SessionEventResponseCreate || realtimeResponseCreateIsOutOfBand(event) {
		return
	}
	s.responseMu.Lock()
	if s.response.retry == retryRemembered {
		s.response.retry = retrySent
	}
	s.responseMu.Unlock()
}

func (s *realtimeSession) observeResponseLifecycle(event models.SessionEvent) {
	switch event.Type { //nolint:exhaustive // Only response lifecycle and error events move the response state.
	case models.SessionEventResponseCreated:
		s.observeResponseCreated(event)
	case models.SessionEventResponseDone:
		s.observeResponseDone(event)
	case models.SessionEventResponseOutputItemAdded:
		if firstStringField(event.Data, "item.type") == "function_call" {
			s.observeFunctionCallItem()
		}
	case models.SessionEventError:
		s.observeResponseCancelRejection(event)
		s.observeResponseCreateActiveError(event)
	default:
	}
}

// observeFunctionCallItem records that the active response chose a function
// call. It supersedes any standalone response request queued for the same
// audio turn; combined tool-result intents, the continuation needed to
// complete this call, are kept.
//
// An idle slot stays idle: it is idle here only after a local release or for
// an out-of-band response, so no tracked response.done would ever free a
// responseSlotFunctionCall mark, and every later intent would park. The tool
// turn alone keeps standalone requests stale until the result is admitted.
func (s *realtimeSession) observeFunctionCallItem() {
	s.responseMu.Lock()
	st := &s.response
	if st.slot == responseSlotActive {
		st.slot = responseSlotFunctionCall
	}
	st.tool = toolTurnAwaitingResult
	st.forgetRetry()
	st.dropStandaloneResponses()
	s.responseMu.Unlock()
}

func (s *realtimeSession) observeResponseCreated(event models.SessionEvent) {
	if realtimeResponseCreatedIsOutOfBand(event) {
		return
	}
	responseID := firstStringField(event.Data, "response_id", "response.id")
	s.responseMu.Lock()
	st := &s.response
	if st.slot == responseSlotIdle {
		// A response the adapter did not reserve lifts function-call
		// suppression; an admitted tool result stays recorded.
		switch st.tool {
		case toolTurnAwaitingResult:
			st.tool = toolTurnNone
		case toolTurnResultAdmitted:
			st.tool = toolTurnResultUnsuppressed
		case toolTurnNone, toolTurnResultUnsuppressed:
		}
	}
	// A provider may start a replacement response after barge-in while a
	// standalone response.create for the interrupted turn is still queued.
	// That request is stale: dispatching it after this response completes would
	// reserve the provider's single response slot ahead of the replacement's
	// function_call_output, potentially blocking the real continuation forever.
	// Preserve combined tool-result intents; only retire standalone default
	// response requests that were waiting for the superseded response.
	if st.slot != responseSlotIdle && st.id != "" && responseID != "" && st.id != responseID {
		st.dropStandaloneResponses()
	}
	st.slot = responseSlotActive
	st.id = responseID
	s.responseMu.Unlock()
}

func (s *realtimeSession) observeResponseDone(event models.SessionEvent) {
	if realtimeResponseDoneIsOutOfBand(event) {
		return
	}
	doneID := firstStringField(event.Data, "response_id", "response.id")
	s.responseMu.Lock()
	st := &s.response
	// Ignore a done that does not name the active response: either side
	// lacking an id while the other has one, or two different ids.
	if st.slot == responseSlotIdle || st.id != doneID {
		s.responseMu.Unlock()
		return
	}
	if st.retry == retryArmed {
		// The single retry: resend the rejected request ahead of queued work
		// now that the blocking response ended.
		retry := responseIntent{events: []models.SessionEvent{st.retryEvent}, generation: st.generation}
		st.pending = append([]responseIntent{retry}, st.pending...)
	}
	st.endResponse()
	st.forgetRetry()
	s.responseMu.Unlock()
	s.signalResponseIntentWorker()
}

func (s *realtimeSession) observeResponseCreateActiveError(event models.SessionEvent) {
	if event.Type != models.SessionEventError ||
		firstStringField(event.Data, "error.type") != realtimeInvalidRequestErrorType ||
		firstStringField(event.Data, "error.code") != realtimeResponseCreateActiveCode {
		return
	}
	s.responseMu.Lock()
	st := &s.response
	if st.slot != responseSlotIdle && (st.retry == retrySent || st.retry == retryArmed) {
		st.retry = retryArmed
	}
	s.responseMu.Unlock()
}

func (s *realtimeSession) observeResponseCancelRejection(event models.SessionEvent) {
	if event.Type != models.SessionEventError ||
		firstStringField(event.Data, "error.type") != realtimeInvalidRequestErrorType ||
		firstStringField(event.Data, "error.code") != realtimeResponseCancelNotActiveCode {
		return
	}
	// The provider says there is no active response. Clear a stale local
	// reservation so a queued continuation cannot remain blocked forever.
	s.releaseResponseAdmission()
}
