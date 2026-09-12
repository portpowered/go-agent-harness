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
