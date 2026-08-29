package testkit

import (
	"context"
	"errors"
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
		return nil, fmt.Errorf("%w: browser ID: %v", ErrInvalidReplayRequest, err)
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

// NewReplay is an alias for NewBrowserReplay.
func NewReplay(script BrowserScript, options ...ReplayOption) (*BrowserReplay, error) {
	return NewBrowserReplay(script, options...)
}

// NewBrowserScriptReplay is an alias for NewBrowserReplay.
func NewBrowserScriptReplay(script BrowserScript, options ...ReplayOption) (*BrowserReplay, error) {
	return NewBrowserReplay(script, options...)
}

// NewScriptReplay is an alias for NewBrowserReplay.
func NewScriptReplay(script BrowserScript, options ...ReplayOption) (*BrowserReplay, error) {
	return NewBrowserReplay(script, options...)
}

// NewBrowserReplayFromBytes loads a strict browser-script.v1 document before
// constructing a replay.
func NewBrowserReplayFromBytes(data []byte, options ...ReplayOption) (*BrowserReplay, error) {
	script, err := LoadBrowserScript(data)
	if err != nil {
		return nil, err
	}
	return NewBrowserReplay(script, options...)
}

// NewReplayFromBytes is an alias for NewBrowserReplayFromBytes.
func NewReplayFromBytes(data []byte, options ...ReplayOption) (*BrowserReplay, error) {
	return NewBrowserReplayFromBytes(data, options...)
}

// NewBrowserReplayFromFile loads a fixture from disk before constructing a
// replay.
func NewBrowserReplayFromFile(path string, options ...ReplayOption) (*BrowserReplay, error) {
	script, err := LoadBrowserScriptFile(path)
	if err != nil {
		return nil, err
	}
	return NewBrowserReplay(script, options...)
}

// NewReplayFromFile is an alias for NewBrowserReplayFromFile.
func NewReplayFromFile(path string, options ...ReplayOption) (*BrowserReplay, error) {
	return NewBrowserReplayFromFile(path, options...)
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

// MatchOperation is an alias for ObserveOperation.
func (r *BrowserReplay) MatchOperation(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.ObserveOperation(ctx, request)
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

// ExecuteOperation, Run, and RunOperation are descriptive aliases for
// Execute.
func (r *BrowserReplay) ExecuteOperation(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.Execute(ctx, request)
}

func (r *BrowserReplay) Run(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.Execute(ctx, request)
}

func (r *BrowserReplay) RunOperation(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.Execute(ctx, request)
}

// ObserveExecution matches an operation, its scripted result, and the actual
// emitted events captured by a caller. It is the atomic conformance helper.
func (r *BrowserReplay) ObserveExecution(ctx context.Context, execution RuntimeExecution) error {
	if r == nil {
		return ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.prepareLocked(ctx); err != nil {
		return err
	}
	if r.mode == ReplayDiagnostic && isDiagnosticReadOnlyOperation(execution.Request) {
		if err := validateDiagnosticReadOnlyRequest(execution.Request); err != nil {
			return r.divergeOperationLocked(execution.Request, "request", requestTypeLabel(execution.Request), "invalid read-only discovery/list request", err)
		}
		r.ignored = append(r.ignored, cloneOperationRequest(execution.Request))
		return nil
	}
	expected, err := r.matchOperationLocked(execution.Request)
	if err != nil {
		return err
	}
	if diff := replayJSONDifference(expected.Result, execution.Result); diff != "" {
		return r.divergeOperationLocked(execution.Request, "result", requestTypeLabel(execution.Request), requestTypeLabel(execution.Request), errors.New("scripted result differs at "+diff))
	}
	if expected.InvocationID != execution.InvocationID {
		return r.divergeOperationLocked(execution.Request, "invocation_id", requestTypeLabel(execution.Request), requestTypeLabel(execution.Request), errors.New("invocation ID differs"))
	}
	for index, actual := range execution.Events {
		if index >= len(expected.Events) {
			return r.divergeEventLocked(actual, "event", "end", eventTypeLabel(actual), errors.New("unexpected event after scripted events"))
		}
		if err := r.matchEventLocked(actual); err != nil {
			return err
		}
	}
	r.maybeCompleteLocked()
	return nil
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

// MatchEvent, ObserveEmittedEvent, MatchEmittedEvent, and SendEvent are
// aliases for ObserveEvent. The typed helpers make call sites self-documenting
// while retaining one dynamic entry point for the two event representations.
func (r *BrowserReplay) MatchEvent(ctx context.Context, event FixtureEvent) error {
	return r.ObserveEvent(ctx, event)
}

func (r *BrowserReplay) ObserveEmittedEvent(ctx context.Context, event EmittedEvent) error {
	return r.ObserveEvent(ctx, event)
}

func (r *BrowserReplay) MatchEmittedEvent(ctx context.Context, event EmittedEvent) error {
	return r.ObserveEvent(ctx, event)
}

func (r *BrowserReplay) SendEvent(ctx context.Context, event FixtureEvent) error {
	return r.ObserveEvent(ctx, event)
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

// Finish is an alias for Complete.
func (r *BrowserReplay) Finish() error { return r.Complete() }

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

// Cancel marks an open replay canceled. Divergence or incompletion already
// recorded remains the primary outcome.
func (r *BrowserReplay) Cancel() error {
	if r == nil {
		return ErrReplayClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.outcome.Err
	}
	return r.cancelLocked(context.Canceled)
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
		replayContext := r.replayContext
		r.mu.Unlock()

		select {
		case <-done:
			return r.Err()
		case <-ctx.Done():
			return r.cancelFromContext(ctx.Err())
		case <-replayContextDone(replayContext):
			return r.cancelFromContext(replayContext.Err())
		}
	}
}

// Await and WaitForCompletion are aliases for Wait.
func (r *BrowserReplay) Await(ctx context.Context) error { return r.Wait(ctx) }

func (r *BrowserReplay) WaitForCompletion(ctx context.Context) error { return r.Wait(ctx) }

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

// Events returns the finite buffered stream of events accepted by replay.
func (r *BrowserReplay) Events() <-chan FixtureEvent {
	if r == nil {
		closed := make(chan FixtureEvent)
		close(closed)
		return closed
	}
	return r.stream
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

// PendingInvocations is a descriptive alias for PendingInvocationIDs.
func (r *BrowserReplay) PendingInvocations() []string { return r.PendingInvocationIDs() }

// Mode returns the configured replay mode.
func (r *BrowserReplay) Mode() ReplayMode {
	if r == nil {
		return ""
	}
	return r.mode
}

// Script returns an immutable-by-convention copy of the fixture used by the
// replay. Raw page-owned JSON is cloned with the same semantics as runtime
// execution results.
func (r *BrowserReplay) Script() BrowserScript {
	if r == nil {
		return BrowserScript{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneBrowserScript(r.script)
}

// Position returns the number of expected operations/events consumed.
func (r *BrowserReplay) Position() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.position
}
