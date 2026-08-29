package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// output, credentials, or redacted values. The typed context fields contain
// shape-only summaries rather than the original operation or event values.
// Difference is a structural path or a fixed description such as
// "JSON values differ".
type ReplayMismatchError struct {
	Position   int
	Kind       ReplayItemKind
	Path       string
	Expected   string
	Actual     string
	Difference string

	ExpectedOperation *ReplayOperationSummary
	ActualOperation   *ReplayOperationSummary
	ExpectedEvent     *ReplayEventSummary
	ActualEvent       *ReplayEventSummary
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
	var expectedOperation *ReplayOperationSummary
	if r.operationIndex < len(r.script.Operations) {
		copyOf := summarizeOperationExpectation(r.script.Operations[r.operationIndex].Expect)
		expectedOperation = &copyOf
	}
	var expectedEvent *ReplayEventSummary
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		operation := r.script.Operations[r.activeOperation]
		if r.emitIndex < len(operation.Emit) {
			copyOf := summarizeExpectedEvent(operation.Emit[r.emitIndex])
			expectedEvent = &copyOf
		}
	}
	actualCopy := summarizeOperationRequest(request)
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
		Cause:             safeReplayCause(cause),
	}
	return r.failLocked(ReplayDiverged, err)
}

func (r *BrowserReplay) divergeEventLocked(actual FixtureEvent, path, expected, actualLabel string, cause error) error {
	var expectedEvent *ReplayEventSummary
	if r.activeOperation >= 0 && r.activeOperation < len(r.script.Operations) {
		operation := r.script.Operations[r.activeOperation]
		if r.emitIndex < len(operation.Emit) {
			copyOf := summarizeExpectedEvent(operation.Emit[r.emitIndex])
			expectedEvent = &copyOf
		}
	}
	actualCopy := summarizeFixtureEvent(actual)
	err := &ReplayMismatchError{
		Position:      r.position,
		Kind:          ReplayEventItem,
		Path:          path,
		Expected:      expected,
		Actual:        actualLabel,
		Difference:    safeReplayDifference(cause),
		ExpectedEvent: expectedEvent,
		ActualEvent:   &actualCopy,
		Cause:         safeReplayCause(cause),
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
	if errors.Is(err, ErrFixtureClock) {
		return ErrFixtureClock.Error()
	}
	for _, message := range []string{
		"operation type differs",
		"frame ID differs",
		"tool name differs",
		"invocation ID differs",
		"URL differs",
		"JSON values differ",
		"tool catalog length differs",
		"tool name differs",
		"tool description differs",
		"tool frame ID differs",
		"tool input schema differs",
		"tool annotations differ",
		"terminal status differs",
		"terminal output differs",
		"terminal error differs",
		"expected emitted event before next operation",
		"no expected operation remains",
		"an emitted event was not expected",
		"no emitted event remains for the operation",
		"unexpected event after scripted events",
		"replay event stream capacity exhausted",
		"invalid operation",
		"invalid read-only discovery/list request",
	} {
		if err.Error() == message {
			return message
		}
	}
	return "replay values differ"
}

func safeReplayCause(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrFixtureClock) {
		return ErrFixtureClock
	}
	return errors.New(safeReplayDifference(err))
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
