// Package service contains the private response lifecycle reducer. Only the
// public sessiondiagnostics contract crosses this package boundary.
package service

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

const (
	rateLimitRetryCode         = "rate_limit_exceeded"
	defaultRateLimitRetryDelay = 2 * time.Second
	maxRateLimitRetryDelay     = 15 * time.Second
	maxStatusDetailBytes       = 256
)

var rateLimitRetryDelayPattern = regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`)

type reducer struct {
	mu        sync.Mutex
	closed    bool
	scheduler sessiondiagnostics.RetryScheduler

	activeResponse   bool
	activeResponseID string
	activePurpose    sessiondiagnostics.ResponsePurpose
	completedIDs     map[string]struct{}
	retiredIDs       map[string]struct{}

	scheduled             []sessiondiagnostics.ScheduledState
	scheduledResponseByID map[string]int
	nextScheduledResponse int
	activeScheduledIndex  int
	activeScheduledID     string
	activeScheduledSet    bool
	logicalScheduledIndex int
	logicalScheduledID    string
	logicalScheduledSet   bool
	retryCandidateIndex   int
	retryCandidateSet     bool
	retryCandidateID      string

	continuations  map[string]sessiondiagnostics.ContinuationState
	toolTurn       bool
	messageEndSeen bool
}

var _ sessiondiagnostics.Service = (*reducer)(nil)

// New constructs one independent reducer. It performs no I/O and starts no
// goroutine; the Wire package is the normal composition entry point.
func New(options sessiondiagnostics.Options) sessiondiagnostics.Service {
	return &reducer{
		scheduler:             options.RetryScheduler,
		completedIDs:          make(map[string]struct{}),
		retiredIDs:            make(map[string]struct{}),
		scheduledResponseByID: make(map[string]int),
		continuations:         make(map[string]sessiondiagnostics.ContinuationState),
	}
}

func (r *reducer) Apply(ctx context.Context, event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error) {
	if r == nil {
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrClosed
	}
	r.mu.Lock()
	if r.closed && event.Kind != sessiondiagnostics.EventReset {
		r.mu.Unlock()
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrClosed
	}
	observation, err, retryDelay, retryScheduled := r.applyLocked(event)
	scheduler := r.scheduler
	r.mu.Unlock()
	if err != nil || !retryScheduled || scheduler == nil {
		return observation, err
	}
	if err := scheduler(ctx, retryDelay); err != nil {
		return observation, err
	}
	return observation, nil
}

func (r *reducer) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	scheduler := r.scheduler
	*r = reducer{
		scheduler:             scheduler,
		completedIDs:          make(map[string]struct{}),
		retiredIDs:            make(map[string]struct{}),
		scheduledResponseByID: make(map[string]int),
		continuations:         make(map[string]sessiondiagnostics.ContinuationState),
	}
	r.mu.Unlock()
}

func (r *reducer) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	r.activeResponse = false
	r.activeResponseID = ""
	r.activePurpose = sessiondiagnostics.ResponsePurposeNormal
	r.activeScheduledSet = false
	r.logicalScheduledSet = false
	r.retryCandidateSet = false
	r.mu.Unlock()
	return nil
}

func (r *reducer) Snapshot() sessiondiagnostics.Snapshot {
	if r == nil {
		return sessiondiagnostics.Snapshot{Closed: true}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *reducer) snapshotLocked() sessiondiagnostics.Snapshot {
	snapshot := sessiondiagnostics.Snapshot{
		Closed:                r.closed,
		ActiveResponse:        r.activeResponse,
		ActiveResponseID:      r.activeResponseID,
		ActivePurpose:         r.activePurpose,
		CompletedResponseIDs:  sortedSet(r.completedIDs),
		RetiredResponseIDs:    sortedSet(r.retiredIDs),
		Scheduled:             cloneScheduled(r.scheduled),
		ScheduledResponseByID: cloneIndexMap(r.scheduledResponseByID),
		NextScheduledResponse: r.nextScheduledResponse,
		ActiveScheduledIndex:  r.activeScheduledIndex,
		ActiveScheduledID:     r.activeScheduledID,
		ActiveScheduledSet:    r.activeScheduledSet,
		LogicalScheduledIndex: r.logicalScheduledIndex,
		LogicalScheduledID:    r.logicalScheduledID,
		LogicalScheduledSet:   r.logicalScheduledSet,
		CompletedScheduled:    r.completedScheduledLocked(),
		RetryCandidateIndex:   r.retryCandidateIndex,
		RetryCandidateSet:     r.retryCandidateSet,
		RetryCandidateID:      r.retryCandidateID,
		ContinuationStates:    cloneContinuations(r.continuations),
	}
	return snapshot
}

func (r *reducer) applyLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	switch event.Kind {
	case sessiondiagnostics.EventResponseOpen:
		return r.openResponseLocked(event.ResponseID, event.Purpose), nil, 0, false
	case sessiondiagnostics.EventResponseAdopt:
		return r.adoptResponseLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventResponseBelongs:
		return sessiondiagnostics.Observation{Accepted: r.responseBelongsLocked(event.ResponseID), OwnsResponse: r.responseBelongsLocked(event.ResponseID)}, nil, 0, false
	case sessiondiagnostics.EventResponseOwnsEnd:
		owns := r.ownsResponseEndLocked(event.ResponseID)
		return sessiondiagnostics.Observation{Accepted: owns, OwnsResponse: owns}, nil, 0, false
	case sessiondiagnostics.EventResponseEnd:
		return r.endResponseLocked(event)
	case sessiondiagnostics.EventResponseFinish:
		return r.finishResponseLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventBindScheduledBoundary:
		return r.bindBoundaryLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventBindScheduledTerminalOnly:
		return r.bindTerminalOnlyLocked(event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventBindScheduledID:
		return r.bindScheduledIDLocked(event.Index, event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventSetScheduledOwner:
		return r.setScheduledOwnerLocked(event.Index, event.ResponseID), nil, 0, false
	case sessiondiagnostics.EventEnsureScheduled:
		return r.ensureScheduledLocked(event.Count), nil, 0, false
	case sessiondiagnostics.EventNoteScheduledTerminal:
		return r.noteScheduledTerminalLocked(event.ResponseID, event.Terminal), nil, 0, false
	case sessiondiagnostics.EventRememberRetry:
		lifecycleID := event.LifecycleID
		if strings.TrimSpace(lifecycleID) == "" {
			lifecycleID = event.ResponseID
		}
		return r.rememberRetryLocked(event.ResponseID, lifecycleID, event.Terminal), nil, 0, false
	case sessiondiagnostics.EventClaimRetry:
		return r.claimRetryLocked(event.ResponseID, event.Terminal)
	case sessiondiagnostics.EventScheduledDisposition:
		return r.noteDispositionLocked(event.ResponseID, event.Disposition), nil, 0, false
	case sessiondiagnostics.EventToolCall:
		return r.toolCallLocked(event), nil, 0, false
	case sessiondiagnostics.EventToolResultAccepted:
		return r.toolResultAcceptedLocked(event.CallID), nil, 0, false
	case sessiondiagnostics.EventContinuationRequested:
		return r.continuationRequestedLocked(event.CallID)
	case sessiondiagnostics.EventSyncLegacy:
		return r.syncLegacyLocked(event.Legacy), nil, 0, false
	case sessiondiagnostics.EventReset:
		r.resetLocked()
		return sessiondiagnostics.Observation{Accepted: true}, nil, 0, false
	default:
		return sessiondiagnostics.Observation{}, sessiondiagnostics.ErrMalformedSequence, 0, false
	}
}

func (r *reducer) resetLocked() {
	r.activeResponse = false
	r.activeResponseID = ""
	r.activePurpose = sessiondiagnostics.ResponsePurposeNormal
	r.completedIDs = make(map[string]struct{})
	r.retiredIDs = make(map[string]struct{})
	r.scheduled = nil
	r.scheduledResponseByID = make(map[string]int)
	r.nextScheduledResponse = 0
	r.activeScheduledSet = false
	r.logicalScheduledSet = false
	r.retryCandidateSet = false
	r.retryCandidateID = ""
	r.continuations = make(map[string]sessiondiagnostics.ContinuationState)
	r.toolTurn = false
	r.messageEndSeen = false
}

func (r *reducer) openResponseLocked(rawID string, purpose sessiondiagnostics.ResponsePurpose) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if id != "" {
		if _, ok := r.completedIDs[id]; ok {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
		if _, ok := r.retiredIDs[id]; ok {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
	}
	if r.activeResponse {
		if r.activeResponseID == id {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
		if r.activeResponseID != "" && id == "" {
			return sessiondiagnostics.Observation{ResponseID: r.activeResponseID}
		}
		if r.activeResponseID != "" && id != "" {
			r.retiredIDs[r.activeResponseID] = struct{}{}
		}
	}
	r.activeResponse = true
	r.activeResponseID = id
	r.activePurpose = purpose
	r.messageEndSeen = false
	r.toolTurn = false
	return sessiondiagnostics.Observation{Accepted: true, NewResponse: true, ResponseID: id}
}

func (r *reducer) adoptResponseLocked(rawID string) sessiondiagnostics.Observation {
	if !r.activeResponse || r.activeResponseID != "" {
		return sessiondiagnostics.Observation{Accepted: true, ResponseID: r.activeResponseID}
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		return sessiondiagnostics.Observation{Accepted: true}
	}
	if _, ok := r.completedIDs[id]; ok {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if _, ok := r.retiredIDs[id]; ok {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if r.activeScheduledSet && !r.bindScheduledIDLocked(r.activeScheduledIndex, id).Accepted {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	r.activeResponseID = id
	if r.activeScheduledSet {
		r.setScheduledOwnerLocked(r.activeScheduledIndex, id)
	}
	for callID, state := range r.continuations {
		if state.ResultAccepted && state.ContinuationRequested && state.ProviderCallObserved && state.ToolResponseComplete && state.ContinuationResponseID == "" {
			state.ContinuationResponseID = id
			r.continuations[callID] = state
		}
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id}
}

func (r *reducer) responseBelongsLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if !r.activeResponse {
		return id == ""
	}
	if r.activeResponseID != "" {
		return id == "" || id == r.activeResponseID
	}
	return id == ""
}

func (r *reducer) ownsResponseEndLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if r.activeResponse {
		if r.activeResponseID != "" {
			return id == "" || id == r.activeResponseID
		}
		if id == "" {
			return true
		}
		_, completed := r.completedIDs[id]
		_, retired := r.retiredIDs[id]
		return !completed && !retired
	}
	if id != "" {
		_, completed := r.completedIDs[id]
		_, retired := r.retiredIDs[id]
		return !completed && !retired
	}
	return !r.messageEndSeen
}

func (r *reducer) finishResponseLocked(rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if id == "" {
		id = r.activeResponseID
	}
	if id != "" {
		r.completedIDs[id] = struct{}{}
	}
	r.activeResponse = false
	r.activeResponseID = ""
	if r.activeScheduledSet {
		r.clearActiveOwnerLocked(r.activeScheduledIndex, id)
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id}
}

func (r *reducer) ensureScheduledLocked(count int) sessiondiagnostics.Observation {
	if count < 0 {
		return sessiondiagnostics.Observation{}
	}
	for len(r.scheduled) < count {
		r.scheduled = append(r.scheduled, sessiondiagnostics.ScheduledState{Disposition: sessiondiagnostics.DispositionPending})
	}
	return sessiondiagnostics.Observation{Accepted: true}
}

func (r *reducer) bindBoundaryLocked(id string) sessiondiagnostics.Observation {
	if index, ok := r.pendingRetryIndexLocked(); ok {
		return r.bindRetryLocked(index, id)
	}
	if index, ok := r.pendingContinuationIndexLocked(); ok {
		observation := r.bindContinuationLocked(index, id)
		if observation.Accepted {
			r.bindPendingContinuationsLocked(index, id)
		}
		return observation
	}
	return r.bindNextLocked(id)
}

func (r *reducer) bindTerminalOnlyLocked(id string) sessiondiagnostics.Observation {
	if index, ok := r.pendingRetryIndexLocked(); ok {
		if !r.activeResponse {
			r.activeResponse = true
			r.activeResponseID = ""
			r.activePurpose = sessiondiagnostics.ResponsePurposeNormal
			r.messageEndSeen = false
			r.toolTurn = false
		}
		return r.bindRetryLocked(index, id)
	}
	return r.bindNextLocked(id)
}

func (r *reducer) bindScheduledIDLocked(index int, rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if index < 0 || index >= len(r.scheduled) {
		return sessiondiagnostics.Observation{}
	}
	if id != "" {
		if existing, ok := r.scheduledResponseByID[id]; ok && existing != index {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
		if !contains(r.scheduled[index].ResponseIDs, id) {
			r.scheduled[index].ResponseIDs = append(r.scheduled[index].ResponseIDs, id)
			sort.Strings(r.scheduled[index].ResponseIDs)
		}
		r.scheduledResponseByID[id] = index
	}
	r.scheduled[index].Bound = true
	if r.scheduled[index].Disposition == "" {
		r.scheduled[index].Disposition = sessiondiagnostics.DispositionPending
	}
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id, ScheduledIndex: index, HasScheduledIndex: true}
}

func (r *reducer) setScheduledOwnerLocked(index int, rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if index < 0 || index >= len(r.scheduled) {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	if id != "" {
		if existing, ok := r.scheduledResponseByID[id]; ok && existing != index {
			return sessiondiagnostics.Observation{ResponseID: id}
		}
		r.scheduledResponseByID[id] = index
		if !contains(r.scheduled[index].ResponseIDs, id) {
			r.scheduled[index].ResponseIDs = append(r.scheduled[index].ResponseIDs, id)
			sort.Strings(r.scheduled[index].ResponseIDs)
		}
	}
	r.activeScheduledIndex = index
	r.activeScheduledID = id
	r.activeScheduledSet = true
	r.logicalScheduledIndex = index
	r.logicalScheduledID = id
	r.logicalScheduledSet = true
	return sessiondiagnostics.Observation{Accepted: true, ResponseID: id, ScheduledIndex: index, HasScheduledIndex: true}
}

func (r *reducer) bindNextLocked(rawID string) sessiondiagnostics.Observation {
	id := strings.TrimSpace(rawID)
	if id != "" {
		if index, ok := r.scheduledResponseByID[id]; ok {
			return r.setScheduledOwnerLocked(index, id)
		}
	}
	for r.nextScheduledResponse < len(r.scheduled) && r.scheduled[r.nextScheduledResponse].Bound {
		r.nextScheduledResponse++
	}
	if r.nextScheduledResponse >= len(r.scheduled) {
		return sessiondiagnostics.Observation{ResponseID: id}
	}
	index := r.nextScheduledResponse
	bound := r.bindScheduledIDLocked(index, id)
	if !bound.Accepted {
		return bound
	}
	owner := r.setScheduledOwnerLocked(index, id)
	if !owner.Accepted {
		return owner
	}
	r.nextScheduledResponse++
	return owner
}

func (r *reducer) bindContinuationLocked(index int, id string) sessiondiagnostics.Observation {
	if index < 0 || index >= len(r.scheduled) {
		if r.logicalScheduledSet {
			index = r.logicalScheduledIndex
		} else {
			return sessiondiagnostics.Observation{}
		}
	}
	if !r.bindScheduledIDLocked(index, id).Accepted {
		return sessiondiagnostics.Observation{ResponseID: strings.TrimSpace(id)}
	}
	return r.setScheduledOwnerLocked(index, id)
}

func (r *reducer) bindPendingContinuationsLocked(index int, id string) {
	id = strings.TrimSpace(id)
	for callID, state := range r.continuations {
		if !state.ResultAccepted || !state.ContinuationRequested || !state.ProviderCallObserved || !state.ToolResponseComplete || state.ContinuationResponseID != "" {
			continue
		}
		owner, ok := r.scheduledIndexForLocked(state.ResponseID)
		if !ok || owner != index {
			continue
		}
		state.ContinuationScheduledIndex = index
		state.ContinuationScheduledSet = true
		if id != "" {
			state.ContinuationResponseID = id
		}
		r.continuations[callID] = state
	}
}

func (r *reducer) bindRetryLocked(index int, id string) sessiondiagnostics.Observation {
	if index < 0 || index >= len(r.scheduled) || !r.scheduled[index].Bound || !r.scheduled[index].RetryPending {
		return sessiondiagnostics.Observation{ResponseID: strings.TrimSpace(id)}
	}
	owner := r.bindScheduledIDLocked(index, id)
	if !owner.Accepted {
		return owner
	}
	r.scheduled[index].RetryPending = false
	r.scheduled[index].TerminalFailure = false
	r.scheduled[index].TerminalStatus = ""
	r.scheduled[index].TerminalErrorCode = ""
	r.scheduled[index].TerminalStatusDetails = ""
	for callID, state := range r.continuations {
		if !state.ContinuationScheduledSet || state.ContinuationScheduledIndex != index {
			continue
		}
		state.ContinuationResponseID = strings.TrimSpace(id)
		clearContinuationTerminal(&state)
		r.continuations[callID] = state
	}
	return owner
}

func (r *reducer) pendingRetryIndexLocked() (int, bool) {
	for index := range r.scheduled {
		if r.scheduled[index].Bound && r.scheduled[index].RetryPending {
			return index, true
		}
	}
	return 0, false
}

func (r *reducer) pendingContinuationIndexLocked() (int, bool) {
	index := -1
	for _, state := range r.continuations {
		if !state.ResultAccepted || !state.ContinuationRequested || !state.ProviderCallObserved || !state.ToolResponseComplete || state.ContinuationResponseID != "" {
			continue
		}
		owner, ok := r.scheduledIndexForLocked(state.ResponseID)
		if !ok || (index >= 0 && owner >= index) {
			continue
		}
		index = owner
	}
	return index, index >= 0
}

func (r *reducer) scheduledIndexForLocked(rawID string) (int, bool) {
	id := strings.TrimSpace(rawID)
	if id != "" {
		index, ok := r.scheduledResponseByID[id]
		return index, ok && index >= 0 && index < len(r.scheduled)
	}
	if r.logicalScheduledSet {
		return r.logicalScheduledIndex, true
	}
	if r.activeScheduledSet {
		return r.activeScheduledIndex, true
	}
	return 0, false
}

func (r *reducer) noteScheduledTerminalLocked(rawID string, terminal *sessiondiagnostics.Terminal) sessiondiagnostics.Observation {
	if terminal == nil || !providerFailure(terminal) {
		return sessiondiagnostics.Observation{}
	}
	index, ok := r.scheduledIndexForLocked(rawID)
	if !ok || index < 0 || index >= len(r.scheduled) || !r.scheduled[index].Bound || r.scheduled[index].Disposition != sessiondiagnostics.DispositionPending {
		return sessiondiagnostics.Observation{}
	}
	r.scheduled[index].TerminalFailure = true
	r.scheduled[index].TerminalStatus = normalize(terminal.Status)
	r.scheduled[index].TerminalErrorCode = sanitize(providerErrorCode(terminal))
	r.scheduled[index].TerminalStatusDetails = sanitize(terminal.StatusDetails)
	if r.scheduled[index].TerminalStatusDetails == "" {
		r.scheduled[index].TerminalStatusDetails = sanitize(providerErrorMessage(terminal))
	}
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true}
}

func (r *reducer) rememberRetryLocked(responseID, lifecycleID string, terminal *sessiondiagnostics.Terminal) sessiondiagnostics.Observation {
	r.retryCandidateSet = false
	r.retryCandidateID = ""
	delay, eligible := retryDecision(terminal)
	if !eligible {
		return sessiondiagnostics.Observation{}
	}
	index, ok := r.scheduledIndexForLocked(lifecycleID)
	if !ok {
		return sessiondiagnostics.Observation{}
	}
	r.retryCandidateIndex = index
	r.retryCandidateSet = true
	r.retryCandidateID = strings.TrimSpace(responseID)
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Retry: sessiondiagnostics.RetryRequest{Delay: delay}}
}

func (r *reducer) claimRetryLocked(rawID string, terminal *sessiondiagnostics.Terminal) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	delay, eligible := retryDecision(terminal)
	if !eligible {
		return sessiondiagnostics.Observation{}, nil, 0, false
	}
	index, ok := r.scheduledIndexForLocked(rawID)
	if !ok && r.retryCandidateSet && strings.TrimSpace(rawID) == r.retryCandidateID {
		index, ok = r.retryCandidateIndex, true
	}
	if !ok || index < 0 || index >= len(r.scheduled) {
		return sessiondiagnostics.Observation{}, nil, 0, false
	}
	lifecycle := &r.scheduled[index]
	if !lifecycle.Bound || lifecycle.Disposition != sessiondiagnostics.DispositionPending {
		return sessiondiagnostics.Observation{}, nil, 0, false
	}
	if lifecycle.RetryUsed {
		return sessiondiagnostics.Observation{ScheduledIndex: index, HasScheduledIndex: true, Retry: sessiondiagnostics.RetryRequest{Exhausted: true}}, sessiondiagnostics.ErrRetryExhausted, 0, false
	}
	lifecycle.RetryUsed = true
	lifecycle.RetryPending = true
	lifecycle.TerminalFailure = false
	lifecycle.TerminalStatus = ""
	lifecycle.TerminalErrorCode = ""
	lifecycle.TerminalStatusDetails = ""
	r.retryCandidateSet = false
	r.retryCandidateID = ""
	for callID, state := range r.continuations {
		if state.ContinuationScheduledSet && state.ContinuationScheduledIndex == index {
			clearContinuationTerminal(&state)
			r.continuations[callID] = state
		}
	}
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Retry: sessiondiagnostics.RetryRequest{Accepted: true, Delay: delay}}, nil, delay, true
}

func (r *reducer) noteDispositionLocked(rawID string, disposition sessiondiagnostics.Disposition) sessiondiagnostics.Observation {
	if disposition == sessiondiagnostics.DispositionPending {
		return sessiondiagnostics.Observation{}
	}
	index, ok := r.scheduledIndexForLocked(rawID)
	if !ok {
		if strings.TrimSpace(rawID) != "" || !r.canBindUnidentifiedLocked(rawID) {
			return sessiondiagnostics.Observation{}
		}
		bound := r.bindNextLocked(rawID)
		if !bound.Accepted {
			return bound
		}
		index, ok = r.scheduledIndexForLocked(rawID)
	}
	if !ok || index < 0 || index >= len(r.scheduled) || !r.scheduled[index].Bound || !r.ownerMatchesLocked(index, rawID) {
		return sessiondiagnostics.Observation{}
	}
	lifecycle := &r.scheduled[index]
	if lifecycle.Disposition != sessiondiagnostics.DispositionPending {
		r.clearScheduledOwnerLocked(index, rawID)
		return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Disposition: lifecycle.Disposition}
	}
	lifecycle.Disposition = disposition
	lifecycle.RetryPending = false
	lifecycle.RetryUsed = false
	lifecycle.TerminalFailure = false
	lifecycle.TerminalStatus = ""
	lifecycle.TerminalErrorCode = ""
	lifecycle.TerminalStatusDetails = ""
	r.clearScheduledOwnerLocked(index, rawID)
	return sessiondiagnostics.Observation{Accepted: true, ScheduledIndex: index, HasScheduledIndex: true, Disposition: disposition}
}

func (r *reducer) canBindUnidentifiedLocked(rawID string) bool {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return true
	}
	if r.activeScheduledSet && r.activeScheduledID != "" && r.activeScheduledID != id {
		return false
	}
	return !r.logicalScheduledSet || r.logicalScheduledID == "" || r.logicalScheduledID == id
}

func (r *reducer) ownerMatchesLocked(index int, rawID string) bool {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return r.activeScheduledSet && r.activeScheduledIndex == index && r.activeScheduledID == ""
	}
	if r.activeScheduledSet && r.activeScheduledIndex == index {
		return r.activeScheduledID == id
	}
	if r.logicalScheduledSet && r.logicalScheduledIndex == index {
		return r.logicalScheduledID == id
	}
	return true
}

func (r *reducer) clearScheduledOwnerLocked(index int, rawID string) {
	id := strings.TrimSpace(rawID)
	r.clearActiveOwnerLocked(index, id)
	if r.logicalScheduledSet && r.logicalScheduledIndex == index && r.logicalScheduledID == id {
		r.logicalScheduledSet = false
		r.logicalScheduledID = ""
	}
}

func (r *reducer) clearActiveOwnerLocked(index int, rawID string) {
	id := strings.TrimSpace(rawID)
	if r.activeScheduledSet && r.activeScheduledIndex == index && r.activeScheduledID == id {
		r.activeScheduledSet = false
		r.activeScheduledID = ""
	}
}

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
	if legacy.ActiveResponseID == "" {
		r.activeResponseID = ""
	}
	r.completedIDs = make(map[string]struct{}, len(legacy.CompletedResponseIDs))
	for _, id := range legacy.CompletedResponseIDs {
		if id = strings.TrimSpace(id); id != "" {
			r.completedIDs[id] = struct{}{}
		}
	}
	r.retiredIDs = make(map[string]struct{}, len(legacy.RetiredResponseIDs))
	for _, id := range legacy.RetiredResponseIDs {
		if id = strings.TrimSpace(id); id != "" {
			r.retiredIDs[id] = struct{}{}
		}
	}
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
	r.continuations = make(map[string]sessiondiagnostics.ContinuationState, len(legacy.ContinuationStates))
	for _, state := range legacy.ContinuationStates {
		if id := strings.TrimSpace(state.CallID); id != "" {
			state.CallID = id
			r.continuations[id] = state
		}
	}
	return sessiondiagnostics.Observation{Accepted: true}
}

func (r *reducer) endResponseLocked(event sessiondiagnostics.Event) (sessiondiagnostics.Observation, error, time.Duration, bool) {
	id := strings.TrimSpace(event.ResponseID)
	if !r.ownsResponseEndLocked(id) {
		return sessiondiagnostics.Observation{ResponseID: id}, nil, 0, false
	}
	if id != "" && r.activeResponse && r.activeResponseID == "" {
		adopted := r.adoptResponseLocked(id)
		if !adopted.Accepted {
			return adopted, nil, 0, false
		}
	}
	if !r.activeResponse && id != "" {
		opened := r.openResponseLocked(id, sessiondiagnostics.ResponsePurposeNormal)
		if opened.NewResponse {
			r.bindBoundaryLocked(id)
		}
	}
	effectiveID := id
	if effectiveID == "" {
		effectiveID = r.activeResponseID
	}
	duplicateEnd := r.messageEndSeen
	r.messageEndSeen = true
	changed := false
	if event.Role == sessiondiagnostics.RoleTool {
		for callID, state := range r.continuations {
			if r.toolStateBelongsToResponseLocked(state, effectiveID) && state.ProviderCallObserved && !state.ToolResponseComplete {
				state.ToolResponseComplete = true
				r.continuations[callID] = state
			}
		}
	} else {
		for callID, state := range r.continuations {
			if !r.continuationOwnerMatchesLocked(state, effectiveID) || !state.ToolResponseComplete || state.ContinuationComplete {
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

func recordContinuationTerminal(state *sessiondiagnostics.ContinuationState, terminal *sessiondiagnostics.Terminal, output bool) bool {
	if state == nil || !state.ToolResponseComplete {
		return false
	}
	if !state.ContinuationTerminalSeen {
		state.ContinuationTerminalSeen = true
		if terminal != nil {
			state.ContinuationStatus = normalize(terminal.Status)
			state.ContinuationErrorCode = sanitize(providerErrorCode(terminal))
			state.ContinuationStatusDetails = sanitize(terminal.StatusDetails)
			if state.ContinuationStatusDetails == "" {
				state.ContinuationStatusDetails = sanitize(providerErrorMessage(terminal))
			}
			state.ContinuationReason = terminal.Reason
		}
		state.ContinuationOutput = output
		if supersededByTurn(state) {
			state.ContinuationComplete = true
			return true
		}
		status := normalize(state.ContinuationStatus)
		failed := !state.ContinuationOutput || (status != "" && status != "completed") || (state.ContinuationReason != "" && state.ContinuationReason != "provider_authored_completion" && state.ContinuationReason != "loop_synthesized_completion")
		if failed && state.ContinuationStatusDetails == "" && state.ContinuationReason != "" && !state.ContinuationOutput {
			state.ContinuationStatusDetails = "assistant continuation produced no observable output"
		}
		if failed {
			state.ContinuationFailure = true
		}
	}
	if continuationCanComplete(*state) {
		state.ContinuationComplete = true
		return true
	}
	return state.ResultAccepted && state.ContinuationRequested
}

func continuationCanComplete(state sessiondiagnostics.ContinuationState) bool {
	if !state.ResultAccepted || !state.ContinuationRequested || !state.ToolResponseComplete || !state.ContinuationTerminalSeen {
		return false
	}
	status := normalize(state.ContinuationStatus)
	if state.ContinuationFailure || (status != "" && status != "completed") {
		return false
	}
	if state.ContinuationReason != "" && state.ContinuationReason != "provider_authored_completion" && state.ContinuationReason != "loop_synthesized_completion" {
		return false
	}
	return state.ContinuationOutput
}

func supersededByTurn(state *sessiondiagnostics.ContinuationState) bool {
	if state == nil {
		return false
	}
	status := normalize(state.ContinuationStatus)
	if status != "cancelled" && status != "canceled" {
		return false
	}
	for _, field := range strings.FieldsFunc(state.ContinuationStatusDetails, func(r rune) bool { return r == ',' || r == ';' }) {
		if strings.TrimSpace(field) == "reason=turn_detected" {
			return true
		}
	}
	return false
}

func clearContinuationTerminal(state *sessiondiagnostics.ContinuationState) {
	state.ContinuationTerminalSeen = false
	state.ContinuationStatus = ""
	state.ContinuationErrorCode = ""
	state.ContinuationStatusDetails = ""
	state.ContinuationReason = ""
	state.ContinuationOutput = false
	state.ContinuationFailure = false
	state.ContinuationComplete = false
}

func providerFailure(terminal *sessiondiagnostics.Terminal) bool {
	if terminal == nil || terminal.ProviderCancellation || normalize(terminal.Reason) == "cancellation" {
		return false
	}
	status := normalize(terminal.Status)
	switch status {
	case "", "completed":
		return normalize(terminal.Reason) == "terminal_failure"
	case "cancelled", "canceled":
		return false
	default:
		return true
	}
}

func retryDecision(terminal *sessiondiagnostics.Terminal) (time.Duration, bool) {
	if terminal == nil || normalize(terminal.Status) != "failed" || normalize(terminal.Reason) == "cancellation" || providerErrorCode(terminal) != rateLimitRetryCode {
		return 0, false
	}
	message := providerErrorMessage(terminal)
	match := rateLimitRetryDelayPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return defaultRateLimitRetryDelay, true
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return defaultRateLimitRetryDelay, true
	}
	if seconds > maxRateLimitRetryDelay.Seconds() {
		return maxRateLimitRetryDelay, true
	}
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		return time.Nanosecond, true
	}
	return delay, true
}

func providerErrorCode(terminal *sessiondiagnostics.Terminal) string {
	if terminal == nil {
		return ""
	}
	if code := strings.TrimSpace(terminal.ErrorCode); code != "" {
		return code
	}
	return legacyDetail(terminal.StatusDetails, "code")
}

func providerErrorMessage(terminal *sessiondiagnostics.Terminal) string {
	if terminal == nil {
		return ""
	}
	if message := strings.TrimSpace(terminal.ErrorMessage); message != "" {
		return message
	}
	return legacyDetail(terminal.StatusDetails, "message")
}

func legacyDetail(details, wanted string) string {
	parts := strings.Split(details, ",")
	for index, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) != wanted {
			continue
		}
		value = strings.TrimSpace(value)
		if wanted == "message" && index+1 < len(parts) {
			value = strings.TrimSpace(strings.Join(append([]string{value}, parts[index+1:]...), ","))
		}
		return sanitize(value)
	}
	return ""
}

func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func sanitize(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > maxStatusDetailBytes {
		return value[:maxStatusDetailBytes]
	}
	return value
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneIndexMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneScheduled(values []sessiondiagnostics.ScheduledState) []sessiondiagnostics.ScheduledState {
	if values == nil {
		return nil
	}
	result := make([]sessiondiagnostics.ScheduledState, len(values))
	for index, value := range values {
		result[index] = value
		result[index].ResponseIDs = append([]string(nil), value.ResponseIDs...)
	}
	return result
}

func cloneContinuations(values map[string]sessiondiagnostics.ContinuationState) []sessiondiagnostics.ContinuationState {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]sessiondiagnostics.ContinuationState, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
