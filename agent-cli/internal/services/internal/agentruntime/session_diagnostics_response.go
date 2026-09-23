package agentruntime

import (
	"context"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sd "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/lifecycle"
	sdw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
)

type scheduledAudioResponseDisposition = sd.Disposition

const (
	scheduledAudioResponsePending   = sd.DispositionPending
	scheduledAudioResponseCompleted = sd.DispositionCompleted
	scheduledAudioResponseCancelled = sd.DispositionCancelled
)

func (o *sessionProgressObserver) ensureLifecycle() sd.Service {
	if o == nil {
		return nil
	}
	if o.lifecycle == nil {
		o.lifecycle = sdw.NewLifecycleService()
	}
	return o.lifecycle
}
func lifecycleTerminal(value *messages.MessageEndValue) *sd.Terminal {
	if value == nil {
		return nil
	}
	return &sd.Terminal{
		Status:               sanitizeContinuationDetail(value.Status),
		ErrorCode:            sanitizeContinuationDetail(value.ProviderErrorCode),
		ErrorMessage:         sanitizeContinuationDetail(value.ProviderErrorMessage),
		StatusDetails:        sanitizeContinuationDetail(value.StatusDetails),
		Reason:               string(value.TerminalReason),
		ProviderCancellation: value.TerminalReason == messages.TerminalReasonCancellation,
	}
}

func (o *sessionProgressObserver) applyLifecycle(ctx context.Context, event sd.Event) (sd.Observation, error) {
	if o == nil {
		return sd.Observation{}, sd.ErrClosed
	}
	lifecycle := o.ensureLifecycle()
	if lifecycle == nil {
		return sd.Observation{}, sd.ErrClosed
	}
	if err := o.ensureLifecycleSchedule(ctx, lifecycle); err != nil {
		return sd.Observation{}, err
	}
	observation, err := lifecycle.Apply(ctx, event)
	return observation, err
}

// ensureLifecycleSchedule declares the number of dispatched scheduled slots;
// all response IDs, ownership, continuation, retry, and disposition state
// remains private to the runtime reducer.
func (o *sessionProgressObserver) ensureLifecycleSchedule(ctx context.Context, lifecycle sd.Service) error {
	if o == nil || lifecycle == nil {
		return sd.ErrClosed
	}
	o.scheduleMu.Lock()
	dispatched := o.dispatchedInputs
	o.scheduleMu.Unlock()
	if len(lifecycle.Snapshot().Scheduled) >= dispatched {
		return nil
	}
	_, err := lifecycle.Apply(ctx, sd.Event{Kind: sd.EventEnsureScheduled, Count: dispatched})
	return err
}
func (o *sessionProgressObserver) lifecycleEvent(event sd.Event) sd.Observation {
	return o.lifecycleEventWithContext(context.Background(), event)
}

func (o *sessionProgressObserver) lifecycleEventWithContext(ctx context.Context, event sd.Event) sd.Observation {
	observation, err := o.applyLifecycle(ctx, event)
	if err != nil {
		return sd.Observation{}
	}
	return observation
}

func (o *sessionProgressObserver) noteToolResultAccepted(callID string) {
	o.noteToolResultAcceptedWithContext(context.Background(), callID)
}

func (o *sessionProgressObserver) noteToolContinuationRequested() {
	o.noteToolContinuationRequestedWithContext(context.Background())
}

func (o *sessionProgressObserver) observedResponseProjection() (active bool, id string) {
	if o == nil {
		return false, ""
	}
	snapshot := o.ensureLifecycle().Snapshot()
	return snapshot.ActiveResponse, snapshot.ActiveResponseID
}

func (o *sessionProgressObserver) plainEvent(kind sd.EventKind, id string) sd.Observation {
	return o.lifecycleEvent(sd.Event{Kind: kind, ResponseID: id})
}

//lint:ignore U1000 package tests exercise the response projection seam.
func (o *sessionProgressObserver) indexEvent(kind sd.EventKind, index int, id string) sd.Observation {
	return o.lifecycleEvent(sd.Event{Kind: kind, Index: index, ResponseID: id})
}

//lint:ignore U1000 package tests exercise the response projection seam.
func (o *sessionProgressObserver) pendingScheduledRateLimitRetryIndex() (int, bool) {
	if o == nil {
		return 0, false
	}
	snapshot := o.ensureLifecycle().Snapshot()
	for index, value := range snapshot.Scheduled {
		if value.Bound && value.RetryPending {
			return index, true
		}
	}
	return 0, false
}
func (o *sessionProgressObserver) noteScheduledResponseTerminal(id string, terminal *messages.MessageEndValue) {
	o.lifecycleEvent(sd.Event{Kind: sd.EventNoteScheduledTerminal, ResponseID: id, Terminal: lifecycleTerminal(terminal)})
}
func (o *sessionProgressObserver) hasTerminalScheduledResponseFailure() bool {
	_, ok := o.pendingScheduledFailure()
	return ok
}
func (o *sessionProgressObserver) scheduledAudioFailureMetadata() (string, string, string) {
	value, ok := o.pendingScheduledFailure()
	if !ok {
		return "", "", ""
	}
	return value.TerminalStatus, value.TerminalErrorCode, value.TerminalStatusDetails
}
func (o *sessionProgressObserver) pendingScheduledFailure() (sd.ScheduledState, bool) {
	if o == nil {
		return sd.ScheduledState{}, false
	}
	for _, value := range o.ensureLifecycle().Snapshot().Scheduled {
		if value.Bound && value.Disposition == sd.DispositionPending && value.TerminalFailure && !value.RetryPending {
			return value, true
		}
	}
	return sd.ScheduledState{}, false
}
func (o *sessionProgressObserver) bindScheduledResponseBoundary(id string) {
	o.plainEvent(sd.EventBindScheduledBoundary, id)
}
func (o *sessionProgressObserver) bindScheduledTerminalOnly(id string) {
	o.plainEvent(sd.EventBindScheduledTerminalOnly, id)
}
func (o *sessionProgressObserver) rememberRateLimitRetryCandidate(responseID, lifecycleID string, terminal *messages.MessageEndValue) {
	o.lifecycleEvent(sd.Event{Kind: sd.EventRememberRetry, ResponseID: responseID, LifecycleID: lifecycleID, Terminal: lifecycleTerminal(terminal)})
}

func (o *sessionProgressObserver) noteScheduledRateLimitRetryDispatched() {
	o.lifecycleEvent(sd.Event{Kind: sd.EventRetryDispatched})
}

//lint:ignore U1000 package tests exercise the response projection seam.
func (o *sessionProgressObserver) bindScheduledResponseID(index int, id string) bool {
	return o.indexEvent(sd.EventBindScheduledID, index, id).Accepted
}

//lint:ignore U1000 package tests exercise the response projection seam.
func (o *sessionProgressObserver) setActiveScheduledResponseWithID(index int, id string) bool {
	return o.indexEvent(sd.EventSetScheduledOwner, index, id).Accepted
}
func (o *sessionProgressObserver) claimScheduledRateLimitRetry(responseID string, terminal *messages.MessageEndValue) (time.Duration, bool) {
	if o == nil {
		return 0, false
	}
	value := o.lifecycleEvent(sd.Event{Kind: sd.EventClaimRetry, ResponseID: responseID, Terminal: lifecycleTerminal(terminal)})
	return value.Retry.Delay, value.Retry.Accepted
}
func (o *sessionProgressObserver) noteScheduledResponseDisposition(id string, disposition scheduledAudioResponseDisposition) {
	if disposition != scheduledAudioResponsePending {
		o.lifecycleEvent(sd.Event{Kind: sd.EventScheduledDisposition, ResponseID: id, Disposition: disposition})
	}
}
func (o *sessionProgressObserver) resetObservedResponseState() {
	if o == nil {
		return
	}
	activeResponse := o.ensureLifecycle().Snapshot().ActiveResponse
	if !activeResponse && !o.hasPendingLifecycleContinuation() {
		o.lifecycleEvent(sd.Event{Kind: sd.EventReset})
	}
	o.toolStateMu.Lock()
	o.resetResponseOutputLocked()
	o.assistantResponseDone = false
	o.assistantOutputObserved = false
	o.toolCallInTurn = false
	o.messageEndSeen = false
	o.toolStateMu.Unlock()
	o.toolDeltaSeen = false
}

// hasPendingLifecycleContinuation keeps a late SESSION.OPEN boundary from
// erasing a tool lifecycle acknowledgement that was already accepted by the
// provider-facing send path. The model runner may send the result and its
// response.create before the corresponding provider tool-call delta reaches
// the observer, so the lifecycle reducer can legitimately contain an
// acknowledged continuation before the first inbound boundary is consumed.
func (o *sessionProgressObserver) hasPendingLifecycleContinuation() bool {
	if o == nil {
		return false
	}
	snapshot := o.ensureLifecycle().Snapshot()
	for _, state := range snapshot.ContinuationStates {
		if state.ProviderCallObserved && !state.ResultAccepted {
			return true
		}
		if state.ResultAccepted && !state.ContinuationComplete {
			return true
		}
	}
	return false
}
func (o *sessionProgressObserver) beginObservedResponseForPurpose(id string, purpose messages.ResponsePurpose) bool {
	if o == nil {
		return false
	}
	return o.lifecycleEvent(sd.Event{Kind: sd.EventResponseOpen, ResponseID: id, Purpose: sd.ResponsePurpose(purpose)}).NewResponse
}
func (o *sessionProgressObserver) adoptObservedResponseID(id string) bool {
	if o == nil {
		return true
	}
	snapshot := o.ensureLifecycle().Snapshot()
	activeResponse, activeResponseID := snapshot.ActiveResponse, snapshot.ActiveResponseID
	if !activeResponse || activeResponseID != "" {
		return true
	}
	return o.lifecycleEvent(sd.Event{Kind: sd.EventResponseAdopt, ResponseID: id}).Accepted
}
func (o *sessionProgressObserver) ownsObservedResponseEnd(id string) bool {
	return o.plainEvent(sd.EventResponseOwnsEnd, id).OwnsResponse
}
func (o *sessionProgressObserver) finishObservedResponse(id string) {
	o.plainEvent(sd.EventResponseFinish, id)
}
func (o *sessionProgressObserver) responseEventBelongsToActive(id string) bool {
	if o == nil {
		return false
	}
	// Validate the response envelope before applying the untagged content
	// boundary. A foreign response event must not clear the reducer's terminal
	// marker and make a duplicate end for the active response admissible.
	if !o.plainEvent(sd.EventResponseBelongs, id).Accepted {
		return false
	}
	o.toolStateMu.Lock()
	contentBoundary := o.messageEndSeen
	o.toolStateMu.Unlock()
	if contentBoundary {
		contentID := strings.TrimSpace(id)
		if contentID == "" {
			_, contentID = o.observedResponseProjection()
		}
		o.lifecycleEvent(sd.Event{Kind: sd.EventResponseContentBoundary, ResponseID: contentID})
	}
	return true
}
func (o *sessionProgressObserver) observeProviderToolCallStartForResponse(callID, name, responseID string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.toolStateMu.Lock()
	enabled := o.toolResultsEnabled
	o.toolStateMu.Unlock()
	if !enabled {
		return
	}
	toolCall := o.lifecycleEvent(sd.Event{Kind: sd.EventToolCall, CallID: callID, ToolName: name, ResponseID: responseID})
	if !toolCall.Accepted {
		// Terminal buffer recovery can replay a provider tool call after its
		// response has already been finished. The reducer correctly rejects that
		// stale envelope; do not turn a continuation whose result was already
		// accepted into a new generic unresolved obligation.
		if o.continuationResultAccepted(callID) {
			return
		}
		// The reducer records an unowned provider call as a rejected continuation
		// so terminal diagnostics still have one service-owned source of truth.
		return
	}
	o.toolStateMu.Lock()
	o.providerToolCallSeen = true
	o.toolStateMu.Unlock()
}
func (o *sessionProgressObserver) observeProviderToolCallWithIDForResponse(callID, name, responseID string) {
	o.observeProviderToolCallStartForResponse(callID, name, responseID)
}
func (o *sessionProgressObserver) observeProviderMessageEndForResponse(role messages.Role, terminal *messages.MessageEndValue, responseID string, outputPresent bool) bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	duplicate := o.messageEndSeen
	o.messageEndSeen = true
	o.toolCallInTurn = false
	ch := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	value := o.lifecycleEvent(sd.Event{Kind: sd.EventResponseEnd, ResponseID: responseID, Role: sd.Role(role), Terminal: lifecycleTerminal(terminal), Output: outputPresent})
	o.toolStateMu.Lock()
	if value.Candidate {
		o.assistantResponseDone = true
	}
	o.toolStateMu.Unlock()
	if value.ContinuationChanged && ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return value.Candidate && !duplicate
}
func isLocalResponseCancellation(value *messages.MessageEndValue) bool {
	return value != nil && value.TerminalReason == messages.TerminalReasonPartialOutput && value.TerminalProvenance == messages.TerminalProvenanceLoop
}
func responseScopedStreamType(t messages.StreamMessageType) bool {
	switch t {
	case messages.StreamTypeMessageStart, messages.StreamTypeMessageEnd, messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd, messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd, messages.StreamTypeAudioStart, messages.StreamTypeAudioDelta, messages.StreamTypeAudioEnd, messages.StreamTypeImageStart, messages.StreamTypeImageDelta, messages.StreamTypeImageEnd, messages.StreamTypeVideoStart, messages.StreamTypeVideoDelta, messages.StreamTypeVideoEnd, messages.StreamTypeFileStart, messages.StreamTypeFileDelta, messages.StreamTypeFileEnd, messages.StreamTypeEmbeddingStart, messages.StreamTypeEmbeddingDelta, messages.StreamTypeEmbeddingEnd, messages.StreamTypeReasoningStart, messages.StreamTypeReasoningDelta, messages.StreamTypeReasoningEnd, messages.StreamTypeTranscriptStart, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptEnd, messages.StreamTypeRefusal, messages.StreamTypeUsageInfo:
		return true
	default:
		return false
	}
}
