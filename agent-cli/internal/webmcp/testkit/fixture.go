package testkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
