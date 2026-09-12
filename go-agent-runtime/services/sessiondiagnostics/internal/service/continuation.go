package service

import (
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

func (r *reducer) toolCallLocked(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
	callID := strings.TrimSpace(event.CallID)
	if callID == "" {
		return sessiondiagnostics.Observation{}
	}
	state := r.continuations[callID]
	state.CallID = callID
	if strings.TrimSpace(event.ToolName) != "" {
		state.ToolName = strings.TrimSpace(event.ToolName)
	}
	responseID := strings.TrimSpace(event.ResponseID)
	if responseID != "" && (state.ResponseID == "" || state.ResponseID == responseID) {
		state.ResponseID = responseID
	}
	state.ProviderCallObserved = true
	r.continuations[callID] = state
	r.toolTurn = true
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: responseID}
}

func (r *reducer) toolResultAcceptedLocked(callID string) sessiondiagnostics.Observation {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return sessiondiagnostics.Observation{}
	}
	state := r.continuations[callID]
	state.CallID = callID
	state.ResultAccepted = true
	r.continuations[callID] = state
	return sessiondiagnostics.Observation{Accepted: true}
}

func (r *reducer) continuationRequestedLocked(callID string) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	requested := 0
	for id, state := range r.continuations {
		if strings.TrimSpace(callID) != "" && id != strings.TrimSpace(callID) {
			continue
		}
		if !state.ResultAccepted || state.ContinuationComplete || state.ContinuationRequested {
			continue
		}
		state.ContinuationRequested = true
		r.continuations[id] = state
		requested++
	}
	if requested == 0 {
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrMalformedSequence, 0, false
	}
	return sessiondiagnostics.Observation{Accepted: true, PendingContinuations: r.pendingContinuationCountLocked()}, nil, 0, false
}

func (r *reducer) syncLegacyLocked(legacy *sessiondiagnostics.LegacyState) sessiondiagnostics.Observation {
	if legacy == nil {
		return sessiondiagnostics.Observation{}
	}
	r.activeResponse = legacy.ActiveResponse
	r.activeResponseID = strings.TrimSpace(legacy.ActiveResponseID)
	r.activePurpose = legacy.ActivePurpose
	r.restoreLegacyIDsLocked(legacy)
	r.restoreLegacyScheduledLocked(legacy)
	r.nextScheduledResponse = legacy.NextScheduledResponse
	r.activeScheduledIndex = legacy.ActiveScheduledIndex
	r.activeScheduledID = strings.TrimSpace(legacy.ActiveScheduledID)
	r.activeScheduledSet = legacy.ActiveScheduledSet
	r.logicalScheduledIndex = legacy.LogicalScheduledIndex
	r.logicalScheduledID = strings.TrimSpace(legacy.LogicalScheduledID)
	r.logicalScheduledSet = legacy.LogicalScheduledSet
	r.retryCandidateIndex = legacy.RetryCandidateIndex
	r.retryCandidateSet = legacy.RetryCandidateSet
	r.retryCandidateID = strings.TrimSpace(legacy.RetryCandidateID)
	r.restoreLegacyContinuationsLocked(legacy)
	return sessiondiagnostics.Observation{Accepted: true}
}

func (r *reducer) restoreLegacyIDsLocked(legacy *sessiondiagnostics.LegacyState) {
	r.completedIDs = trimSet(legacy.CompletedResponseIDs)
	r.retiredIDs = trimSet(legacy.RetiredResponseIDs)
}

func trimSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func (r *reducer) restoreLegacyScheduledLocked(legacy *sessiondiagnostics.LegacyState) {
	r.scheduled = cloneScheduled(legacy.Scheduled)
	for index := range r.scheduled {
		if r.scheduled[index].Disposition == "" {
			r.scheduled[index].Disposition = sessiondiagnostics.DispositionPending
		}
	}
	r.scheduledResponseByID = cloneIndexMap(legacy.ScheduledResponseByID)
	if r.scheduledResponseByID == nil {
		r.scheduledResponseByID = make(map[string]int)
	}
	for index, state := range r.scheduled {
		for _, id := range state.ResponseIDs {
			if strings.TrimSpace(id) != "" {
				r.scheduledResponseByID[id] = index
			}
		}
	}
}

func (r *reducer) restoreLegacyContinuationsLocked(legacy *sessiondiagnostics.LegacyState) {
	r.continuations = make(map[string]sessiondiagnostics.ContinuationState, len(legacy.ContinuationStates))
	for _, state := range legacy.ContinuationStates {
		if id := strings.TrimSpace(state.CallID); id != "" {
			state.CallID = id
			r.continuations[id] = state
		}
	}
}

func (r *reducer) endResponseLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	id := strings.TrimSpace(event.ResponseID)
	effectiveID, early, admitted := r.admitResponseEndLocked(id)
	if !admitted {
		return early, nil, 0, false
	}
	duplicateEnd := r.messageEndSeen
	r.messageEndSeen = true
	changed := false
	if r.toolTurn || event.Role == sessiondiagnostics.RoleTool {
		changed = r.markToolResponseCompleteLocked(effectiveID)
	}
	if !r.toolTurn && event.Role != sessiondiagnostics.RoleTool {
		changed = r.markNonToolResponseCompleteLocked(effectiveID, changed)
	}
	if event.Role != sessiondiagnostics.RoleTool {
		changed = r.recordContinuationEndsLocked(effectiveID, event, duplicateEnd, changed)
	}
	pending := r.pendingContinuationCountLocked()
	unresolved := r.unresolvedCallCountLocked()
	candidate := event.Output && event.Role != sessiondiagnostics.RoleTool && !r.toolTurn && unresolved == 0 && pending == 0
	r.toolTurn = false
	return sessiondiagnostics.Observation{
		Accepted:             true,
		Candidate:            candidate,
		ResponseID:           effectiveID,
		ContinuationChanged:  changed,
		PendingContinuations: pending,
	}, nil, 0, false
}

func (r *reducer) admitResponseEndLocked(id string) (string, sessiondiagnostics.Observation, bool) {
	if !r.ownsResponseEndLocked(id) {
		return id, sessiondiagnostics.Observation{ResponseID: id}, false
	}
	if id != "" && r.activeResponse && r.activeResponseID == "" {
		adopted := r.adoptResponseLocked(id)
		if !adopted.Accepted {
			return id, adopted, false
		}
	}
	if !r.activeResponse && id != "" {
		opened := r.openResponseLocked(id, sessiondiagnostics.ResponsePurposeNormal)
		if opened.NewResponse {
			r.bindBoundaryLocked(id)
		}
	}
	if id == "" {
		id = r.activeResponseID
	}
	return id, sessiondiagnostics.Observation{}, true
}

func (r *reducer) markToolResponseCompleteLocked(responseID string) bool {
	changed := false
	for callID, state := range r.continuations {
		belongs := r.toolStateBelongsToResponseLocked(state, responseID)
		if !belongs || !state.ProviderCallObserved || state.ToolResponseComplete {
			continue
		}
		state.ToolResponseComplete = true
		r.continuations[callID] = state
		changed = true
	}
	return changed
}

func (r *reducer) markNonToolResponseCompleteLocked(responseID string, changed bool) bool {
	for callID, state := range r.continuations {
		if r.continuationOwnerMatchesLocked(state, responseID) && state.ProviderCallObserved && !state.ToolResponseComplete {
			state.ToolResponseComplete = true
			r.continuations[callID] = state
			changed = true
		}
	}
	return changed
}

func (r *reducer) recordContinuationEndsLocked(responseID string, event sessiondiagnostics.Event, duplicateEnd, changed bool) bool {
	for callID, state := range r.continuations {
		if !r.continuationOwnerMatchesLocked(state, responseID) || !state.ToolResponseComplete || state.ContinuationComplete {
			continue
		}
		if duplicateEnd && !state.ContinuationRequested {
			continue
		}
		if recordContinuationTerminal(&state, event.Terminal, event.Output || r.toolTurn) {
			changed = true
		}
		r.continuations[callID] = state
	}
	return changed
}

func (r *reducer) toolStateBelongsToResponseLocked(state sessiondiagnostics.ContinuationState, responseID string) bool {
	if responseID == "" {
		return state.ResponseID == "" || (state.ContinuationScheduledSet && r.activeScheduledSet && r.activeScheduledIndex == state.ContinuationScheduledIndex)
	}
	return state.ResponseID == responseID
}

func (r *reducer) continuationOwnerMatchesLocked(state sessiondiagnostics.ContinuationState, responseID string) bool {
	if responseID != "" {
		return state.ContinuationResponseID == responseID
	}
	if state.ContinuationResponseID == "" && state.ResponseID == "" {
		return !r.toolTurn
	}
	return state.ContinuationScheduledSet && r.activeResponse && r.activeResponseID == "" && r.activeScheduledSet && r.activeScheduledIndex == state.ContinuationScheduledIndex
}

func (r *reducer) pendingContinuationCountLocked() int {
	count := 0
	for _, state := range r.continuations {
		if state.ResultAccepted && !state.ContinuationComplete {
			count++
		}
	}
	return count
}

func (r *reducer) unresolvedCallCountLocked() int {
	count := 0
	for _, state := range r.continuations {
		if state.ProviderCallObserved && !state.ResultAccepted {
			count++
		}
	}
	return count
}

func (r *reducer) completedScheduledLocked() int {
	count := 0
	for _, state := range r.scheduled {
		if state.Disposition != "" && state.Disposition != sessiondiagnostics.DispositionPending {
			count++
		}
	}
	return count
}
