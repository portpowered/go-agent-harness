package agentruntime

import (
	"context"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sd "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	sdw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics/wire"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
)

type scheduledAudioResponseDisposition = sd.Disposition

const (
	scheduledAudioResponsePending   = sd.DispositionPending
	scheduledAudioResponseCompleted = sd.DispositionCompleted
	scheduledAudioResponseCancelled = sd.DispositionCancelled
)

// Deprecated: retained as a compatibility projection; lifecycle decisions are
// delegated to the provider-neutral service and copied here after each event.
type scheduledAudioResponseLifecycle struct {
	bound                 bool
	disposition           scheduledAudioResponseDisposition
	retryUsed             bool
	retryPending          bool
	terminalFailure       bool
	terminalStatus        string
	terminalErrorCode     string
	terminalStatusDetails string
}

func (o *sessionProgressObserver) ensureLifecycle() sd.Service {
	if o == nil {
		return nil
	}
	if o.lifecycle == nil {
		o.lifecycle = sdw.NewService(sd.Options{})
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

func (o *sessionProgressObserver) legacyLifecycleState() *sd.LegacyState {
	if o == nil {
		return nil
	}
	state := &sd.LegacyState{
		ActiveResponse:        o.activeResponse,
		ActiveResponseID:      o.activeResponseID,
		ActivePurpose:         sd.ResponsePurpose(o.activeResponsePurpose()),
		CompletedResponseIDs:  sortedStrings(o.completedResponseIDs),
		RetiredResponseIDs:    sortedStrings(o.retiredResponseIDs),
		NextScheduledResponse: o.nextScheduledResponse,
		ActiveScheduledIndex:  o.activeScheduledResponseIndex,
		ActiveScheduledID:     o.activeScheduledResponseID,
		ActiveScheduledSet:    o.activeScheduledResponseSet,
		LogicalScheduledIndex: o.logicalScheduledResponseIndex,
		LogicalScheduledID:    o.logicalScheduledResponseID,
		LogicalScheduledSet:   o.logicalScheduledResponseSet,
		RetryCandidateIndex:   o.retryCandidateIndex,
		RetryCandidateSet:     o.retryCandidateSet,
		RetryCandidateID:      o.retryCandidateID,
		ScheduledResponseByID: maps.Clone(o.scheduledResponseByID),
	}
	state.Scheduled = make([]sd.ScheduledState, len(o.scheduledResponses))
	for index, value := range o.scheduledResponses {
		state.Scheduled[index] = sd.ScheduledState{
			Bound:                 value.bound,
			Disposition:           value.disposition,
			RetryUsed:             value.retryUsed,
			RetryPending:          value.retryPending,
			TerminalFailure:       value.terminalFailure,
			TerminalStatus:        value.terminalStatus,
			TerminalErrorCode:     value.terminalErrorCode,
			TerminalStatusDetails: value.terminalStatusDetails,
		}
	}
	for id, index := range o.scheduledResponseByID {
		if index >= 0 && index < len(state.Scheduled) && strings.TrimSpace(id) != "" {
			state.Scheduled[index].ResponseIDs = append(state.Scheduled[index].ResponseIDs, id)
		}
	}
	for index := range state.Scheduled {
		sort.Strings(state.Scheduled[index].ResponseIDs)
	}
	o.toolStateMu.Lock()
	state.ContinuationStates = make([]sd.ContinuationState, 0, len(o.toolContinuations))
	for callID, value := range o.toolContinuations {
		if value == nil || strings.TrimSpace(callID) == "" {
			continue
		}
		state.ContinuationStates = append(state.ContinuationStates, sd.ContinuationState{
			CallID:                     callID,
			ToolName:                   value.toolName,
			ResponseID:                 value.responseID,
			ProviderCallObserved:       value.providerCallObserved,
			ResultAccepted:             value.resultAccepted,
			ToolResponseComplete:       value.toolResponseComplete,
			ContinuationRequested:      value.continuationRequested,
			ContinuationResponseID:     value.continuationResponseID,
			ContinuationScheduledIndex: value.continuationScheduledIndex,
			ContinuationScheduledSet:   value.continuationScheduledSet,
			ContinuationTerminalSeen:   value.continuationTerminalSeen,
			ContinuationStatus:         value.continuationStatus,
			ContinuationErrorCode:      value.continuationErrorCode,
			ContinuationStatusDetails:  value.continuationStatusDetails,
			ContinuationReason:         string(value.continuationTerminalReason),
			ContinuationOutput:         value.continuationOutputObserved,
			ContinuationFailure:        value.continuationFailureObserved,
			ContinuationComplete:       value.continuationComplete,
		})
	}
	o.toolStateMu.Unlock()
	sort.Slice(state.ContinuationStates, func(i, j int) bool { return state.ContinuationStates[i].CallID < state.ContinuationStates[j].CallID })
	return state
}
func (o *sessionProgressObserver) applyLifecycle(ctx context.Context, event sd.Event) (sd.Observation, error) {
	if o == nil {
		return sd.Observation{}, sd.ErrClosed
	}
	lifecycle := o.ensureLifecycle()
	if lifecycle == nil {
		return sd.Observation{}, sd.ErrClosed
	}
	if _, err := lifecycle.Apply(ctx, sd.Event{Kind: sd.EventSyncLegacy, Legacy: o.legacyLifecycleState()}); err != nil {
		return sd.Observation{}, err
	}
	observation, err := lifecycle.Apply(ctx, event)
	o.syncLifecycleProjection()
	return observation, err
}
func (o *sessionProgressObserver) syncLifecycleProjection() {
	if o == nil || o.lifecycle == nil {
		return
	}
	snapshot := o.lifecycle.Snapshot()
	o.activeResponse = snapshot.ActiveResponse
	o.activeResponseID = snapshot.ActiveResponseID
	o.completedResponseIDs = make(map[string]struct{}, len(snapshot.CompletedResponseIDs))
	for _, id := range snapshot.CompletedResponseIDs {
		o.completedResponseIDs[id] = struct{}{}
	}
	o.retiredResponseIDs = make(map[string]struct{}, len(snapshot.RetiredResponseIDs))
	for _, id := range snapshot.RetiredResponseIDs {
		o.retiredResponseIDs[id] = struct{}{}
	}
	o.scheduledResponses = make([]scheduledAudioResponseLifecycle, len(snapshot.Scheduled))
	for index, value := range snapshot.Scheduled {
		o.scheduledResponses[index] = scheduledAudioResponseLifecycle{
			bound: value.Bound, disposition: value.Disposition, retryUsed: value.RetryUsed,
			retryPending: value.RetryPending, terminalFailure: value.TerminalFailure,
			terminalStatus: value.TerminalStatus, terminalErrorCode: value.TerminalErrorCode,
			terminalStatusDetails: value.TerminalStatusDetails,
		}
	}
	o.scheduledResponseByID = maps.Clone(snapshot.ScheduledResponseByID)
	if o.scheduledResponseByID == nil {
		o.scheduledResponseByID = make(map[string]int)
	}
	o.nextScheduledResponse = snapshot.NextScheduledResponse
	o.activeScheduledResponseIndex = snapshot.ActiveScheduledIndex
	o.activeScheduledResponseID = snapshot.ActiveScheduledID
	o.activeScheduledResponseSet = snapshot.ActiveScheduledSet
	o.logicalScheduledResponseIndex = snapshot.LogicalScheduledIndex
	o.logicalScheduledResponseID = snapshot.LogicalScheduledID
	o.logicalScheduledResponseSet = snapshot.LogicalScheduledSet
	o.completedScheduled = snapshot.CompletedScheduled
	o.retryCandidateIndex = snapshot.RetryCandidateIndex
	o.retryCandidateSet = snapshot.RetryCandidateSet
	o.retryCandidateID = snapshot.RetryCandidateID
	o.projectToolContinuations(snapshot.ContinuationStates)
}
func (o *sessionProgressObserver) projectToolContinuations(states []sd.ContinuationState) {
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	o.ensureToolStateLocked()
	previous := o.toolContinuations
	for _, value := range states {
		prior := previous[value.CallID]
		o.toolContinuations[value.CallID] = &toolContinuationState{
			toolName: value.ToolName, responseID: value.ResponseID, providerCallObserved: value.ProviderCallObserved || prior != nil && prior.providerCallObserved,
			resultAccepted: value.ResultAccepted || prior != nil && prior.resultAccepted, toolResponseComplete: value.ToolResponseComplete || prior != nil && prior.toolResponseComplete,
			continuationRequested: value.ContinuationRequested || prior != nil && prior.continuationRequested, continuationResponseID: value.ContinuationResponseID,
			continuationScheduledIndex: value.ContinuationScheduledIndex, continuationScheduledSet: value.ContinuationScheduledSet,
			continuationTerminalSeen: value.ContinuationTerminalSeen, continuationStatus: value.ContinuationStatus,
			continuationErrorCode: value.ContinuationErrorCode, continuationStatusDetails: value.ContinuationStatusDetails,
			continuationTerminalReason: messages.TerminalReason(value.ContinuationReason), continuationOutputObserved: value.ContinuationOutput,
			continuationFailureObserved: value.ContinuationFailure, continuationComplete: value.ContinuationComplete || prior != nil && (prior.continuationComplete || continuationSupersededByServerTurnLocked(prior)),
		}
	}
}
func sortedStrings(values map[string]struct{}) []string { return slices.Sorted(maps.Keys(values)) }
func (o *sessionProgressObserver) activeResponsePurpose() messages.ResponsePurpose {
	if o == nil || o.lifecycle == nil {
		return ""
	}
	return messages.ResponsePurpose(o.lifecycle.Snapshot().ActivePurpose)
}
func (o *sessionProgressObserver) lifecycleEvent(event sd.Event) sd.Observation {
	observation, err := o.applyLifecycle(context.Background(), event)
	if err != nil {
		return sd.Observation{}
	}
	return observation
}
func (o *sessionProgressObserver) plainEvent(kind sd.EventKind, id string) sd.Observation {
	return o.lifecycleEvent(sd.Event{Kind: kind, ResponseID: id})
}
func (o *sessionProgressObserver) indexEvent(kind sd.EventKind, index int, id string) sd.Observation {
	return o.lifecycleEvent(sd.Event{Kind: kind, Index: index, ResponseID: id})
}
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
func (o *sessionProgressObserver) bindScheduledResponseID(index int, id string) bool {
	return o.indexEvent(sd.EventBindScheduledID, index, id).Accepted
}
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
	o.toolStateMu.Lock()
	o.resetResponseOutputLocked()
	o.assistantResponseDone = false
	o.assistantOutputObserved = false
	o.toolCallInTurn = false
	o.messageEndSeen = false
	o.toolStateMu.Unlock()
	o.toolDeltaSeen = false
}
func (o *sessionProgressObserver) beginObservedResponseForPurpose(id string, purpose messages.ResponsePurpose) bool {
	if o == nil {
		return false
	}
	return o.lifecycleEvent(sd.Event{Kind: sd.EventResponseOpen, ResponseID: id, Purpose: sd.ResponsePurpose(purpose)}).NewResponse
}
func (o *sessionProgressObserver) adoptObservedResponseID(id string) bool {
	if o == nil || !o.activeResponse || o.activeResponseID != "" {
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
	o.toolStateMu.Lock()
	contentBoundary := o.messageEndSeen
	o.toolStateMu.Unlock()
	if contentBoundary {
		o.lifecycleEvent(sd.Event{Kind: sd.EventResponseContent})
	}
	return o.plainEvent(sd.EventResponseBelongs, id).Accepted
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
	o.lifecycleEvent(sd.Event{Kind: sd.EventToolCall, CallID: callID, ToolName: name, ResponseID: responseID})
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	o.providerToolCallSeen = true
	// The provider-facing tool-result send may complete while the shared
	// lifecycle reducer is applying the tool-call event. Re-read acceptance
	// under the lock before creating an unresolved obligation so that a result
	// accepted during that handoff cannot be reintroduced as pending.
	_, accepted := o.acceptedToolCalls[callID]
	if accepted {
		delete(o.unresolvedToolCalls, callID)
	} else {
		o.unresolvedToolCalls[callID] = struct{}{}
	}
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
