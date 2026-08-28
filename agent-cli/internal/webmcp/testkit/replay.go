package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	// ErrReplayMismatch identifies a browser replay that diverged from the
	// ordered fixture contract.
	ErrReplayMismatch = errors.New("webmcp testkit: replay mismatch")
	// ErrReplayIncomplete identifies a replay closed before all expected
	// operations and events were consumed.
	ErrReplayIncomplete = errors.New("webmcp testkit: replay incomplete")
	// ErrReplayPendingInvocations identifies invocation responses that were not
	// observed before replay completion.
	ErrReplayPendingInvocations = errors.New("webmcp testkit: replay has pending invocations")
	// ErrReplayCanceled identifies replay stopped by a caller context.
	ErrReplayCanceled = errors.New("webmcp testkit: replay canceled")
	// ErrReplayClosed identifies a replay that has already reached a terminal
	// state and cannot accept another operation or event.
	ErrReplayClosed = errors.New("webmcp testkit: replay closed")
	// ErrInvalidReplayRequest identifies a malformed caller operation or event
	// submitted to a replay.
	ErrInvalidReplayRequest = errors.New("webmcp testkit: invalid replay request")
)

// ReplayMode selects the amount of work a browser replay permits outside its
// scripted operation sequence.
type ReplayMode string

const (
	// ReplayStrict requires every operation and event to match the fixture in
	// order. It is the default mode.
	ReplayStrict ReplayMode = "strict"
	// ReplayDiagnostic permits only the fixed read-only discovery/list
	// operation vocabulary in addition to the fixture sequence.
	ReplayDiagnostic ReplayMode = "diagnostic"

	// StrictReplayMode and DiagnosticReplayMode are descriptive aliases.
	StrictReplayMode     = ReplayStrict
	DiagnosticReplayMode = ReplayDiagnostic
	StrictMode           = ReplayStrict
	DiagnosticMode       = ReplayDiagnostic
)

// ReplayStatus is the lifecycle state of a browser replay.
type ReplayStatus string

const (
	ReplayOpen       ReplayStatus = "open"
	ReplayCompleted  ReplayStatus = "completed"
	ReplayDiverged   ReplayStatus = "diverged"
	ReplayIncomplete ReplayStatus = "incomplete"
	ReplayCanceled   ReplayStatus = "canceled"

	// ReplayCancelled retains the spelling used by the provider session
	// replayer while ReplayCanceled matches the testkit's existing status names.
	ReplayCancelled = ReplayCanceled
)

// ReplayItemKind identifies whether the cursor was expecting an operation or
// an emitted neutral event.
type ReplayItemKind string

const (
	ReplayOperationItem ReplayItemKind = "operation"
	ReplayEventItem     ReplayItemKind = "event"
)

// ReplayMismatchError reports safe, inspectable context for a divergence.
// Expected and Actual contain operation or event type names, never raw input,
// output, credentials, or redacted values. Difference is a structural path or
// a fixed description such as "JSON values differ".
type ReplayMismatchError struct {
	Position   int
	Kind       ReplayItemKind
	Path       string
	Expected   string
	Actual     string
	Difference string

	ExpectedOperation *OperationExpectation
	ActualOperation   *OperationRequest
	ExpectedEvent     *EmittedEvent
	ActualEvent       *FixtureEvent
	Cause             error
}

// BrowserReplayMismatchError and FixtureReplayMismatchError are descriptive
// aliases for callers that want a browser-specific error name.
type BrowserReplayMismatchError = ReplayMismatchError
type FixtureReplayMismatchError = ReplayMismatchError

func (e *ReplayMismatchError) Error() string {
	if e == nil {
		return ErrReplayMismatch.Error()
	}
	message := fmt.Sprintf("%s at position %d", ErrReplayMismatch, e.Position)
	if e.Kind != "" {
		message += " (" + string(e.Kind) + ")"
	}
	if e.Path != "" {
		message += "." + e.Path
	}
	if e.Expected != "" || e.Actual != "" {
		message += fmt.Sprintf(": expected %s, actual %s", e.Expected, e.Actual)
	}
	if e.Difference != "" {
		message += ": " + e.Difference
	} else if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *ReplayMismatchError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrReplayMismatch
	}
	return errors.Join(ErrReplayMismatch, e.Cause)
}

// Is keeps the sentinel classification stable through wrapped causes.
func (e *ReplayMismatchError) Is(target error) bool {
	return e != nil && target == ErrReplayMismatch
}

// ReplayIncompleteError reports the expected cursor and remaining work when a
// replay is closed before successful completion.
type ReplayIncompleteError struct {
	Position            int
	Expected            string
	Actual              string
	OperationsRemaining int
	EventsRemaining     int
	Pending             []string
	Cause               error
}

// BrowserReplayIncompleteError and FixtureReplayIncompleteError are
// descriptive aliases for callers that want a browser-specific error name.
type BrowserReplayIncompleteError = ReplayIncompleteError
type FixtureReplayIncompleteError = ReplayIncompleteError

func (e *ReplayIncompleteError) Error() string {
	if e == nil {
		return ErrReplayIncomplete.Error()
	}
	message := fmt.Sprintf("%s at position %d", ErrReplayIncomplete, e.Position)
	if e.Expected != "" || e.Actual != "" {
		message += fmt.Sprintf(": expected %s, actual %s", e.Expected, e.Actual)
	}
	if e.OperationsRemaining > 0 || e.EventsRemaining > 0 {
		message += fmt.Sprintf(": %d operation(s) and %d event(s) remain", e.OperationsRemaining, e.EventsRemaining)
	}
	if len(e.Pending) > 0 {
		message += fmt.Sprintf("; %d pending invocation(s)", len(e.Pending))
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *ReplayIncompleteError) Unwrap() error {
	if e == nil {
		return ErrReplayIncomplete
	}
	if e.Cause == nil {
		return ErrReplayIncomplete
	}
	return errors.Join(ErrReplayIncomplete, e.Cause)
}

func (e *ReplayIncompleteError) Is(target error) bool {
	return e != nil && (target == ErrReplayIncomplete || target == ErrReplayPendingInvocations && len(e.Pending) > 0)
}

// ReplayCanceledError identifies cancellation while preserving the caller's
// context.Canceled or context.DeadlineExceeded cause.
type ReplayCanceledError struct {
	Position int
	Cause    error
}

func (e *ReplayCanceledError) Error() string {
	if e == nil {
		return ErrReplayCanceled.Error()
	}
	message := fmt.Sprintf("%s at position %d", ErrReplayCanceled, e.Position)
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *ReplayCanceledError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrReplayCanceled
	}
	return errors.Join(ErrReplayCanceled, e.Cause)
}

func (e *ReplayCanceledError) Is(target error) bool {
	return e != nil && target == ErrReplayCanceled
}

// ReplayOutcome is an inspectable snapshot of a browser replay lifecycle.
// Expected and Actual are safe type labels; detailed structural location is in
// Path and the associated typed error.
type ReplayOutcome struct {
	Status              ReplayStatus
	Err                 error
	Position            int
	Expected            string
	Actual              string
	Path                string
	OperationsRemaining int
	EventsRemaining     int
	Pending             []string
	IgnoredOperations   []OperationRequest
}

// BrowserReplayOutcome is a descriptive alias.
type BrowserReplayOutcome = ReplayOutcome

// OK reports whether replay consumed all expected work without divergence,
// incompletion, or cancellation.
func (o ReplayOutcome) OK() bool { return o.Status == ReplayCompleted }

// ReplayOption configures BrowserReplay.
type ReplayOption func(*BrowserReplay)

// WithReplayMode selects strict or diagnostic matching.
func WithReplayMode(mode ReplayMode) ReplayOption {
	return func(replay *BrowserReplay) { replay.mode = mode }
}

// WithStrictReplay selects exact ordered matching.
func WithStrictReplay() ReplayOption { return WithReplayMode(ReplayStrict) }

// WithDiagnosticReplay permits fixed read-only discovery/list operations.
func WithDiagnosticReplay() ReplayOption { return WithReplayMode(ReplayDiagnostic) }

// WithReplayStrictness is an alias for WithReplayMode.
func WithReplayStrictness(strict bool) ReplayOption {
	if strict {
		return WithStrictReplay()
	}
	return WithDiagnosticReplay()
}

// WithReplayContext supplies a context that is checked by replay calls and
// Wait. It does not start a watcher goroutine.
func WithReplayContext(ctx context.Context) ReplayOption {
	return func(replay *BrowserReplay) {
		if ctx != nil {
			replay.replayContext = ctx
		}
	}
}

// WithReplayClock injects the monotonic clock used for generated executions
// and observations.
func WithReplayClock(clock Clock) ReplayOption {
	return func(replay *BrowserReplay) {
		if clock != nil {
			replay.clock = clock
		}
	}
}

// WithReplayClockFunc injects a function-backed replay clock.
func WithReplayClockFunc(clock func() uint64) ReplayOption {
	return WithReplayClock(ClockFunc(clock))
}

// WithReplayIDSource injects deterministic invocation IDs.
func WithReplayIDSource(source IDSource) ReplayOption {
	return func(replay *BrowserReplay) {
		if source != nil {
			replay.ids = source
		}
	}
}

// WithReplayIDFunc injects a function-backed deterministic ID source.
func WithReplayIDFunc(source func(string) string) ReplayOption {
	return WithReplayIDSource(IDSourceFunc(source))
}

// WithReplayBrowserID sets the browser context used by generated events.
func WithReplayBrowserID(browserID string) ReplayOption {
	return func(replay *BrowserReplay) {
		if strings.TrimSpace(browserID) != "" {
			replay.browserID = browserID
		}
	}
}

// WithReplayTargetID selects the endpoint target used by generated events.
func WithReplayTargetID(targetID string) ReplayOption {
	return func(replay *BrowserReplay) {
		if strings.TrimSpace(targetID) != "" {
			replay.targetID = targetID
		}
	}
}

// BrowserReplay verifies caller operations and neutral observations against a
// BrowserScript. It is synchronous and owns no goroutines. ObserveOperation
// consumes an operation and leaves its declared emitted events pending for
// ObserveEvent. Execute is the fixture-friendly form: it consumes the
// operation and its declared events, returning the scripted execution.
type BrowserReplay struct {
	mu sync.Mutex

	script          BrowserScript
	mode            ReplayMode
	clock           Clock
	ids             IDSource
	replayContext   context.Context
	browserID       string
	targetID        string
	generation      uint64
	target          BrowserTarget
	operationIndex  int
	activeOperation int
	emitIndex       int
	position        int
	pending         map[string]struct{}
	ignored         []OperationRequest
	observed        []FixtureEvent
	done            chan struct{}
	stream          chan FixtureEvent
	closed          bool
	outcome         ReplayOutcome
	lastClock       uint64
	hasClock        bool

	detached     bool
	targetClosed bool
}

// Replay and BrowserScriptReplay are descriptive aliases.
type Replay = BrowserReplay
type BrowserScriptReplay = BrowserReplay
type ScriptReplay = BrowserReplay

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

func (r *BrowserReplay) prepareLocked(ctx context.Context) error {
	if r.closed {
		if r.outcome.Err != nil {
			return r.outcome.Err
		}
		return ErrReplayClosed
	}
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidReplayRequest)
	}
	if err := ctx.Err(); err != nil {
		return r.cancelLocked(err)
	}
	if r.replayContext != nil {
		if err := r.replayContext.Err(); err != nil {
			return r.cancelLocked(err)
		}
	}
	return nil
}

func (r *BrowserReplay) matchOperationLocked(request OperationRequest) (RuntimeExecution, error) {
	if err := validateOperationRequest(request); err != nil {
		return RuntimeExecution{}, r.divergeOperationLocked(request, "request", requestTypeLabel(request), "invalid operation", err)
	}
	if r.activeOperation >= 0 {
		return RuntimeExecution{}, r.divergeOperationLocked(request, "order", r.expectedEventLabelLocked(), requestTypeLabel(request), errors.New("expected emitted event before next operation"))
	}
	if r.operationIndex >= len(r.script.Operations) {
		return RuntimeExecution{}, r.divergeOperationLocked(request, "order", "end", requestTypeLabel(request), errors.New("no expected operation remains"))
	}
	expected := r.script.Operations[r.operationIndex].Expect
	if path, difference := compareReplayOperation(expected, request); difference != "" {
		return RuntimeExecution{}, r.divergeOperationLocked(request, path, string(expected.Type), string(request.Type), errors.New(difference))
	}

	now := r.clock.MonotonicMillis()
	if r.hasClock && now < r.lastClock {
		return RuntimeExecution{}, r.divergeOperationLocked(request, "monotonic_ms", string(expected.Type), string(request.Type), fmt.Errorf("%w: previous=%d current=%d", ErrFixtureClock, r.lastClock, now))
	}
	r.hasClock = true
	r.lastClock = now

	operation := r.script.Operations[r.operationIndex]
	r.operationIndex++
	r.position++
	r.activeOperation = -1
	r.emitIndex = 0

	execution := RuntimeExecution{
		Request:     cloneOperationRequest(request),
		Result:      cloneRaw(operation.Result),
		BrowserID:   r.browserID,
		TargetID:    r.targetID,
		Generation:  r.generation,
		MonotonicMS: now,
	}
	if request.Type == OperationInvokeTool {
		invocationID, err := r.invocationIDForResult(operation.Result)
		if err != nil {
			return RuntimeExecution{}, r.divergeOperationLocked(request, "result", string(expected.Type), string(request.Type), err)
		}
		if _, exists := r.pending[invocationID]; exists {
			return RuntimeExecution{}, r.divergeOperationLocked(request, "result.invocation_id", string(expected.Type), string(request.Type), errors.New("invocation ID is already pending"))
		}
		execution.InvocationID = invocationID
		r.pending[invocationID] = struct{}{}
		if len(execution.Result) == 0 {
			execution.Result = MustJSONValue(map[string]any{"invocation_id": invocationID})
		}
	}
	if request.Type == OperationNavigate {
		if expected.URL != "" {
			r.target.URL = expected.URL
		}
		r.generation++
		execution.Generation = r.generation
	}
	if request.Type == OperationDetachTarget {
		r.detached = true
	}
	if request.Type == OperationCloseTarget {
		r.targetClosed = true
	}
	if len(operation.Emit) > 0 {
		r.activeOperation = r.operationIndex - 1
	}
	execution.Events = make([]FixtureEvent, 0, len(operation.Emit))
	for _, emitted := range operation.Emit {
		execution.Events = append(execution.Events, r.fixtureEventLocked(emitted, now))
	}
	return execution, nil
}

func (r *BrowserReplay) fixtureEventLocked(emitted EmittedEvent, now uint64) FixtureEvent {
	return FixtureEvent{
		Type:         emitted.Type,
		MonotonicMS:  now,
		BrowserID:    r.browserID,
		TargetID:     r.targetID,
		Generation:   r.generation,
		Tools:        cloneToolDescriptors(emitted.Tools),
		InvocationID: emitted.InvocationID,
		Status:       emitted.Status,
		Output:       cloneRaw(emitted.Output),
		Error:        cloneRaw(emitted.Error),
	}
}

func (r *BrowserReplay) invocationIDForResult(result json.RawMessage) (string, error) {
	if len(result) > 0 {
		fields, err := decodeJSONObject(result)
		if err != nil {
			return "", err
		}
		if raw, ok := fields["invocation_id"]; ok {
			id, err := parseScriptString(raw)
			if err != nil {
				return "", err
			}
			if err := validateScriptID(id); err != nil {
				return "", err
			}
			return id, nil
		}
	}
	if r.ids == nil {
		return "", errors.New("invocation ID source is unavailable")
	}
	id := r.ids.NextID("invocation")
	if err := validateScriptID(id); err != nil {
		return "", fmt.Errorf("generated invocation ID: %w", err)
	}
	return id, nil
}

func (r *BrowserReplay) matchEventLocked(actual FixtureEvent) error {
	if r.activeOperation < 0 || r.activeOperation >= len(r.script.Operations) {
		return r.divergeEventLocked(actual, "order", r.expectedEventLabelLocked(), eventTypeLabel(actual), errors.New("an emitted event was not expected"))
	}
	operation := r.script.Operations[r.activeOperation]
	if r.emitIndex >= len(operation.Emit) {
		return r.divergeEventLocked(actual, "order", "next operation", eventTypeLabel(actual), errors.New("no emitted event remains for the operation"))
	}
	expected := operation.Emit[r.emitIndex]
	if path, difference := compareReplayEvent(expected, actual, r); difference != "" {
		return r.divergeEventLocked(actual, path, string(expected.Type), eventTypeLabel(actual), errors.New(difference))
	}
	if expected.Type == EmittedToolResponded {
		if _, ok := r.pending[expected.InvocationID]; !ok {
			return r.divergeEventLocked(actual, "invocation_id", string(expected.Type), eventTypeLabel(actual), errors.New("terminal response has no pending invocation"))
		}
		delete(r.pending, expected.InvocationID)
	}
	r.observed = append(r.observed, cloneFixtureEvent(actual))
	select {
	case r.stream <- cloneFixtureEvent(actual):
	default:
		return r.divergeEventLocked(actual, "events", string(expected.Type), eventTypeLabel(actual), errors.New("replay event stream capacity exhausted"))
	}
	r.emitIndex++
	r.position++
	if r.emitIndex == len(operation.Emit) {
		r.activeOperation = -1
		r.emitIndex = 0
	}
	return nil
}

func (r *BrowserReplay) divergeOperationLocked(request OperationRequest, path, expected, actual string, cause error) error {
	var expectedOperation *OperationExpectation
	if r.operationIndex < len(r.script.Operations) {
		copyOf := r.script.Operations[r.operationIndex].Expect
		expectedOperation = &copyOf
	}
	var expectedEvent *EmittedEvent
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		operation := r.script.Operations[r.activeOperation]
		if r.emitIndex < len(operation.Emit) {
			copyOf := operation.Emit[r.emitIndex]
			expectedEvent = &copyOf
		}
	}
	actualCopy := cloneOperationRequest(request)
	err := &ReplayMismatchError{
		Position:          r.position,
		Kind:              ReplayOperationItem,
		Path:              path,
		Expected:          expected,
		Actual:            actual,
		Difference:        safeReplayDifference(cause),
		ExpectedOperation: expectedOperation,
		ActualOperation:   &actualCopy,
		ExpectedEvent:     expectedEvent,
		Cause:             cause,
	}
	return r.failLocked(ReplayDiverged, err)
}

func (r *BrowserReplay) divergeEventLocked(actual FixtureEvent, path, expected, actualLabel string, cause error) error {
	var expectedEvent *EmittedEvent
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		operation := r.script.Operations[r.activeOperation]
		if r.emitIndex < len(operation.Emit) {
			copyOf := operation.Emit[r.emitIndex]
			expectedEvent = &copyOf
		}
	}
	actualCopy := cloneFixtureEvent(actual)
	err := &ReplayMismatchError{
		Position:      r.position,
		Kind:          ReplayEventItem,
		Path:          path,
		Expected:      expected,
		Actual:        actualLabel,
		Difference:    safeReplayDifference(cause),
		ExpectedEvent: expectedEvent,
		ActualEvent:   &actualCopy,
		Cause:         cause,
	}
	return r.failLocked(ReplayDiverged, err)
}

func (r *BrowserReplay) incompleteLocked(actual string) error {
	remainingOperations, remainingEvents := r.remainingLocked()
	causes := make([]error, 0, 1)
	if len(r.pending) > 0 {
		causes = append(causes, ErrReplayPendingInvocations)
	}
	var cause error
	if len(causes) == 1 {
		cause = causes[0]
	}
	err := &ReplayIncompleteError{
		Position:            r.position,
		Expected:            r.expectedDescriptionLocked(),
		Actual:              actual,
		OperationsRemaining: remainingOperations,
		EventsRemaining:     remainingEvents,
		Pending:             pendingIDs(r.pending),
		Cause:               cause,
	}
	return r.failLocked(ReplayIncomplete, err)
}

func (r *BrowserReplay) cancelFromContext(cause error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.outcome.Err
	}
	return r.cancelLocked(cause)
}

func (r *BrowserReplay) cancelLocked(cause error) error {
	err := &ReplayCanceledError{Position: r.position, Cause: cause}
	return r.failLocked(ReplayCanceled, err)
}

func (r *BrowserReplay) failLocked(status ReplayStatus, err error) error {
	if r.closed {
		if r.outcome.Err != nil {
			return r.outcome.Err
		}
		return err
	}
	r.closed = true
	r.outcome = r.outcomeLockedWith(status, err)
	close(r.stream)
	close(r.done)
	return err
}

func (r *BrowserReplay) completeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	r.outcome = r.outcomeLockedWith(ReplayCompleted, nil)
	close(r.stream)
	close(r.done)
}

func (r *BrowserReplay) maybeCompleteLocked() {
	if !r.closed && r.operationIndex == len(r.script.Operations) && r.activeOperation < 0 && len(r.pending) == 0 {
		r.completeLocked()
	}
}

func (r *BrowserReplay) outcomeLocked() ReplayOutcome {
	if r.outcome.Status == "" {
		return r.outcomeLockedWith(ReplayOpen, nil)
	}
	result := r.outcome
	result.Pending = pendingIDs(r.pending)
	result.IgnoredOperations = cloneOperationRequests(r.ignored)
	if result.Status == ReplayOpen {
		result.OperationsRemaining, result.EventsRemaining = r.remainingLocked()
	}
	return result
}

func (r *BrowserReplay) outcomeLockedWith(status ReplayStatus, err error) ReplayOutcome {
	result := ReplayOutcome{
		Status:              status,
		Err:                 err,
		Position:            r.position,
		OperationsRemaining: 0,
		EventsRemaining:     0,
		Pending:             pendingIDs(r.pending),
		IgnoredOperations:   cloneOperationRequests(r.ignored),
	}
	if status == ReplayOpen || status == ReplayIncomplete {
		result.OperationsRemaining, result.EventsRemaining = r.remainingLocked()
	}
	if err != nil {
		var mismatch *ReplayMismatchError
		if errors.As(err, &mismatch) {
			result.Expected = mismatch.Expected
			result.Actual = mismatch.Actual
			result.Path = mismatch.Path
		}
		var incomplete *ReplayIncompleteError
		if errors.As(err, &incomplete) {
			result.Expected = incomplete.Expected
			result.Actual = incomplete.Actual
		}
	}
	return result
}

func (r *BrowserReplay) remainingLocked() (int, int) {
	operations := len(r.script.Operations) - r.operationIndex
	events := 0
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		events += len(r.script.Operations[r.activeOperation].Emit) - r.emitIndex
	}
	for index := r.operationIndex; index < len(r.script.Operations); index++ {
		events += len(r.script.Operations[index].Emit)
	}
	return operations, events
}

func (r *BrowserReplay) expectedDescriptionLocked() string {
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		operation := r.script.Operations[r.activeOperation]
		if r.emitIndex < len(operation.Emit) {
			return "event " + string(operation.Emit[r.emitIndex].Type)
		}
	}
	if r.operationIndex < len(r.script.Operations) {
		return "operation " + string(r.script.Operations[r.operationIndex].Expect.Type)
	}
	return "end"
}

func (r *BrowserReplay) expectedEventLabelLocked() string {
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		operation := r.script.Operations[r.activeOperation]
		if r.emitIndex < len(operation.Emit) {
			return "event " + string(operation.Emit[r.emitIndex].Type)
		}
	}
	return "event"
}

func requestTypeLabel(request OperationRequest) string {
	if request.Type == "" {
		return "empty operation"
	}
	return "operation " + string(request.Type)
}

func eventTypeLabel(event FixtureEvent) string {
	if event.Type == "" {
		return "empty event"
	}
	return "event " + string(event.Type)
}

func safeReplayDifference(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if message == "" {
		return "replay values differ"
	}
	return message
}

func replayContextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

func replayEventValue(value any) (FixtureEvent, error) {
	switch event := value.(type) {
	case FixtureEvent:
		return cloneFixtureEvent(event), nil
	case *FixtureEvent:
		if event == nil {
			return FixtureEvent{}, errors.New("event is nil")
		}
		return cloneFixtureEvent(*event), nil
	case EmittedEvent:
		if err := event.Validate(); err != nil {
			return FixtureEvent{}, err
		}
		return FixtureEvent{
			Type:         event.Type,
			Tools:        cloneToolDescriptors(event.Tools),
			InvocationID: event.InvocationID,
			Status:       event.Status,
			Output:       cloneRaw(event.Output),
			Error:        cloneRaw(event.Error),
		}, nil
	case *EmittedEvent:
		if event == nil {
			return FixtureEvent{}, errors.New("event is nil")
		}
		return replayEventValue(*event)
	default:
		return FixtureEvent{}, fmt.Errorf("event must be a FixtureEvent or EmittedEvent, got %T", value)
	}
}

func compareReplayOperation(expected OperationExpectation, actual OperationRequest) (string, string) {
	if expected.Type != actual.Type {
		return "type", "operation type differs"
	}
	switch expected.Type {
	case OperationInvokeTool:
		if expected.FrameID != actual.FrameID {
			return "frame_id", "frame ID differs"
		}
		if expected.ToolName != actual.ToolName {
			return "tool_name", "tool name differs"
		}
		actualInput := actual.Input
		if len(actualInput) == 0 {
			actualInput = json.RawMessage(`{}`)
		}
		if difference := replayJSONDifferenceAt(expected.Input, actualInput, "input"); difference != "" {
			return difference, "JSON values differ"
		}
	case OperationCancelTool:
		if expected.InvocationID != actual.InvocationID {
			return "invocation_id", "invocation ID differs"
		}
	case OperationNavigate:
		if expected.URL != actual.URL {
			return "url", "URL differs"
		}
	}
	return "", ""
}

func compareReplayEvent(expected EmittedEvent, actual FixtureEvent, replay *BrowserReplay) (string, string) {
	if expected.Type != actual.Type {
		return "type", "event type differs"
	}
	if actual.BrowserID != "" && actual.BrowserID != replay.browserID {
		return "browser_id", "browser ID differs"
	}
	if actual.TargetID != "" && actual.TargetID != replay.targetID {
		return "target_id", "target ID differs"
	}
	if actual.Generation != 0 && actual.Generation != replay.generation {
		return "generation", "generation differs"
	}
	if actual.MonotonicMS != 0 && actual.MonotonicMS != replay.lastClock {
		return "monotonic_ms", "monotonic time differs"
	}
	switch expected.Type {
	case EmittedToolsAdded:
		if path, difference := compareReplayTools(expected.Tools, actual.Tools); difference != "" {
			return path, difference
		}
	case EmittedToolResponded:
		if expected.InvocationID != actual.InvocationID {
			return "invocation_id", "invocation ID differs"
		}
		if expected.Status != actual.Status {
			return "status", "terminal status differs"
		}
		if difference := replayJSONDifferenceAt(expected.Output, actual.Output, "output"); difference != "" {
			return difference, "terminal output differs"
		}
		if difference := replayJSONDifferenceAt(expected.Error, actual.Error, "error"); difference != "" {
			return difference, "terminal error differs"
		}
	}
	return "", ""
}

func compareReplayTools(expected, actual []ToolDescriptor) (string, string) {
	if len(expected) != len(actual) {
		return "tools", "tool catalog length differs"
	}
	for index := range expected {
		left, right := expected[index], actual[index]
		prefix := fmt.Sprintf("tools[%d]", index)
		if left.Name != right.Name {
			return prefix + ".name", "tool name differs"
		}
		if left.Description != right.Description {
			return prefix + ".description", "tool description differs"
		}
		if left.FrameID != right.FrameID {
			return prefix + ".frame_id", "tool frame ID differs"
		}
		if difference := replayJSONDifferenceAt(left.InputSchema, right.InputSchema, prefix+".input_schema"); difference != "" {
			return difference, "tool input schema differs"
		}
		if difference := replayJSONDifferenceAt(left.Annotations, right.Annotations, prefix+".annotations"); difference != "" {
			return difference, "tool annotations differ"
		}
	}
	return "", ""
}

func replayJSONDifference(left, right json.RawMessage) string {
	return replayJSONDifferenceAt(left, right, "value")
}

func replayJSONDifferenceAt(left, right json.RawMessage, path string) string {
	if len(left) == 0 && len(right) == 0 {
		return ""
	}
	if len(left) == 0 || len(right) == 0 {
		return path
	}
	leftValue, err := decodeJSONNumberValue(left)
	if err != nil {
		return path
	}
	rightValue, err := decodeJSONNumberValue(right)
	if err != nil {
		return path
	}
	return replayValueDifference(leftValue, rightValue, path)
}

func replayValueDifference(left, right any, path string) string {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok {
			return path
		}
		keys := make([]string, 0, len(leftValue)+len(rightValue))
		seen := make(map[string]struct{}, len(leftValue)+len(rightValue))
		for key := range leftValue {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		for key := range rightValue {
			if _, ok := seen[key]; !ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			leftChild, leftOK := leftValue[key]
			rightChild, rightOK := rightValue[key]
			childPath := replayJSONFieldPath(path, key)
			if !leftOK || !rightOK {
				return childPath
			}
			if difference := replayValueDifference(leftChild, rightChild, childPath); difference != "" {
				return difference
			}
		}
		return ""
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return path
		}
		for index := range leftValue {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if difference := replayValueDifference(leftValue[index], rightValue[index], childPath); difference != "" {
				return difference
			}
		}
		return ""
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok || leftValue.String() != rightValue.String() {
			return path
		}
		return ""
	default:
		if !reflect.DeepEqual(left, right) {
			return path
		}
		return ""
	}
}

func replayJSONFieldPath(base, key string) string {
	if key == "" {
		return base + `[""]`
	}
	for index, char := range key {
		if !(char == '_' || char == '-' || char == '.' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9') {
			return base + "[" + strconv.Quote(key) + "]"
		}
	}
	return base + "." + key
}

// OperationDiscover and the list constants are diagnostic-only caller
// operation names. They are deliberately not included in the frozen
// browser-script.v1 operation vocabulary.
const (
	OperationDiscover           OperationType = "discover"
	OperationList               OperationType = "list"
	OperationListTargets        OperationType = "list_targets"
	OperationListTools          OperationType = "list_tools"
	OperationBrowserDiscover    OperationType = "browser_discover"
	OperationBrowserListTargets OperationType = "browser_list_targets"
	OperationBrowserListTools   OperationType = "browser_list_tools"
	OperationDoctor             OperationType = "doctor"
	OperationContext            OperationType = "context"
	OperationBrowsers           OperationType = "browsers"
	OperationTabs               OperationType = "tabs"
	OperationTools              OperationType = "tools"
)

var diagnosticReadOnlyOperationTypes = map[OperationType]struct{}{
	OperationDiscover:           {},
	OperationList:               {},
	OperationListTargets:        {},
	OperationListTools:          {},
	OperationBrowserDiscover:    {},
	OperationBrowserListTargets: {},
	OperationBrowserListTools:   {},
	OperationDoctor:             {},
	OperationContext:            {},
	OperationBrowsers:           {},
	OperationTabs:               {},
	OperationTools:              {},
}

// IsDiagnosticReadOnlyOperation reports whether request belongs to the fixed
// discovery/list vocabulary. Shape validation is intentionally separate.
func IsDiagnosticReadOnlyOperation(request OperationRequest) bool {
	_, ok := diagnosticReadOnlyOperationTypes[request.Type]
	return ok
}

func isDiagnosticReadOnlyOperation(request OperationRequest) bool {
	return IsDiagnosticReadOnlyOperation(request)
}

func validateDiagnosticReadOnlyRequest(request OperationRequest) error {
	if !IsDiagnosticReadOnlyOperation(request) {
		return errors.New("operation is not a diagnostic discovery/list operation")
	}
	if request.FrameID != "" || request.ToolName != "" || request.Input != nil || request.InvocationID != "" || request.URL != "" {
		return errors.New("read-only discovery/list operation does not accept arguments")
	}
	return nil
}

func cloneOperationRequests(requests []OperationRequest) []OperationRequest {
	if requests == nil {
		return nil
	}
	result := make([]OperationRequest, len(requests))
	for index, request := range requests {
		result[index] = cloneOperationRequest(request)
	}
	return result
}

func cloneBrowserScript(script BrowserScript) BrowserScript {
	result := script
	result.Endpoint.Targets = append([]BrowserTarget(nil), script.Endpoint.Targets...)
	result.Operations = make([]BrowserScriptOperation, len(script.Operations))
	for index, operation := range script.Operations {
		result.Operations[index] = operation
		result.Operations[index].Expect.Input = cloneRaw(operation.Expect.Input)
		result.Operations[index].Result = cloneRaw(operation.Result)
		result.Operations[index].Emit = make([]EmittedEvent, len(operation.Emit))
		for emitIndex, emitted := range operation.Emit {
			result.Operations[index].Emit[emitIndex] = emitted
			result.Operations[index].Emit[emitIndex].Tools = cloneToolDescriptors(emitted.Tools)
			result.Operations[index].Emit[emitIndex].Output = cloneRaw(emitted.Output)
			result.Operations[index].Emit[emitIndex].Error = cloneRaw(emitted.Error)
		}
	}
	return result
}

// LoadReplayScriptReader is the reader form of LoadReplayScriptFile.
func LoadReplayScriptReader(reader io.Reader) (BrowserScript, error) {
	return LoadBrowserScriptReader(reader)
}
