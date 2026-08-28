package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type RuntimeOption func(*ScriptedBrowserRuntime)

// WithFixtureClock injects a monotonic clock for neutral observations.
func WithFixtureClock(clock Clock) RuntimeOption {
	return func(runtime *ScriptedBrowserRuntime) {
		if clock != nil {
			runtime.clock = clock
		}
	}
}

// WithRuntimeClock is a descriptive alias for WithFixtureClock.
func WithRuntimeClock(clock Clock) RuntimeOption { return WithFixtureClock(clock) }

// WithFixtureClockFunc injects a function-backed monotonic clock.
func WithFixtureClockFunc(clock func() uint64) RuntimeOption {
	return WithFixtureClock(ClockFunc(clock))
}

// WithFixtureIDSource injects deterministic invocation IDs.
func WithFixtureIDSource(source IDSource) RuntimeOption {
	return func(runtime *ScriptedBrowserRuntime) {
		if source != nil {
			runtime.ids = source
		}
	}
}

// WithRuntimeIDSource is a descriptive alias for WithFixtureIDSource.
func WithRuntimeIDSource(source IDSource) RuntimeOption { return WithFixtureIDSource(source) }

// WithFixtureIDFunc injects a function-backed deterministic ID source.
func WithFixtureIDFunc(source func(string) string) RuntimeOption {
	return WithFixtureIDSource(IDSourceFunc(source))
}

// WithFixtureBrowserID sets the opaque browser ID used in neutral events.
func WithFixtureBrowserID(browserID string) RuntimeOption {
	return func(runtime *ScriptedBrowserRuntime) {
		if strings.TrimSpace(browserID) != "" {
			runtime.browserID = browserID
		}
	}
}

// WithFixtureTargetID selects a target by ID. Without this option the first
// endpoint target is selected.
func WithFixtureTargetID(targetID string) RuntimeOption {
	return func(runtime *ScriptedBrowserRuntime) {
		if strings.TrimSpace(targetID) != "" {
			runtime.targetID = targetID
		}
	}
}

// WithStateOracle supplies the out-of-band page state store. Scripted
// responses never update this store implicitly.
func WithStateOracle(oracle *FixtureStateOracle) RuntimeOption {
	return func(runtime *ScriptedBrowserRuntime) {
		if oracle != nil {
			runtime.state = oracle
		}
	}
}

// WithFixtureState creates an out-of-band state oracle from a JSON value.
func WithFixtureState(value any) RuntimeOption {
	return func(runtime *ScriptedBrowserRuntime) {
		oracle, err := NewFixtureStateOracle(value)
		if err == nil {
			runtime.state = oracle
		} else {
			runtime.optionErr = err
		}
	}
}

// ScriptedBrowserRuntime is a synchronous, browser-independent fixture
// runtime. It intentionally has no goroutines, sockets, sleeps, or implicit
// state mutation.
type ScriptedBrowserRuntime struct {
	mu sync.Mutex

	script    BrowserScript
	clock     Clock
	ids       IDSource
	state     *FixtureStateOracle
	browserID string
	targetID  string
	target    BrowserTarget
	optionErr error

	generation uint64
	position   int
	pending    map[string]struct{}
	tools      []ToolDescriptor
	events     []FixtureEvent
	last       RuntimeExecution
	lastClock  uint64
	hasClock   bool
	closed     bool
	outcome    BrowserScriptOutcome
	done       chan struct{}
	stream     chan FixtureEvent

	enabledLifecycle bool
	enabledWebMCP    bool
	detached         bool
	targetClosed     bool
}

// Runtime, FixtureRuntime, and BrowserRuntime are aliases for the scripted
// runtime implementation.
type Runtime = ScriptedBrowserRuntime
type FixtureRuntime = ScriptedBrowserRuntime
type BrowserRuntime = ScriptedBrowserRuntime
type ScriptedRuntime = ScriptedBrowserRuntime

// NewScriptedBrowserRuntime validates a script and constructs a synchronous
// runtime. No operation is executed during construction.
func NewScriptedBrowserRuntime(script BrowserScript, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	if err := script.Validate(); err != nil {
		return nil, err
	}
	runtime := &ScriptedBrowserRuntime{
		script:     script,
		clock:      ClockFunc(func() uint64 { return 0 }),
		ids:        NewDeterministicIDSource("fixture"),
		browserID:  "fixture-browser",
		generation: 1,
		pending:    make(map[string]struct{}),
		done:       make(chan struct{}),
		stream:     make(chan FixtureEvent, countScriptEvents(script)),
		state:      mustNewDefaultStateOracle(),
		outcome:    BrowserScriptOutcome{Status: BrowserScriptOpen},
	}
	for _, option := range options {
		if option != nil {
			option(runtime)
		}
	}
	if runtime.optionErr != nil {
		return nil, runtime.optionErr
	}
	if err := validateScriptID(runtime.browserID); err != nil {
		return nil, newScriptError("browser_id", "%v", err)
	}
	if runtime.targetID == "" && len(script.Endpoint.Targets) > 0 {
		runtime.targetID = script.Endpoint.Targets[0].ID
	}
	if runtime.targetID != "" {
		for _, target := range script.Endpoint.Targets {
			if target.ID == runtime.targetID {
				runtime.target = target
				break
			}
		}
		if runtime.target.ID == "" {
			return nil, newScriptError("endpoint.targets", "target %q was not found", runtime.targetID)
		}
	}
	if len(script.Operations) == 0 {
		runtime.completeLocked()
	}
	return runtime, nil
}

// NewBrowserScriptRuntime is an alias for NewScriptedBrowserRuntime.
func NewBrowserScriptRuntime(script BrowserScript, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	return NewScriptedBrowserRuntime(script, options...)
}

// NewFixtureRuntime is an alias for NewScriptedBrowserRuntime.
func NewFixtureRuntime(script BrowserScript, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	return NewScriptedBrowserRuntime(script, options...)
}

// NewScriptRuntime is an alias for NewScriptedBrowserRuntime.
func NewScriptRuntime(script BrowserScript, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	return NewScriptedBrowserRuntime(script, options...)
}

// NewScriptedRuntime is an alias for NewScriptedBrowserRuntime.
func NewScriptedRuntime(script BrowserScript, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	return NewScriptedBrowserRuntime(script, options...)
}

// NewBrowserRuntime is an alias for NewScriptedBrowserRuntime.
func NewBrowserRuntime(script BrowserScript, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	return NewScriptedBrowserRuntime(script, options...)
}

// NewRuntime accepts either a BrowserScript value or pointer for convenient
// use by callers that load a script through a pointer-oriented helper.
func NewRuntime(value any, options ...RuntimeOption) (*ScriptedBrowserRuntime, error) {
	switch script := value.(type) {
	case BrowserScript:
		return NewScriptedBrowserRuntime(script, options...)
	case *BrowserScript:
		if script == nil {
			return nil, newScriptError("script", "is nil")
		}
		return NewScriptedBrowserRuntime(*script, options...)
	default:
		return nil, newScriptError("script", "must be a BrowserScript")
	}
}

func countScriptEvents(script BrowserScript) int {
	count := 0
	for _, operation := range script.Operations {
		count += len(operation.Emit)
	}
	return count
}

func mustNewDefaultStateOracle() *FixtureStateOracle {
	oracle, err := NewFixtureStateOracle(map[string]any{})
	if err != nil {
		panic(err)
	}
	return oracle
}

// Execute consumes exactly the next expected operation and returns its
// scripted result plus neutral observations. A mismatch permanently diverges
// the runtime before the next operation can be attempted.
func (r *ScriptedBrowserRuntime) Execute(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	if r == nil {
		return RuntimeExecution{}, ErrFixtureClosed
	}
	if ctx == nil {
		return RuntimeExecution{}, fmt.Errorf("%w: nil context", ErrInvalidFixtureOperation)
	}
	if err := ctx.Err(); err != nil {
		return r.cancel(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		if r.outcome.Err != nil {
			return RuntimeExecution{}, r.outcome.Err
		}
		return RuntimeExecution{}, ErrFixtureClosed
	}
	if err := validateOperationRequest(request); err != nil {
		return RuntimeExecution{}, r.failLocked(BrowserScriptDiverged, &FixtureOperationError{
			Kind:     ErrInvalidFixtureOperation,
			Position: r.position,
			Path:     "request",
			Actual:   request,
			Cause:    err,
		})
	}
	if r.position >= len(r.script.Operations) {
		return RuntimeExecution{}, r.failLocked(BrowserScriptDiverged, &FixtureOperationError{
			Kind:     ErrFixtureOperationMismatch,
			Position: r.position,
			Path:     "type",
			Actual:   request,
			Cause:    errors.New("no expected operation remains"),
		})
	}
	expected := r.script.Operations[r.position].Expect
	if path, detail := compareOperation(expected, request); detail != nil {
		return RuntimeExecution{}, r.failLocked(BrowserScriptDiverged, &FixtureOperationError{
			Kind:     ErrFixtureOperationMismatch,
			Position: r.position,
			Path:     path,
			Expected: expected,
			Actual:   request,
			Cause:    detail,
		})
	}
	operation := r.script.Operations[r.position]
	r.position++
	now := r.clock.MonotonicMillis()
	if r.hasClock && now < r.lastClock {
		return RuntimeExecution{}, r.failLocked(BrowserScriptDiverged, fmt.Errorf("%w: previous=%d current=%d", ErrFixtureClock, r.lastClock, now))
	}
	r.lastClock = now
	r.hasClock = true

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
			return RuntimeExecution{}, r.failLocked(BrowserScriptDiverged, wrapScriptError(fmt.Sprintf("operations[%d].result", r.position-1), err))
		}
		execution.InvocationID = invocationID
		r.pending[invocationID] = struct{}{}
		if len(operation.Result) == 0 {
			execution.Result = MustJSONValue(map[string]any{"invocation_id": invocationID})
		}
	}
	if request.Type == OperationNavigate {
		if operation.Expect.URL != "" {
			r.target.URL = operation.Expect.URL
		}
		r.generation++
		execution.Generation = r.generation
	}
	if request.Type == OperationEnableLifecycle {
		r.enabledLifecycle = true
	}
	if request.Type == OperationEnableWebMCP {
		r.enabledWebMCP = true
	}
	if request.Type == OperationDetachTarget {
		r.detached = true
	}
	if request.Type == OperationCloseTarget {
		r.targetClosed = true
	}

	execution.Events = make([]FixtureEvent, 0, len(operation.Emit))
	for _, emitted := range operation.Emit {
		event, err := r.observeLocked(emitted, now)
		if err != nil {
			return RuntimeExecution{}, r.failLocked(BrowserScriptDiverged, &FixtureOperationError{
				Kind:     ErrFixtureOperationMismatch,
				Position: r.position - 1,
				Path:     "emit",
				Expected: expected,
				Actual:   request,
				Cause:    err,
			})
		}
		execution.Events = append(execution.Events, event)
	}
	r.last = cloneRuntimeExecution(execution)
	r.maybeCompleteLocked()
	return execution, nil
}

// ExecuteOperation is an alias for Execute.
func (r *ScriptedBrowserRuntime) ExecuteOperation(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.Execute(ctx, request)
}

// Run executes one operation. It is a descriptive alias for Execute.
func (r *ScriptedBrowserRuntime) Run(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.Execute(ctx, request)
}

// RunOperation is an alias for Run.
func (r *ScriptedBrowserRuntime) RunOperation(ctx context.Context, request OperationRequest) (RuntimeExecution, error) {
	return r.Execute(ctx, request)
}

func validateOperationRequest(request OperationRequest) error {
	if !isOperationType(request.Type) {
		return fmt.Errorf("unknown operation type %q", request.Type)
	}
	switch request.Type {
	case OperationEnableLifecycle, OperationEnableWebMCP, OperationCloseTarget, OperationDetachTarget:
		if request.FrameID != "" || request.ToolName != "" || request.Input != nil || request.InvocationID != "" || request.URL != "" {
			return errors.New("operation does not accept additional fields")
		}
	case OperationInvokeTool:
		if err := validateScriptID(request.FrameID); err != nil {
			return fmt.Errorf("frame_id: %w", err)
		}
		if strings.TrimSpace(request.ToolName) == "" {
			return errors.New("tool_name is required")
		}
		input := request.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		if !isJSONObject(input) {
			return errors.New("input must be a JSON object")
		}
	case OperationCancelTool:
		if err := validateScriptID(request.InvocationID); err != nil {
			return fmt.Errorf("invocation_id: %w", err)
		}
		if request.FrameID != "" || request.ToolName != "" || request.Input != nil || request.URL != "" {
			return errors.New("operation does not accept frame_id, tool_name, input, or url")
		}
	case OperationNavigate:
		if request.URL != "" && strings.TrimSpace(request.URL) == "" {
			return errors.New("url must not be empty")
		}
		if request.FrameID != "" || request.ToolName != "" || request.Input != nil || request.InvocationID != "" {
			return errors.New("operation accepts only url")
		}
	}
	return nil
}

func compareOperation(expected OperationExpectation, actual OperationRequest) (string, error) {
	if expected.Type != actual.Type {
		return "type", fmt.Errorf("expected %q, got %q", expected.Type, actual.Type)
	}
	switch expected.Type {
	case OperationInvokeTool:
		if expected.FrameID != actual.FrameID {
			return "frame_id", fmt.Errorf("expected frame %q, got %q", expected.FrameID, actual.FrameID)
		}
		if expected.ToolName != actual.ToolName {
			return "tool_name", fmt.Errorf("expected tool %q, got %q", expected.ToolName, actual.ToolName)
		}
		actualInput := actual.Input
		if len(actualInput) == 0 {
			actualInput = json.RawMessage(`{}`)
		}
		if !jsonSemanticEqual(expected.Input, actualInput) {
			return "input", errors.New("JSON values differ")
		}
	case OperationCancelTool:
		if expected.InvocationID != actual.InvocationID {
			return "invocation_id", fmt.Errorf("expected invocation %q, got %q", expected.InvocationID, actual.InvocationID)
		}
	case OperationNavigate:
		if expected.URL != actual.URL {
			return "url", fmt.Errorf("expected URL %q, got %q", expected.URL, actual.URL)
		}
	}
	return "", nil
}

func jsonSemanticEqual(left, right json.RawMessage) bool {
	leftValue, err := decodeJSONNumberValue(left)
	if err != nil {
		return false
	}
	rightValue, err := decodeJSONNumberValue(right)
	if err != nil {
		return false
	}
	return semanticValueEqual(leftValue, rightValue)
}

func decodeJSONNumberValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func semanticValueEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, value := range leftValue {
			other, ok := rightValue[key]
			if !ok || !semanticValueEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !semanticValueEqual(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case json.Number:
		rightValue, ok := right.(json.Number)
		return ok && leftValue.String() == rightValue.String()
	default:
		return left == right
	}
}

func (r *ScriptedBrowserRuntime) invocationIDForResult(result json.RawMessage) (string, error) {
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
	id := r.ids.NextID("invocation")
	if err := validateScriptID(id); err != nil {
		return "", fmt.Errorf("generated invocation ID: %w", err)
	}
	return id, nil
}

func (r *ScriptedBrowserRuntime) observeLocked(emitted EmittedEvent, now uint64) (FixtureEvent, error) {
	event := FixtureEvent{
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
	switch emitted.Type {
	case EmittedToolsAdded:
		r.tools = append(r.tools[:0], cloneToolDescriptors(emitted.Tools)...)
	case EmittedToolResponded:
		if _, ok := r.pending[emitted.InvocationID]; !ok {
			return FixtureEvent{}, fmt.Errorf("response for invocation %q has no pending invocation", emitted.InvocationID)
		}
		delete(r.pending, emitted.InvocationID)
	default:
		return FixtureEvent{}, fmt.Errorf("unknown emitted event type %q", emitted.Type)
	}
	r.events = append(r.events, cloneFixtureEvent(event))
	select {
	case r.stream <- cloneFixtureEvent(event):
	default:
		return FixtureEvent{}, errors.New("fixture event stream capacity exhausted")
	}
	return event, nil
}

func (r *ScriptedBrowserRuntime) maybeCompleteLocked() {
	if r.position == len(r.script.Operations) && len(r.pending) == 0 && !r.closed {
		r.completeLocked()
	}
}

func (r *ScriptedBrowserRuntime) completeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	r.outcome = BrowserScriptOutcome{Status: BrowserScriptCompleted, Position: r.position}
	close(r.stream)
	close(r.done)
}

func (r *ScriptedBrowserRuntime) failLocked(status BrowserScriptStatus, err error) error {
	if r.closed {
		if r.outcome.Err != nil {
			return r.outcome.Err
		}
		return err
	}
	r.closed = true
	r.outcome = BrowserScriptOutcome{Status: status, Err: err, Position: r.position}
	close(r.stream)
	close(r.done)
	return err
}

func (r *ScriptedBrowserRuntime) cancel(err error) (RuntimeExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		if r.outcome.Err != nil {
			return RuntimeExecution{}, r.outcome.Err
		}
		return RuntimeExecution{}, ErrFixtureClosed
	}
	return RuntimeExecution{}, r.failLocked(BrowserScriptCanceled, errors.Join(ErrFixtureCanceled, err))
}

// Complete closes a runtime successfully only when all operations and
// invocation responses have been consumed.
func (r *ScriptedBrowserRuntime) Complete() error {
	if r == nil {
		return ErrFixtureClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.outcome.Err
	}
	if r.position != len(r.script.Operations) || len(r.pending) != 0 {
		return r.failLocked(BrowserScriptIncomplete, &FixtureIncompleteError{
			Position:   r.position,
			Operations: len(r.script.Operations) - r.position,
			Pending:    pendingIDs(r.pending),
		})
	}
	r.completeLocked()
	return nil
}

// Finish is an alias for Complete.
func (r *ScriptedBrowserRuntime) Finish() error { return r.Complete() }

// Close terminates a runtime. An unfinished runtime reports an incomplete
// error; a completed runtime is safe to close repeatedly.
func (r *ScriptedBrowserRuntime) Close() error {
	if r == nil {
		return ErrFixtureClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.outcome.Err
	}
	return r.failLocked(BrowserScriptIncomplete, &FixtureIncompleteError{
		Position:   r.position,
		Operations: len(r.script.Operations) - r.position,
		Pending:    pendingIDs(r.pending),
	})
}

// Done closes once the runtime has either completed or failed.
func (r *ScriptedBrowserRuntime) Done() <-chan struct{} {
	if r == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return r.done
}

// Events returns a finite, buffered stream of neutral observations. It is
// closed at runtime completion or failure and never requires a goroutine to
// deliver events.
func (r *ScriptedBrowserRuntime) Events() <-chan FixtureEvent {
	if r == nil {
		closed := make(chan FixtureEvent)
		close(closed)
		return closed
	}
	return r.stream
}

// Observations returns all emitted observations seen so far.
func (r *ScriptedBrowserRuntime) Observations() []FixtureEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]FixtureEvent, len(r.events))
	for index, event := range r.events {
		result[index] = cloneFixtureEvent(event)
	}
	return result
}

// LastExecution returns a copy of the most recent successful operation.
func (r *ScriptedBrowserRuntime) LastExecution() RuntimeExecution {
	if r == nil {
		return RuntimeExecution{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneRuntimeExecution(r.last)
}

// Outcome returns the current runtime lifecycle snapshot.
func (r *ScriptedBrowserRuntime) Outcome() BrowserScriptOutcome {
	if r == nil {
		return BrowserScriptOutcome{Status: BrowserScriptIncomplete, Err: ErrFixtureClosed}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return BrowserScriptOutcome{Status: r.outcome.Status, Err: r.outcome.Err, Position: r.outcome.Position}
}

// Err returns the terminal runtime error, if any.
func (r *ScriptedBrowserRuntime) Err() error {
	return r.Outcome().Err
}

// PendingInvocationIDs returns stable pending invocation IDs.
func (r *ScriptedBrowserRuntime) PendingInvocationIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return pendingIDs(r.pending)
}

// PendingInvocations is a descriptive alias for PendingInvocationIDs.
func (r *ScriptedBrowserRuntime) PendingInvocations() []string { return r.PendingInvocationIDs() }

// Tools returns the latest tools_added catalog as raw-schema-preserving data.
func (r *ScriptedBrowserRuntime) Tools() []ToolDescriptor {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneToolDescriptors(r.tools)
}

// StateOracle returns the independent page-state oracle.
func (r *ScriptedBrowserRuntime) StateOracle() *FixtureStateOracle {
	if r == nil {
		return nil
	}
	return r.state
}

// PageState is a convenience snapshot of the independent state oracle.
func (r *ScriptedBrowserRuntime) PageState() json.RawMessage {
	if r == nil || r.state == nil {
		return nil
	}
	return r.state.Snapshot()
}

// BrowserID returns the stable fixture browser identifier.
func (r *ScriptedBrowserRuntime) BrowserID() string {
	if r == nil {
		return ""
	}
	return r.browserID
}

// Target returns the current target metadata, including a navigation-updated
// URL.
func (r *ScriptedBrowserRuntime) Target() BrowserTarget {
	if r == nil {
		return BrowserTarget{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.target
}

// Generation returns the current page generation.
func (r *ScriptedBrowserRuntime) Generation() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

// EnableLifecycle consumes the next lifecycle operation.
func (r *ScriptedBrowserRuntime) EnableLifecycle(ctx context.Context) error {
	_, err := r.Execute(ctx, OperationRequest{Type: OperationEnableLifecycle})
	return err
}

// EnableWebMCP consumes the next WebMCP-enable operation.
func (r *ScriptedBrowserRuntime) EnableWebMCP(ctx context.Context) error {
	_, err := r.Execute(ctx, OperationRequest{Type: OperationEnableWebMCP})
	return err
}

// InvokeTool consumes the next invoke_tool operation and returns its
// invocation identity. A nil input means the empty JSON object.
func (r *ScriptedBrowserRuntime) InvokeTool(ctx context.Context, frameID, toolName string, input json.RawMessage) (string, error) {
	execution, err := r.Execute(ctx, OperationRequest{Type: OperationInvokeTool, FrameID: frameID, ToolName: toolName, Input: cloneRaw(input)})
	if err != nil {
		return "", err
	}
	return execution.InvocationID, nil
}

// InvokeToolValue marshals an invocation input without converting page-owned
// JSON through a floating-point map.
func (r *ScriptedBrowserRuntime) InvokeToolValue(ctx context.Context, frameID, toolName string, input any) (string, error) {
	raw, err := JSONValue(input)
	if err != nil {
		return "", err
	}
	return r.InvokeTool(ctx, frameID, toolName, raw)
}

// CancelTool consumes the next cancel_tool operation.
func (r *ScriptedBrowserRuntime) CancelTool(ctx context.Context, invocationID string) error {
	if r == nil {
		return ErrFixtureClosed
	}
	r.mu.Lock()
	_, pending := r.pending[invocationID]
	r.mu.Unlock()
	if !pending {
		return fmt.Errorf("%w: invocation %q is not pending", ErrInvalidFixtureOperation, invocationID)
	}
	_, err := r.Execute(ctx, OperationRequest{Type: OperationCancelTool, InvocationID: invocationID})
	return err
}

// Navigate consumes the next navigate operation.
func (r *ScriptedBrowserRuntime) Navigate(ctx context.Context, url string) error {
	_, err := r.Execute(ctx, OperationRequest{Type: OperationNavigate, URL: url})
	return err
}

// CloseTarget consumes the next close_target operation.
func (r *ScriptedBrowserRuntime) CloseTarget(ctx context.Context) error {
	_, err := r.Execute(ctx, OperationRequest{Type: OperationCloseTarget})
	return err
}

// DetachTarget consumes the next detach_target operation.
func (r *ScriptedBrowserRuntime) DetachTarget(ctx context.Context) error {
	_, err := r.Execute(ctx, OperationRequest{Type: OperationDetachTarget})
	return err
}

// FixtureStateOracle stores page state outside the scripted response stream.
// It is deliberately explicit: a response cannot make a state assertion pass
// unless the test changes or observes this oracle.
type FixtureStateOracle struct {
	mu      sync.RWMutex
	initial json.RawMessage
	value   json.RawMessage
}

// StateOracle is a descriptive alias for FixtureStateOracle.
type StateOracle = FixtureStateOracle

// NewFixtureStateOracle creates an oracle from any JSON value.
func NewFixtureStateOracle(value any) (*FixtureStateOracle, error) {
	raw, err := JSONValue(value)
	if err != nil {
		return nil, fmt.Errorf("state oracle: %w", err)
	}
	return NewFixtureStateOracleJSON(raw)
}

// NewStateOracle is an alias for NewFixtureStateOracle.
func NewStateOracle(value any) (*FixtureStateOracle, error) {
	return NewFixtureStateOracle(value)
}

// NewFixtureStateOracleJSON creates an oracle from one validated JSON value.
func NewFixtureStateOracleJSON(raw json.RawMessage) (*FixtureStateOracle, error) {
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return nil, err
	}
	return &FixtureStateOracle{initial: cloneRaw(normalized), value: cloneRaw(normalized)}, nil
}

// Snapshot returns a cloned JSON state value.
func (o *FixtureStateOracle) Snapshot() json.RawMessage {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneRaw(o.value)
}

// Read is an alias for Snapshot.
func (o *FixtureStateOracle) Read() json.RawMessage { return o.Snapshot() }

// Set replaces the state with a JSON value.
func (o *FixtureStateOracle) Set(value any) error {
	raw, err := JSONValue(value)
	if err != nil {
		return err
	}
	return o.SetJSON(raw)
}

// SetJSON replaces the state with a validated raw JSON value.
func (o *FixtureStateOracle) SetJSON(raw json.RawMessage) error {
	if o == nil {
		return errors.New("state oracle is nil")
	}
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.value = cloneRaw(normalized)
	o.mu.Unlock()
	return nil
}

// Reset restores the state present at construction.
func (o *FixtureStateOracle) Reset() error {
	if o == nil {
		return errors.New("state oracle is nil")
	}
	o.mu.Lock()
	o.value = cloneRaw(o.initial)
	o.mu.Unlock()
	return nil
}

func pendingIDs(pending map[string]struct{}) []string {
	result := make([]string, 0, len(pending))
	for id := range pending {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func cloneOperationRequest(request OperationRequest) OperationRequest {
	if request.Type == OperationInvokeTool && len(request.Input) == 0 {
		request.Input = json.RawMessage(`{}`)
	}
	request.Input = cloneRaw(request.Input)
	return request
}

func cloneRuntimeExecution(execution RuntimeExecution) RuntimeExecution {
	execution.Request = cloneOperationRequest(execution.Request)
	execution.Result = cloneRaw(execution.Result)
	events := execution.Events
	execution.Events = make([]FixtureEvent, len(events))
	for index, event := range events {
		execution.Events[index] = cloneFixtureEvent(event)
	}
	return execution
}

func cloneFixtureEvent(event FixtureEvent) FixtureEvent {
	event.Tools = cloneToolDescriptors(event.Tools)
	event.Output = cloneRaw(event.Output)
	event.Error = cloneRaw(event.Error)
	return event
}

func cloneToolDescriptors(tools []ToolDescriptor) []ToolDescriptor {
	if tools == nil {
		return nil
	}
	result := make([]ToolDescriptor, len(tools))
	for index, tool := range tools {
		result[index] = tool
		result[index].InputSchema = cloneRaw(tool.InputSchema)
		result[index].Annotations = cloneRaw(tool.Annotations)
	}
	return result
}
