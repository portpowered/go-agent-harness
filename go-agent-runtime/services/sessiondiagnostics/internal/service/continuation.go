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
	responseID := strings.TrimSpace(event.ResponseID)
	// A tool call is provider output owned by the active response. An explicit
	// foreign response ID must never create a new continuation or rebind an
	// existing call. Empty IDs remain compatible with legacy providers and can
	// be enriched by a later identified boundary.
	if !r.responseBelongsLocked(responseID) {
		return sessiondiagnostics.Observation{ResponseID: responseID}
	}
	state := r.continuations[callID]
	if state.ResponseID != "" && responseID != "" && state.ResponseID != responseID {
		return sessiondiagnostics.Observation{ResponseID: responseID}
	}
	state.CallID = callID
	if strings.TrimSpace(event.ToolName) != "" {
		state.ToolName = strings.TrimSpace(event.ToolName)
	}
	if responseID != "" && state.ResponseID == "" {
		state.ResponseID = responseID
	}
	state.ProviderCallObserved = true
	r.continuations[callID] = state
	r.responseContentSeen = true
	r.toolTurn = true
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: responseID}
}

func (r *reducer) toolResultAcceptedLocked(callID string) sessiondiagnostics.Observation {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return sessiondiagnostics.Observation{}
	}
	state, known := r.continuations[callID]
	// A result may legitimately beat the provider tool-call delta, but only
	// while a response lifecycle is active. A result on a fresh reducer is an
	// orphan and must not manufacture continuation ownership.
	if !known && !r.activeResponse {
		return sessiondiagnostics.Observation{}
	}
	state.CallID = callID
	if !known {
		state.ResponseID = strings.TrimSpace(r.activeResponseID)
	}
	state.ResultAccepted = true
	if continuationCanComplete(state) {
		state.ContinuationComplete = true
	}
	r.continuations[callID] = state
	return sessiondiagnostics.Observation{Accepted: true}
}

func (r *reducer) continuationRequestedLocked(callID string) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	requested := 0
	for id, state := range r.continuations {
		if strings.TrimSpace(callID) != "" && id != strings.TrimSpace(callID) {
			continue
		}
		if !state.ResultAccepted || state.ContinuationComplete || state.ContinuationRequested {
			continue
		}
		state.ContinuationRequested = true
		if continuationCanComplete(state) {
			state.ContinuationComplete = true
		}
		r.continuations[id] = state
		requested++
	}
	if requested == 0 {
		return sessiondiagnostics.Observation{}, 0, false, sessiondiagnostics.ErrMalformedSequence
	}
	return sessiondiagnostics.Observation{Accepted: true, PendingContinuations: r.pendingContinuationCountLocked()}, 0, false, nil
}

func (r *reducer) endResponseLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, time.Duration, bool, error) {
	id := strings.TrimSpace(event.ResponseID)
	// A provider response has exactly one terminal boundary. Tool-role events
	// are the separate provider-tool-result bridge used to complete a pending
	// continuation after the model's tool-call response has ended, so they may
	// still arrive while messageEndSeen is true.
	if r.messageEndSeen && event.Role != sessiondiagnostics.RoleTool {
		return sessiondiagnostics.Observation{ResponseID: id}, 0, false, nil
	}
	effectiveID, early, admitted := r.admitResponseEndLocked(id, event.Purpose)
	if !admitted {
		return early, 0, false, nil
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
	candidate := r.responsePurposeAllowsAdmissionLocked() && terminalAllowsAdmission(event.Terminal) && event.Output && event.Role != sessiondiagnostics.RoleTool && !r.toolTurn && unresolved == 0 && pending == 0
	r.toolTurn = false
	return sessiondiagnostics.Observation{
		Accepted:             true,
		Candidate:            candidate,
		Admitted:             candidate,
		ResponseID:           effectiveID,
		ContinuationChanged:  changed,
		PendingContinuations: pending,
	}, 0, false, nil
}

func (r *reducer) admitResponseEndLocked(id string, purpose sessiondiagnostics.ResponsePurpose) (string, sessiondiagnostics.Observation, bool) {
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
		opened := r.openResponseLocked(id, purpose)
		if !opened.NewResponse {
			return id, opened, false
		}
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
