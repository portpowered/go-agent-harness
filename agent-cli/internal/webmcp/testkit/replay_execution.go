package testkit

import (
	"context"
	"fmt"
)

// NewBrowserReplay validates a script and constructs a strict browser replay.
// No fixture operation is consumed during construction.
func NewBrowserReplay(script BrowserScript, options ...ReplayOption) (*BrowserReplay, error) {
	if err := script.Validate(); err != nil {
		return nil, err
	}
	replay := &BrowserReplay{
		script:          cloneBrowserScript(script),
		mode:            ReplayStrict,
		clock:           ClockFunc(func() uint64 { return 0 }),
		ids:             NewDeterministicIDSource("replay"),
		browserID:       "fixture-browser",
		generation:      1,
		activeOperation: -1,
		pending:         make(map[string]struct{}),
		done:            make(chan struct{}),
		stream:          make(chan FixtureEvent, countScriptEvents(script)),
		outcome:         ReplayOutcome{Status: ReplayOpen},
	}
	for _, option := range options {
		if option != nil {
			option(replay)
		}
	}
	if replay.mode != ReplayStrict && replay.mode != ReplayDiagnostic {
		return nil, fmt.Errorf("%w: unknown replay mode %q", ErrInvalidReplayRequest, replay.mode)
	}
	if err := validateScriptID(replay.browserID); err != nil {
		return nil, fmt.Errorf("%w: browser ID: %w", ErrInvalidReplayRequest, err)
	}
	if replay.targetID == "" && len(replay.script.Endpoint.Targets) > 0 {
		replay.targetID = replay.script.Endpoint.Targets[0].ID
	}
	if replay.targetID != "" {
		for _, target := range replay.script.Endpoint.Targets {
			if target.ID == replay.targetID {
				replay.target = target
				break
			}
		}
		if replay.target.ID == "" {
			return nil, fmt.Errorf("%w: target %q was not found", ErrInvalidReplayRequest, replay.targetID)
		}
	}
	if len(replay.script.Operations) == 0 {
		replay.completeLocked()
	}
	return replay, nil
}

// ObserveOperation consumes one operation from the expected sequence and
// returns the scripted response. Declared emitted events remain pending and
// must be supplied to ObserveEvent in the same order.
func (r *BrowserReplay) ObserveOperation(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	if r == nil {
		return RuntimeExecution{}, ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.prepareLocked(ctx); err != nil {
		return RuntimeExecution{}, err
	}
	if r.mode == ReplayDiagnostic && isDiagnosticReadOnlyOperation(request) {
		if err := validateDiagnosticReadOnlyRequest(request); err != nil {
			return RuntimeExecution{}, r.divergeOperationLocked(request, "request", requestTypeLabel(request), "invalid read-only discovery/list request", err)
		}
		r.ignored = append(r.ignored, cloneOperationRequest(request))
		return RuntimeExecution{Request: cloneOperationRequest(request)}, nil
	}
	execution, err := r.matchOperationLocked(request)
	if err != nil {
		return RuntimeExecution{}, err
	}
	r.maybeCompleteLocked()
	return execution, nil
}

// Execute consumes the next expected operation and all of its declared
// emitted events. It is useful when the replay itself is the scripted browser
// endpoint; adapter conformance callers should use ObserveOperation followed
// by ObserveEvent to verify their actual generated events.
func (r *BrowserReplay) Execute(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	if r == nil {
		return RuntimeExecution{}, ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.prepareLocked(ctx); err != nil {
		return RuntimeExecution{}, err
	}
	if r.mode == ReplayDiagnostic && isDiagnosticReadOnlyOperation(request) {
		if err := validateDiagnosticReadOnlyRequest(request); err != nil {
			return RuntimeExecution{}, r.divergeOperationLocked(request, "request", requestTypeLabel(request), "invalid read-only discovery/list request", err)
		}
		r.ignored = append(r.ignored, cloneOperationRequest(request))
		return RuntimeExecution{Request: cloneOperationRequest(request)}, nil
	}
	execution, err := r.matchOperationLocked(request)
	if err != nil {
		return RuntimeExecution{}, err
	}
	for _, event := range execution.Events {
		if err := r.matchEventLocked(event); err != nil {
			return RuntimeExecution{}, err
		}
	}
	r.maybeCompleteLocked()
	return execution, nil
}

// ObserveEvent consumes the next expected emitted event. It accepts either a
// FixtureEvent (with runtime context) or an EmittedEvent (semantic fields
// only), allowing the same verifier to serve the scripted runtime and an
// adapter that constructs its own event structs.
func (r *BrowserReplay) ObserveEvent(ctx context.Context, value any) error {
	if r == nil {
		return ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.prepareLocked(ctx); err != nil {
		return err
	}
	actual, err := replayEventValue(value)
	if err != nil {
		return r.divergeEventLocked(FixtureEvent{}, "event", r.expectedEventLabelLocked(), "invalid", err)
	}
	if err := r.matchEventLocked(actual); err != nil {
		return err
	}
	r.maybeCompleteLocked()
	return nil
}

// Complete succeeds only after all expected operations/events and invocation
// responses have been consumed. A failed replay keeps its primary error.
func (r *BrowserReplay) Complete() error {
	if r == nil {
		return ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.outcome.Err
	}
	if r.operationIndex != len(r.script.Operations) || r.activeOperation >= 0 || len(r.pending) != 0 {
		return r.incompleteLocked("replay complete")
	}
	r.completeLocked()
	return nil
}

// Close terminates an unfinished replay as incomplete. A completed replay is
// safe to close repeatedly.
func (r *BrowserReplay) Close() error {
	if r == nil {
		return ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.outcome.Err
	}
	return r.incompleteLocked("replay close")
}

// Wait blocks until replay reaches a terminal state or ctx/replay context is
// canceled. It does not create a watcher goroutine and never converts a prior
// divergence or incompletion into cancellation.
func (r *BrowserReplay) Wait(ctx context.Context) error {
	if r == nil {
		return ErrReplayClosed
	}
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidReplayRequest)
	}
	for {
		r.mu.Lock()
		if r.closed {
			err := r.outcome.Err
			r.mu.Unlock()
			return err
		}
		done := r.done
		replayDone, replayErr := r.replayDone, r.replayErr
		r.mu.Unlock()

		select {
		case <-done:
			return r.Err()
		case <-ctx.Done():
			return r.cancelFromContext(ctx.Err())
		case <-replayDone:
			return r.cancelFromContext(replayErr())
		}
	}
}

// Done closes when replay completes, diverges, becomes incomplete, or is
// canceled.
func (r *BrowserReplay) Done() <-chan struct{} {
	if r == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return r.done
}

// Observations returns accepted events in order.
func (r *BrowserReplay) Observations() []FixtureEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]FixtureEvent, len(r.observed))
	for index, event := range r.observed {
		result[index] = cloneFixtureEvent(event)
	}
	return result
}

// IgnoredOperations returns diagnostic-only read-only operations skipped by
// the replay cursor.
func (r *BrowserReplay) IgnoredOperations() []OperationRequest {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]OperationRequest, len(r.ignored))
	for index, request := range r.ignored {
		result[index] = cloneOperationRequest(request)
	}
	return result
}

// Outcome returns a copy of the current replay lifecycle snapshot.
func (r *BrowserReplay) Outcome() ReplayOutcome {
	if r == nil {
		return ReplayOutcome{Status: ReplayIncomplete, Err: ErrReplayClosed}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outcomeLocked()
}

// Err returns the primary terminal replay error, if one occurred.
func (r *BrowserReplay) Err() error { return r.Outcome().Err }

// PendingInvocationIDs returns stable pending invocation IDs.
func (r *BrowserReplay) PendingInvocationIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return pendingIDs(r.pending)
}
