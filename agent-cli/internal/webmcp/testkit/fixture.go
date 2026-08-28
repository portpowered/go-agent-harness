package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	// BrowserScriptVersion is the only scripted browser fixture schema accepted
	// by this package.
	BrowserScriptVersion = "webmcp.browser-script.v1"
	// BrowserFixtureVersion is a descriptive alias for BrowserScriptVersion.
	BrowserFixtureVersion = BrowserScriptVersion
)

var (
	// ErrInvalidBrowserScript identifies a malformed or unsupported fixture.
	ErrInvalidBrowserScript = errors.New("webmcp testkit: invalid browser script")
	// ErrInvalidFixtureOperation identifies a caller operation that cannot be
	// sent to a scripted runtime.
	ErrInvalidFixtureOperation = errors.New("webmcp testkit: invalid fixture operation")
	// ErrFixtureOperationMismatch identifies a caller operation that differs
	// from the next expected operation in a script.
	ErrFixtureOperationMismatch = errors.New("webmcp testkit: fixture operation mismatch")
	// ErrFixtureIncomplete identifies a runtime closed before all expected
	// operations or pending invocations were resolved.
	ErrFixtureIncomplete = errors.New("webmcp testkit: fixture incomplete")
	// ErrFixturePendingInvocations identifies unresolved invocation state at
	// fixture completion.
	ErrFixturePendingInvocations = errors.New("webmcp testkit: pending fixture invocations")
	// ErrFixtureClosed identifies a runtime that cannot accept more work.
	ErrFixtureClosed = errors.New("webmcp testkit: fixture runtime closed")
	// ErrFixtureCanceled identifies context cancellation at the fixture edge.
	ErrFixtureCanceled = errors.New("webmcp testkit: fixture operation canceled")
	// ErrFixtureClock identifies a clock that moved backwards while emitting
	// neutral observations.
	ErrFixtureClock = errors.New("webmcp testkit: fixture clock moved backwards")
)

// ScriptValidationError identifies a safe structural fixture error. Path is a
// JSON-like location such as operations[2].expect.input.
type ScriptValidationError struct {
	Path  string
	Cause error
}

func (e *ScriptValidationError) Error() string {
	if e == nil {
		return ErrInvalidBrowserScript.Error()
	}
	message := ErrInvalidBrowserScript.Error()
	if e.Path != "" {
		message += " at " + e.Path
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *ScriptValidationError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrInvalidBrowserScript
	}
	return errors.Join(ErrInvalidBrowserScript, e.Cause)
}

func newScriptError(path, format string, args ...any) error {
	return &ScriptValidationError{Path: path, Cause: fmt.Errorf(format, args...)}
}

func wrapScriptError(path string, err error) error {
	if err == nil {
		return nil
	}
	var validation *ScriptValidationError
	if errors.As(err, &validation) {
		copyOf := *validation
		if copyOf.Path == "" {
			copyOf.Path = path
		} else if path != "" {
			copyOf.Path = path + "." + copyOf.Path
		}
		return &copyOf
	}
	return &ScriptValidationError{Path: path, Cause: err}
}

// BrowserScript is a complete webmcp.browser-script.v1 document.
type BrowserScript struct {
	Version    string                   `json:"version"`
	Endpoint   BrowserEndpoint          `json:"endpoint"`
	Operations []BrowserScriptOperation `json:"operations"`

	endpointSet   bool
	operationsSet bool
}

// Script and FixtureScript are descriptive aliases for BrowserScript.
type Script = BrowserScript
type FixtureScript = BrowserScript
type BrowserScriptFixture = BrowserScript
type Fixture = BrowserScript

// BrowserEndpoint describes the browser-level DevTools fixture metadata.
type BrowserEndpoint struct {
	Version EndpointVersionInfo `json:"version"`
	Targets []BrowserTarget     `json:"targets"`

	versionSet bool
	targetsSet bool
}

// Endpoint is a shorter alias for BrowserEndpoint.
type Endpoint = BrowserEndpoint

// EndpointVersionInfo contains the exact endpoint version fields used by the
// fixture contract.
type EndpointVersionInfo struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// EndpointVersion and BrowserVersion are aliases for the endpoint version
// object. The JSON spelling remains the C0 spelling above.
type EndpointVersion = EndpointVersionInfo
type BrowserVersion = EndpointVersionInfo

// UnmarshalJSON validates the endpoint version's exact three-field control
// shape. The capitalization is part of the DevTools discovery contract.
func (v *EndpointVersionInfo) UnmarshalJSON(data []byte) error {
	if v == nil {
		return errors.New("cannot unmarshal endpoint version into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("version", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"Browser": {}, "Protocol-Version": {}, "webSocketDebuggerUrl": {}}); err != nil {
		return newScriptError("version", "%v", err)
	}
	result := EndpointVersionInfo{}
	for name, destination := range map[string]*string{
		"Browser":              &result.Browser,
		"Protocol-Version":     &result.ProtocolVersion,
		"webSocketDebuggerUrl": &result.WebSocketDebuggerURL,
	} {
		raw, ok := fields[name]
		if !ok {
			return newScriptError(name, "is required")
		}
		value, err := parseScriptString(raw)
		if err != nil {
			return wrapScriptError(name, err)
		}
		*destination = value
	}
	*v = result
	return nil
}

// BrowserTarget describes one target returned by the scripted endpoint.
type BrowserTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Target is a shorter alias for BrowserTarget.
type Target = BrowserTarget

// OperationType is the controlled command vocabulary of browser-script.v1.
type OperationType string

const (
	OperationEnableLifecycle OperationType = "enable_lifecycle"
	OperationEnableWebMCP    OperationType = "enable_webmcp"
	OperationInvokeTool      OperationType = "invoke_tool"
	OperationCancelTool      OperationType = "cancel_tool"
	OperationNavigate        OperationType = "navigate"
	OperationCloseTarget     OperationType = "close_target"
	OperationDetachTarget    OperationType = "detach_target"

	// Short names keep fixture construction readable.
	EnableLifecycle = OperationEnableLifecycle
	EnableWebMCP    = OperationEnableWebMCP
	InvokeTool      = OperationInvokeTool
	CancelTool      = OperationCancelTool
	Navigate        = OperationNavigate
	CloseTarget     = OperationCloseTarget
	DetachTarget    = OperationDetachTarget
)

// BrowserScriptOperation is one expected operation and its scripted response
// or neutral observations.
type BrowserScriptOperation struct {
	Expect OperationExpectation `json:"expect"`
	Result json.RawMessage      `json:"result,omitempty"`
	Emit   []EmittedEvent       `json:"emit,omitempty"`

	resultSet bool
	emitSet   bool
}

type ScriptOperation = BrowserScriptOperation

// OperationExpectation is the controlled request shape inside an operation's
// expect object. Page-owned JSON is retained only in Input.
type OperationExpectation struct {
	Type         OperationType   `json:"type"`
	FrameID      string          `json:"frame_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	InvocationID string          `json:"invocation_id,omitempty"`
	URL          string          `json:"url,omitempty"`

	frameIDSet      bool
	toolNameSet     bool
	inputSet        bool
	invocationIDSet bool
	urlSet          bool
}

// Expectation is a shorter alias for OperationExpectation.
type Expectation = OperationExpectation
type ScriptExpectation = OperationExpectation

// EmittedEventType is the neutral event vocabulary supported by fixtures.
type EmittedEventType string

const (
	EmittedToolsAdded    EmittedEventType = "tools_added"
	EmittedToolResponded EmittedEventType = "tool_responded"

	ToolsAdded    = EmittedToolsAdded
	ToolResponded = EmittedToolResponded
)

// EmittedEvent is one page-session observation generated after a matched
// operation. Only tools_added and tool_responded are part of the v1 fixture
// vocabulary.
type EmittedEvent struct {
	Type         EmittedEventType `json:"type"`
	Tools        []ToolDescriptor `json:"tools,omitempty"`
	InvocationID string           `json:"invocation_id,omitempty"`
	Status       string           `json:"status,omitempty"`
	Output       json.RawMessage  `json:"output,omitempty"`
	Error        json.RawMessage  `json:"error,omitempty"`

	toolsSet  bool
	outputSet bool
	errorSet  bool
}

// FixtureEvent is the neutral runtime observation. Timestamp and runtime
// context are added by the fake runtime; the script itself contains only the
// page-session event data.
type FixtureEvent struct {
	Type         EmittedEventType `json:"type"`
	MonotonicMS  uint64           `json:"monotonic_ms"`
	BrowserID    string           `json:"browser_id"`
	TargetID     string           `json:"target_id"`
	Generation   uint64           `json:"generation"`
	Tools        []ToolDescriptor `json:"tools,omitempty"`
	InvocationID string           `json:"invocation_id,omitempty"`
	Status       string           `json:"status,omitempty"`
	Output       json.RawMessage  `json:"output,omitempty"`
	Error        json.RawMessage  `json:"error,omitempty"`
}

// Observation and NeutralEvent are aliases for FixtureEvent.
type Observation = FixtureEvent
type NeutralEvent = FixtureEvent
type ScriptEvent = EmittedEvent

// ToolDescriptor is a page-owned WebMCP descriptor used by tools_added.
// InputSchema and Annotations stay as raw JSON so schema values and integer
// tokens are not reinterpreted by the fixture loader.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	FrameID     string          `json:"frame_id"`
	InputSchema json.RawMessage `json:"input_schema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`

	annotationsSet bool
}

// Tool is a shorter alias for ToolDescriptor.
type Tool = ToolDescriptor

// OperationRequest is a caller operation sent to ScriptedBrowserRuntime.
type OperationRequest struct {
	Type         OperationType   `json:"type"`
	FrameID      string          `json:"frame_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	InvocationID string          `json:"invocation_id,omitempty"`
	URL          string          `json:"url,omitempty"`
}

// Request is a shorter alias for OperationRequest.
type Request = OperationRequest

// RuntimeExecution contains the scripted result and all neutral observations
// produced by one matched operation.
type RuntimeExecution struct {
	Request      OperationRequest
	Result       json.RawMessage
	Events       []FixtureEvent
	InvocationID string
	BrowserID    string
	TargetID     string
	Generation   uint64
	MonotonicMS  uint64
}

// Execution is a shorter alias for RuntimeExecution.
type Execution = RuntimeExecution
type RuntimeResult = RuntimeExecution

// BrowserScriptStatus is the terminal state of a scripted runtime.
type BrowserScriptStatus string

const (
	BrowserScriptOpen       BrowserScriptStatus = "open"
	BrowserScriptCompleted  BrowserScriptStatus = "completed"
	BrowserScriptDiverged   BrowserScriptStatus = "diverged"
	BrowserScriptIncomplete BrowserScriptStatus = "incomplete"
	BrowserScriptCanceled   BrowserScriptStatus = "canceled"
)

// BrowserScriptOutcome is an inspectable runtime state snapshot.
type BrowserScriptOutcome struct {
	Status   BrowserScriptStatus
	Err      error
	Position int
}

// OK reports whether every scripted operation was consumed without pending
// invocations or an execution error.
func (o BrowserScriptOutcome) OK() bool {
	return o.Status == BrowserScriptCompleted
}

// FixtureOperationError contains safe operation mismatch context. It does not
// print raw input or result values.
type FixtureOperationError struct {
	Kind     error
	Position int
	Path     string
	Expected OperationExpectation
	Actual   OperationRequest
	Cause    error
}

func (e *FixtureOperationError) Error() string {
	if e == nil {
		return ErrFixtureOperationMismatch.Error()
	}
	kind := ErrFixtureOperationMismatch
	if e.Kind != nil {
		kind = e.Kind
	}
	message := fmt.Sprintf("%s at operations[%d]", kind, e.Position)
	if e.Path != "" {
		message += "." + e.Path
	}
	message += fmt.Sprintf(": expected %q, got %q", e.Expected.Type, e.Actual.Type)
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *FixtureOperationError) Unwrap() error {
	if e == nil || e.Kind == nil {
		return ErrFixtureOperationMismatch
	}
	if e.Cause == nil {
		return e.Kind
	}
	return errors.Join(e.Kind, e.Cause)
}

// FixtureIncompleteError describes what remained when a runtime was closed.
type FixtureIncompleteError struct {
	Position   int
	Operations int
	Pending    []string
	Cause      error
}

func (e *FixtureIncompleteError) Error() string {
	if e == nil {
		return ErrFixtureIncomplete.Error()
	}
	message := fmt.Sprintf("%s at operation %d: %d expected operation(s) remain", ErrFixtureIncomplete, e.Position, e.Operations)
	if len(e.Pending) > 0 {
		message += fmt.Sprintf("; %d pending invocation(s)", len(e.Pending))
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *FixtureIncompleteError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrFixtureIncomplete
	}
	return errors.Join(ErrFixtureIncomplete, e.Cause)
}

// UnmarshalJSON validates the complete script before returning it to the
// caller. This makes json.Unmarshal, LoadBrowserScript, and runtime creation
// share the same strict boundary.
func (s *BrowserScript) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("cannot unmarshal browser script into nil receiver")
	}
	if !utf8.Valid(data) {
		return newScriptError("script", "input is not valid UTF-8")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("script", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"version": {}, "endpoint": {}, "operations": {}}); err != nil {
		return newScriptError("script", "%v", err)
	}
	versionRaw, ok := fields["version"]
	if !ok {
		return newScriptError("version", "is required")
	}
	version, err := parseScriptString(versionRaw)
	if err != nil {
		return wrapScriptError("version", err)
	}
	endpointRaw, ok := fields["endpoint"]
	if !ok {
		return newScriptError("endpoint", "is required")
	}
	if !isJSONObject(endpointRaw) {
		return newScriptError("endpoint", "must be a JSON object")
	}
	var endpoint BrowserEndpoint
	if err := json.Unmarshal(endpointRaw, &endpoint); err != nil {
		return wrapScriptError("endpoint", err)
	}
	operationsRaw, ok := fields["operations"]
	if !ok {
		return newScriptError("operations", "is required")
	}
	operationValues, err := scriptArray(operationsRaw)
	if err != nil {
		return wrapScriptError("operations", err)
	}
	operations := make([]BrowserScriptOperation, len(operationValues))
	for index, raw := range operationValues {
		if err := json.Unmarshal(raw, &operations[index]); err != nil {
			return wrapScriptError(fmt.Sprintf("operations[%d]", index), err)
		}
	}
	result := BrowserScript{
		Version:       version,
		Endpoint:      endpoint,
		Operations:    operations,
		endpointSet:   true,
		operationsSet: true,
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*s = result
	return nil
}

// Validate checks a BrowserScript value independently of JSON decoding.
func (s BrowserScript) Validate() error {
	if s.Version != BrowserScriptVersion {
		return newScriptError("version", "want %q, got %q", BrowserScriptVersion, s.Version)
	}
	if !s.endpointSet && s.Endpoint.Version.Browser == "" && len(s.Endpoint.Targets) == 0 {
		return newScriptError("endpoint", "is required")
	}
	if err := s.Endpoint.Validate(); err != nil {
		return wrapScriptError("endpoint", err)
	}
	if err := validateScriptOperations(s.Operations); err != nil {
		return err
	}
	return nil
}

// UnmarshalJSON validates the endpoint's exact two-field control shape.
func (e *BrowserEndpoint) UnmarshalJSON(data []byte) error {
	if e == nil {
		return errors.New("cannot unmarshal browser endpoint into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("endpoint", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"version": {}, "targets": {}}); err != nil {
		return newScriptError("endpoint", "%v", err)
	}
	versionRaw, ok := fields["version"]
	if !ok {
		return newScriptError("version", "is required")
	}
	if !isJSONObject(versionRaw) {
		return newScriptError("version", "must be a JSON object")
	}
	var version EndpointVersionInfo
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return wrapScriptError("version", err)
	}
	targetsRaw, ok := fields["targets"]
	if !ok {
		return newScriptError("targets", "is required")
	}
	targetValues, err := scriptArray(targetsRaw)
	if err != nil {
		return wrapScriptError("targets", err)
	}
	targets := make([]BrowserTarget, len(targetValues))
	for index, raw := range targetValues {
		if err := json.Unmarshal(raw, &targets[index]); err != nil {
			return wrapScriptError(fmt.Sprintf("targets[%d]", index), err)
		}
	}
	result := BrowserEndpoint{Version: version, Targets: targets, versionSet: true, targetsSet: true}
	if err := result.Validate(); err != nil {
		return err
	}
	*e = result
	return nil
}

// Validate checks an endpoint and all target records.
func (e BrowserEndpoint) Validate() error {
	if strings.TrimSpace(e.Version.Browser) == "" {
		return newScriptError("version.Browser", "is required")
	}
	if strings.TrimSpace(e.Version.ProtocolVersion) == "" {
		return newScriptError("version.Protocol-Version", "is required")
	}
	if strings.TrimSpace(e.Version.WebSocketDebuggerURL) == "" {
		return newScriptError("version.webSocketDebuggerUrl", "is required")
	}
	seen := make(map[string]struct{}, len(e.Targets))
	for index, target := range e.Targets {
		if err := target.Validate(); err != nil {
			return wrapScriptError(fmt.Sprintf("targets[%d]", index), err)
		}
		if _, exists := seen[target.ID]; exists {
			return newScriptError(fmt.Sprintf("targets[%d].id", index), "duplicate target ID %q", target.ID)
		}
		seen[target.ID] = struct{}{}
	}
	return nil
}

// UnmarshalJSON validates a target's exact five-field control shape.
func (t *BrowserTarget) UnmarshalJSON(data []byte) error {
	if t == nil {
		return errors.New("cannot unmarshal browser target into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("target", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"id": {}, "type": {}, "title": {}, "url": {}, "webSocketDebuggerUrl": {}}); err != nil {
		return newScriptError("target", "%v", err)
	}
	var result BrowserTarget
	for name, destination := range map[string]*string{
		"id":                   &result.ID,
		"type":                 &result.Type,
		"title":                &result.Title,
		"url":                  &result.URL,
		"webSocketDebuggerUrl": &result.WebSocketDebuggerURL,
	} {
		raw, ok := fields[name]
		if !ok {
			return newScriptError(name, "is required")
		}
		value, err := parseScriptString(raw)
		if err != nil {
			return wrapScriptError(name, err)
		}
		*destination = value
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*t = result
	return nil
}

// Validate checks a target's required values and safe opaque IDs.
func (t BrowserTarget) Validate() error {
	if err := validateScriptID(t.ID); err != nil {
		return wrapScriptError("id", err)
	}
	if strings.TrimSpace(t.Type) == "" {
		return newScriptError("type", "is required")
	}
	if strings.TrimSpace(t.URL) == "" {
		return newScriptError("url", "is required")
	}
	if strings.TrimSpace(t.WebSocketDebuggerURL) == "" {
		return newScriptError("webSocketDebuggerUrl", "is required")
	}
	return nil
}

// UnmarshalJSON validates one script operation and its optional response and
// event list before the runtime can see it.
func (o *BrowserScriptOperation) UnmarshalJSON(data []byte) error {
	if o == nil {
		return errors.New("cannot unmarshal browser script operation into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("operation", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"expect": {}, "result": {}, "emit": {}}); err != nil {
		return newScriptError("operation", "%v", err)
	}
	expectRaw, ok := fields["expect"]
	if !ok {
		return newScriptError("expect", "is required")
	}
	if !isJSONObject(expectRaw) {
		return newScriptError("expect", "must be a JSON object")
	}
	var expect OperationExpectation
	if err := json.Unmarshal(expectRaw, &expect); err != nil {
		return wrapScriptError("expect", err)
	}
	result := BrowserScriptOperation{Expect: expect}
	if resultRaw, ok := fields["result"]; ok {
		normalized, err := normalizeJSON(resultRaw)
		if err != nil {
			return wrapScriptError("result", err)
		}
		result.Result = normalized
		result.resultSet = true
	}
	if emitRaw, ok := fields["emit"]; ok {
		emitValues, err := scriptArray(emitRaw)
		if err != nil {
			return wrapScriptError("emit", err)
		}
		result.Emit = make([]EmittedEvent, len(emitValues))
		for index, raw := range emitValues {
			if err := json.Unmarshal(raw, &result.Emit[index]); err != nil {
				return wrapScriptError(fmt.Sprintf("emit[%d]", index), err)
			}
		}
		result.emitSet = true
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*o = result
	return nil
}

// Validate checks an operation and its controlled response shapes.
func (o BrowserScriptOperation) Validate() error {
	if err := o.Expect.Validate(); err != nil {
		return wrapScriptError("expect", err)
	}
	if o.resultSet || len(o.Result) > 0 {
		if err := validateOperationResult(o.Expect.Type, o.Result); err != nil {
			return wrapScriptError("result", err)
		}
	}
	for index, event := range o.Emit {
		if err := event.Validate(); err != nil {
			return wrapScriptError(fmt.Sprintf("emit[%d]", index), err)
		}
	}
	return nil
}

func validateScriptOperations(operations []BrowserScriptOperation) error {
	for index, operation := range operations {
		if err := operation.Validate(); err != nil {
			return wrapScriptError(fmt.Sprintf("operations[%d]", index), err)
		}
	}
	return nil
}

// UnmarshalJSON validates the type-specific operation control fields.
func (e *OperationExpectation) UnmarshalJSON(data []byte) error {
	if e == nil {
		return errors.New("cannot unmarshal operation expectation into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("expect", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"type": {}, "frame_id": {}, "tool_name": {}, "input": {}, "invocation_id": {}, "url": {}}); err != nil {
		return newScriptError("expect", "%v", err)
	}
	typeRaw, ok := fields["type"]
	if !ok {
		return newScriptError("type", "is required")
	}
	typeName, err := parseScriptString(typeRaw)
	if err != nil {
		return wrapScriptError("type", err)
	}
	result := OperationExpectation{Type: OperationType(typeName)}
	if raw, ok := fields["frame_id"]; ok {
		result.FrameID, err = parseScriptString(raw)
		if err != nil {
			return wrapScriptError("frame_id", err)
		}
		result.frameIDSet = true
	}
	if raw, ok := fields["tool_name"]; ok {
		result.ToolName, err = parseScriptString(raw)
		if err != nil {
			return wrapScriptError("tool_name", err)
		}
		result.toolNameSet = true
	}
	if raw, ok := fields["input"]; ok {
		result.Input, err = normalizeJSON(raw)
		if err != nil {
			return wrapScriptError("input", err)
		}
		result.inputSet = true
	}
	if raw, ok := fields["invocation_id"]; ok {
		result.InvocationID, err = parseScriptString(raw)
		if err != nil {
			return wrapScriptError("invocation_id", err)
		}
		result.invocationIDSet = true
	}
	if raw, ok := fields["url"]; ok {
		result.URL, err = parseScriptString(raw)
		if err != nil {
			return wrapScriptError("url", err)
		}
		result.urlSet = true
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*e = result
	return nil
}

// Validate checks the operation vocabulary and all per-variant fields.
func (e OperationExpectation) Validate() error {
	if !isOperationType(e.Type) {
		return newScriptError("type", "unknown operation type %q", e.Type)
	}
	switch e.Type {
	case OperationEnableLifecycle, OperationEnableWebMCP, OperationCloseTarget, OperationDetachTarget:
		if e.frameIDSet || e.toolNameSet || e.inputSet || e.invocationIDSet || e.urlSet {
			return newScriptError("type", "operation %q does not accept additional fields", e.Type)
		}
	case OperationInvokeTool:
		if strings.TrimSpace(e.FrameID) == "" {
			return newScriptError("frame_id", "is required")
		}
		if err := validateScriptID(e.FrameID); err != nil {
			return wrapScriptError("frame_id", err)
		}
		if strings.TrimSpace(e.ToolName) == "" {
			return newScriptError("tool_name", "is required")
		}
		if !e.inputSet && len(e.Input) == 0 {
			return newScriptError("input", "is required")
		}
		if !isJSONObject(e.Input) {
			return newScriptError("input", "must be a JSON object")
		}
		if e.invocationIDSet || e.urlSet {
			return newScriptError("type", "operation %q does not accept invocation_id or url", e.Type)
		}
	case OperationCancelTool:
		if strings.TrimSpace(e.InvocationID) == "" {
			return newScriptError("invocation_id", "is required")
		}
		if err := validateScriptID(e.InvocationID); err != nil {
			return wrapScriptError("invocation_id", err)
		}
		if e.frameIDSet || e.toolNameSet || e.inputSet || e.urlSet {
			return newScriptError("type", "operation %q does not accept frame_id, tool_name, input, or url", e.Type)
		}
	case OperationNavigate:
		if e.frameIDSet || e.toolNameSet || e.inputSet || e.invocationIDSet {
			return newScriptError("type", "operation %q accepts only url", e.Type)
		}
		if e.urlSet && strings.TrimSpace(e.URL) == "" {
			return newScriptError("url", "must not be empty")
		}
	}
	return nil
}

func isOperationType(value OperationType) bool {
	switch value {
	case OperationEnableLifecycle, OperationEnableWebMCP, OperationInvokeTool, OperationCancelTool, OperationNavigate, OperationCloseTarget, OperationDetachTarget:
		return true
	default:
		return false
	}
}

func validateOperationResult(operationType OperationType, raw json.RawMessage) error {
	if operationType == OperationInvokeTool && !isJSONObject(raw) {
		return newScriptError("", "invoke_tool result must be a JSON object")
	}
	if operationType == OperationInvokeTool {
		fields, err := decodeJSONObject(raw)
		if err != nil {
			return err
		}
		if invocationRaw, ok := fields["invocation_id"]; ok {
			value, err := parseScriptString(invocationRaw)
			if err != nil {
				return fmt.Errorf("invocation_id: %w", err)
			}
			if err := validateScriptID(value); err != nil {
				return fmt.Errorf("invocation_id: %w", err)
			}
		}
	}
	return nil
}

// UnmarshalJSON validates one neutral tools_added or tool_responded event.
func (e *EmittedEvent) UnmarshalJSON(data []byte) error {
	if e == nil {
		return errors.New("cannot unmarshal emitted event into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("emit", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"type": {}, "tools": {}, "invocation_id": {}, "status": {}, "output": {}, "error": {}}); err != nil {
		return newScriptError("emit", "%v", err)
	}
	typeRaw, ok := fields["type"]
	if !ok {
		return newScriptError("type", "is required")
	}
	typeName, err := parseScriptString(typeRaw)
	if err != nil {
		return wrapScriptError("type", err)
	}
	result := EmittedEvent{Type: EmittedEventType(typeName)}
	if raw, ok := fields["tools"]; ok {
		values, err := scriptArray(raw)
		if err != nil {
			return wrapScriptError("tools", err)
		}
		result.Tools = make([]ToolDescriptor, len(values))
		for index, value := range values {
			if err := json.Unmarshal(value, &result.Tools[index]); err != nil {
				return wrapScriptError(fmt.Sprintf("tools[%d]", index), err)
			}
		}
		result.toolsSet = true
	}
	if raw, ok := fields["invocation_id"]; ok {
		result.InvocationID, err = parseScriptString(raw)
		if err != nil {
			return wrapScriptError("invocation_id", err)
		}
	}
	if raw, ok := fields["status"]; ok {
		result.Status, err = parseScriptString(raw)
		if err != nil {
			return wrapScriptError("status", err)
		}
	}
	if raw, ok := fields["output"]; ok {
		result.Output, err = normalizeJSON(raw)
		if err != nil {
			return wrapScriptError("output", err)
		}
		result.outputSet = true
	}
	if raw, ok := fields["error"]; ok {
		result.Error, err = normalizeJSON(raw)
		if err != nil {
			return wrapScriptError("error", err)
		}
		result.errorSet = true
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*e = result
	return nil
}

// Validate checks one emitted neutral event and its terminal response shape.
func (e EmittedEvent) Validate() error {
	switch e.Type {
	case EmittedToolsAdded:
		if !e.toolsSet && e.Tools == nil {
			return newScriptError("type", "tools_added requires tools")
		}
		if e.InvocationID != "" || e.Status != "" || e.outputSet || e.errorSet || len(e.Output) > 0 || len(e.Error) > 0 {
			return newScriptError("type", "tools_added does not accept invocation response fields")
		}
		for index, tool := range e.Tools {
			if err := tool.Validate(); err != nil {
				return wrapScriptError(fmt.Sprintf("tools[%d]", index), err)
			}
		}
	case EmittedToolResponded:
		hasOutput := e.outputSet || len(e.Output) > 0
		hasError := e.errorSet || len(e.Error) > 0
		if strings.TrimSpace(e.InvocationID) == "" {
			return newScriptError("invocation_id", "is required")
		}
		if err := validateScriptID(e.InvocationID); err != nil {
			return wrapScriptError("invocation_id", err)
		}
		if !isInvocationStatus(e.Status) {
			return newScriptError("status", "must be Completed, Canceled, or Error")
		}
		if hasOutput == hasError || (!hasOutput && !hasError) {
			return newScriptError("output", "exactly one of output or error is required")
		}
		if e.Status == "Completed" && !hasOutput {
			return newScriptError("output", "Completed response requires output")
		}
		if e.Status != "Completed" && !hasError {
			return newScriptError("error", "%s response requires error", e.Status)
		}
		if hasError {
			if err := validateStableFixtureError(e.Error); err != nil {
				return wrapScriptError("error", err)
			}
		}
	default:
		return newScriptError("type", "unknown emitted event type %q", e.Type)
	}
	return nil
}

func isInvocationStatus(value string) bool {
	return value == "Completed" || value == "Canceled" || value == "Error"
}

func validateStableFixtureError(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("must be a non-null stable error")
	}
	if trimmed[0] == '"' {
		value, err := parseScriptString(trimmed)
		if err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" {
			return errors.New("must not be empty")
		}
		return nil
	}
	if trimmed[0] != '{' {
		return errors.New("must be a string or object")
	}
	fields, err := decodeJSONObject(trimmed)
	if err != nil {
		return err
	}
	codeRaw, ok := fields["code"]
	if !ok {
		return errors.New("object error requires code")
	}
	code, err := parseScriptString(codeRaw)
	if err != nil {
		return fmt.Errorf("code: %w", err)
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("code must not be empty")
	}
	if messageRaw, ok := fields["message"]; ok {
		if _, err := parseScriptString(messageRaw); err != nil {
			return fmt.Errorf("message: %w", err)
		}
	}
	if detailsRaw, ok := fields["details"]; ok && !isJSONObject(detailsRaw) {
		return errors.New("details must be a JSON object")
	}
	for name := range fields {
		if name != "code" && name != "message" && name != "details" {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

// UnmarshalJSON validates a tool descriptor while leaving schema and
// annotations opaque JSON values.
func (t *ToolDescriptor) UnmarshalJSON(data []byte) error {
	if t == nil {
		return errors.New("cannot unmarshal tool descriptor into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return newScriptError("tool", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"name": {}, "description": {}, "frame_id": {}, "input_schema": {}, "annotations": {}}); err != nil {
		return newScriptError("tool", "%v", err)
	}
	var result ToolDescriptor
	for name, destination := range map[string]*string{"name": &result.Name, "description": &result.Description, "frame_id": &result.FrameID} {
		raw, ok := fields[name]
		if !ok {
			return newScriptError(name, "is required")
		}
		value, err := parseScriptString(raw)
		if err != nil {
			return wrapScriptError(name, err)
		}
		*destination = value
	}
	schemaRaw, ok := fields["input_schema"]
	if !ok {
		return newScriptError("input_schema", "is required")
	}
	schema, err := normalizeJSON(schemaRaw)
	if err != nil {
		return wrapScriptError("input_schema", err)
	}
	if !isJSONObject(schema) {
		return newScriptError("input_schema", "must be a JSON object")
	}
	result.InputSchema = schema
	if annotationsRaw, ok := fields["annotations"]; ok {
		annotations, err := normalizeJSON(annotationsRaw)
		if err != nil {
			return wrapScriptError("annotations", err)
		}
		if !isJSONObject(annotations) {
			return newScriptError("annotations", "must be a JSON object")
		}
		result.Annotations = annotations
		result.annotationsSet = true
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*t = result
	return nil
}

// Validate checks the descriptor control values. Its nested schema and
// annotations remain page-owned JSON and are not field-inventoried.
func (t ToolDescriptor) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return newScriptError("name", "is required")
	}
	if strings.TrimSpace(t.FrameID) == "" {
		return newScriptError("frame_id", "is required")
	}
	if err := validateScriptID(t.FrameID); err != nil {
		return wrapScriptError("frame_id", err)
	}
	if !isJSONObject(t.InputSchema) {
		return newScriptError("input_schema", "must be a JSON object")
	}
	if len(t.Annotations) > 0 && !isJSONObject(t.Annotations) {
		return newScriptError("annotations", "must be a JSON object")
	}
	return nil
}

func scriptArray(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("must be a JSON array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("must be a JSON array")
	}
	for index := range values {
		values[index] = cloneRaw(values[index])
	}
	return values, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func parseScriptString(raw json.RawMessage) (string, error) {
	value, err := parseString(raw)
	if err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func validateScriptID(value string) error {
	if err := validateOpaqueID(value); err != nil {
		return err
	}
	return nil
}

// LoadBrowserScript decodes and validates one browser-script.v1 document.
func LoadBrowserScript(data []byte) (BrowserScript, error) {
	if !utf8.Valid(data) {
		return BrowserScript{}, newScriptError("script", "input is not valid UTF-8")
	}
	var script BrowserScript
	if err := json.Unmarshal(data, &script); err != nil {
		return BrowserScript{}, wrapScriptError("script", err)
	}
	return script, nil
}

// DecodeBrowserScript is an alias for LoadBrowserScript.
func DecodeBrowserScript(data []byte) (BrowserScript, error) {
	return LoadBrowserScript(data)
}

// DecodeScript is an alias for LoadBrowserScript.
func DecodeScript(data []byte) (BrowserScript, error) { return LoadBrowserScript(data) }

// LoadScript is an alias for LoadBrowserScript.
func LoadScript(data []byte) (BrowserScript, error) {
	return LoadBrowserScript(data)
}

// LoadFixture is an alias for LoadBrowserScript.
func LoadFixture(data []byte) (BrowserScript, error) { return LoadBrowserScript(data) }

// LoadBrowserFixture is an alias for LoadBrowserScript.
func LoadBrowserFixture(data []byte) (BrowserScript, error) { return LoadBrowserScript(data) }

// LoadBrowserScriptFile reads and validates a fixture from disk.
func LoadBrowserScriptFile(path string) (BrowserScript, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BrowserScript{}, fmt.Errorf("read browser script %q: %w", path, err)
	}
	return LoadBrowserScript(data)
}

// LoadScriptFile is an alias for LoadBrowserScriptFile.
func LoadScriptFile(path string) (BrowserScript, error) {
	return LoadBrowserScriptFile(path)
}

// LoadFixtureFile is an alias for LoadBrowserScriptFile.
func LoadFixtureFile(path string) (BrowserScript, error) { return LoadBrowserScriptFile(path) }

// LoadBrowserScriptReader loads a complete script from a reader.
func LoadBrowserScriptReader(reader io.Reader) (BrowserScript, error) {
	if reader == nil {
		return BrowserScript{}, newScriptError("script", "reader is nil")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return BrowserScript{}, fmt.Errorf("read browser script: %w", err)
	}
	return LoadBrowserScript(data)
}

// FixtureRuntimeOption configures a BrowserScriptRuntime.
