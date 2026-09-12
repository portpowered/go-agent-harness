// Package service contains the private response lifecycle reducer. Only the
// public sessiondiagnostics contract crosses this package boundary.
package service

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

const (
	rateLimitRetryCode         = "rate_limit_exceeded"
	defaultRateLimitRetryDelay = 2 * time.Second
	maxRateLimitRetryDelay     = 15 * time.Second
	maxStatusDetailBytes       = 256
	rateLimitRetryDelayPattern = `(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`
)

type reducer struct {
	mu        sync.Mutex
	closed    bool
	scheduler sessiondiagnostics.RetryScheduler

	activeResponse   bool
	activeResponseID string
	activePurpose    sessiondiagnostics.ResponsePurpose
	completedIDs     map[string]struct{}
	retiredIDs       map[string]struct{}
	staleIDs         map[string]struct{}

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

	continuations       map[string]sessiondiagnostics.ContinuationState
	toolTurn            bool
	messageEndSeen      bool
	responseContentSeen bool
}

var _ sessiondiagnostics.Service = (*reducer)(nil)

// New constructs one independent reducer. It performs no I/O and starts no
// goroutine; the Wire package is the normal composition entry point.
func New(options sessiondiagnostics.Options) sessiondiagnostics.Service {
	return &reducer{
		scheduler:             options.RetryScheduler,
		completedIDs:          make(map[string]struct{}),
		retiredIDs:            make(map[string]struct{}),
		staleIDs:              make(map[string]struct{}),
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
	observation, retryDelay, retryScheduled, err := r.applyLocked(event)
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
	r.resetLocked()
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
	return sessiondiagnostics.Snapshot{
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
}

func (r *reducer) resetLocked() {
	r.retireKnownResponseIDsLocked()
	r.completedIDs = make(map[string]struct{})
	r.retiredIDs = make(map[string]struct{})
	r.activeResponse = false
	r.activeResponseID = ""
	r.activePurpose = sessiondiagnostics.ResponsePurposeNormal
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
	r.responseContentSeen = false
}

// retireKnownResponseIDsLocked preserves the ownership history that a reset
// must not erase. A late provider event from the previous lifecycle can arrive
// after SESSION.OPEN has reset the reducer; keeping every known response ID
// retired makes that event fail closed instead of reopening a fresh lifecycle.
func (r *reducer) retireKnownResponseIDsLocked() {
	if r.staleIDs == nil {
		r.staleIDs = make(map[string]struct{})
	}
	for id := range r.completedIDs {
		if id != "" {
			r.staleIDs[id] = struct{}{}
		}
	}
	for id := range r.retiredIDs {
		if id != "" {
			r.staleIDs[id] = struct{}{}
		}
	}
	if r.activeResponseID != "" {
		r.staleIDs[r.activeResponseID] = struct{}{}
	}
	for id := range r.scheduledResponseByID {
		if id != "" {
			r.staleIDs[id] = struct{}{}
		}
	}
	for _, scheduled := range r.scheduled {
		for _, id := range scheduled.ResponseIDs {
			if id != "" {
				r.staleIDs[id] = struct{}{}
			}
		}
	}
	for _, continuation := range r.continuations {
		if continuation.ResponseID != "" {
			r.staleIDs[continuation.ResponseID] = struct{}{}
		}
		if continuation.ContinuationResponseID != "" {
			r.staleIDs[continuation.ContinuationResponseID] = struct{}{}
		}
	}
}
