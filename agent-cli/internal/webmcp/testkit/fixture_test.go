package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLoadBrowserScriptPreservesPageJSONAndStrictControlShapes(t *testing.T) {
	script, err := LoadBrowserScript([]byte(validBrowserScriptJSON))
	if err != nil {
		t.Fatalf("LoadBrowserScript: %v", err)
	}
	if script.Version != BrowserScriptVersion || len(script.Operations) != 3 {
		t.Fatalf("loaded script = %#v", script)
	}
	if got := string(script.Operations[2].Expect.Input); got != `{"count":9007199254740993}` {
		t.Fatalf("input token = %s, want large integer preserved", got)
	}
	if got := string(script.Operations[2].Emit[0].Output); got != `{"value":9007199254740993}` {
		t.Fatalf("output token = %s, want large integer preserved", got)
	}

	invalid := []struct {
		name string
		data string
		want string
	}{
		{name: "unknown root field", data: strings.Replace(validBrowserScriptJSON, `"operations":`, `"extra":true,"operations":`, 1), want: "script"},
		{name: "unknown endpoint field", data: strings.Replace(validBrowserScriptJSON, `"targets":`, `"extra":true,"targets":`, 1), want: "endpoint"},
		{name: "unknown endpoint version field", data: strings.Replace(validBrowserScriptJSON, `"Protocol-Version": "1.3"`, `"extra": "bad", "Protocol-Version": "1.3"`, 1), want: "version"},
		{name: "unknown target field", data: strings.Replace(validBrowserScriptJSON, `"title": "Fixture"`, `"extra": true, "title": "Fixture"`, 1), want: "target"},
		{name: "unknown operation field", data: strings.Replace(validBrowserScriptJSON, `"result": {}`, `"extra": true, "result": {}`, 1), want: "operation"},
		{name: "unknown operation type", data: strings.Replace(validBrowserScriptJSON, `"type": "enable_lifecycle"`, `"type": "unknown"`, 1), want: "unknown operation"},
		{name: "missing invoke input", data: strings.Replace(validBrowserScriptJSON, ",\n        \"input\": {\"count\":9007199254740993}", "", 1), want: "input"},
		{name: "non-object invoke input", data: strings.Replace(validBrowserScriptJSON, `"input": {"count":9007199254740993}`, `"input": []`, 1), want: "input"},
		{name: "missing cancel ID", data: missingCancelIDScriptJSON(), want: "invocation_id"},
		{name: "unknown emitted event", data: strings.Replace(validBrowserScriptJSON, `"type": "tool_responded"`, `"type": "tool_unknown"`, 1), want: "emitted"},
		{name: "completed without output", data: strings.Replace(validBrowserScriptJSON, ",\n          \"output\": {\"value\":9007199254740993}", "", 1), want: "output"},
		{name: "error response with output", data: strings.Replace(validBrowserScriptJSON, "\"status\": \"Completed\",\n          \"output\": {\"value\":9007199254740993}", "\"status\": \"Error\",\n          \"output\": {\"value\":9007199254740993}", 1), want: "error"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadBrowserScript([]byte(test.data))
			if !errors.Is(err, ErrInvalidBrowserScript) {
				t.Fatalf("error = %v, want ErrInvalidBrowserScript", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want context %q", err, test.want)
			}
		})
	}
}

func TestScriptedBrowserRuntimeConsumesOperationsAndEmitsNeutralEvents(t *testing.T) {
	script, err := LoadBrowserScript([]byte(validBrowserScriptJSON))
	if err != nil {
		t.Fatalf("LoadBrowserScript: %v", err)
	}
	clock := NewFakeClock(100)
	runtime, err := NewScriptedBrowserRuntime(script,
		WithFixtureClock(clock),
		WithFixtureIDSource(NewDeterministicIDSource("run")),
		WithFixtureBrowserID("browser-fixture"),
		WithFixtureState(map[string]any{"value": 0}),
	)
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime: %v", err)
	}
	if got := runtime.Outcome().Status; got != BrowserScriptOpen {
		t.Fatalf("initial outcome = %q, want open", got)
	}

	if err := runtime.EnableLifecycle(context.Background()); err != nil {
		t.Fatalf("EnableLifecycle: %v", err)
	}
	clock.Advance(5)
	if err := runtime.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("EnableWebMCP: %v", err)
	}
	tools := runtime.Tools()
	if len(tools) != 1 || tools[0].Name != "read_state" {
		t.Fatalf("catalog = %#v", tools)
	}
	if string(tools[0].InputSchema) != `{"additionalProperties":false,"properties":{},"type":"object"}` &&
		string(tools[0].InputSchema) != `{"type":"object","properties":{},"additionalProperties":false}` {
		t.Fatalf("schema was not retained as JSON: %s", tools[0].InputSchema)
	}

	clock.Advance(7)
	invocationID, err := runtime.InvokeTool(context.Background(), "frame-1", "read_state", json.RawMessage(`{"count":9007199254740993}`))
	if err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if invocationID != "inv-1" {
		t.Fatalf("invocation ID = %q, want scripted ID inv-1", invocationID)
	}
	if pending := runtime.PendingInvocationIDs(); len(pending) != 0 {
		t.Fatalf("pending invocations = %#v, want none after response", pending)
	}
	execution := runtime.LastExecution()
	if execution.MonotonicMS != 112 || execution.Generation != 1 {
		t.Fatalf("execution timing/context = %+v", execution)
	}
	if !bytes.Contains(execution.Events[0].Output, []byte("9007199254740993")) {
		t.Fatalf("output lost large integer: %s", execution.Events[0].Output)
	}
	if got := runtime.Outcome(); !got.OK() {
		t.Fatalf("outcome = %+v, want completed", got)
	}

	var observed []FixtureEvent
	for event := range runtime.Events() {
		observed = append(observed, event)
	}
	if len(observed) != 2 || observed[0].Type != EmittedToolsAdded || observed[1].Type != EmittedToolResponded {
		t.Fatalf("observed events = %#v", observed)
	}
	if observed[0].MonotonicMS != 105 || observed[1].MonotonicMS != 112 {
		t.Fatalf("event times = %d, %d", observed[0].MonotonicMS, observed[1].MonotonicMS)
	}

	state := runtime.PageState()
	if string(state) != `{"value":0}` {
		t.Fatalf("script response implicitly changed state to %s", state)
	}
	if err := runtime.StateOracle().Set(map[string]any{"value": 42}); err != nil {
		t.Fatalf("StateOracle.Set: %v", err)
	}
	if got := string(runtime.PageState()); got != `{"value":42}` {
		t.Fatalf("out-of-band state = %s", got)
	}
}

func missingCancelIDScriptJSON() string {
	return strings.Replace(validBrowserScriptJSON, "\n  ]\n}", ",\n    {\n      \"expect\": {\"type\": \"cancel_tool\"}\n    }\n  ]\n}", 1)
}

func TestScriptedBrowserRuntimeUsesInjectedIDsForUnspecifiedInvocation(t *testing.T) {
	script := BrowserScript{
		Version: BrowserScriptVersion,
		Endpoint: BrowserEndpoint{
			Version: EndpointVersionInfo{Browser: "Chrome/Test", ProtocolVersion: "1.3", WebSocketDebuggerURL: "ws://fixture/browser"},
			Targets: []BrowserTarget{{ID: "tab-1", Type: "page", Title: "Fixture", URL: "https://fixture.test/", WebSocketDebuggerURL: "ws://fixture/page/tab-1"}},
		},
		Operations: []BrowserScriptOperation{
			{Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "read_state", Input: json.RawMessage(`{}`)},
				Emit: []EmittedEvent{{Type: EmittedToolResponded, InvocationID: "run-invocation-001", Status: "Completed", Output: json.RawMessage(`{"ok":true}`)}}},
		},
	}
	runtime, err := NewScriptedBrowserRuntime(script, WithFixtureIDSource(NewDeterministicIDSource("run")))
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime: %v", err)
	}
	id, err := runtime.InvokeTool(context.Background(), "frame-1", "read_state", nil)
	if err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if id != "run-invocation-001" {
		t.Fatalf("generated invocation ID = %q", id)
	}
	if got := string(runtime.LastExecution().Result); got != `{"invocation_id":"run-invocation-001"}` {
		t.Fatalf("generated result = %s", got)
	}
}

func TestScriptedBrowserRuntimeNavigationAndFailureAreDeterministic(t *testing.T) {
	script, err := LoadBrowserScript([]byte(strings.Replace(validBrowserScriptJSON,
		`{"type":"invoke_tool","frame_id":"frame-1","tool_name":"read_state","input":{"count":9007199254740993}}`,
		`{"type":"navigate","url":"https://fixture.test/next"}`, 1)))
	if err != nil {
		t.Fatalf("LoadBrowserScript: %v", err)
	}
	// The replacement above removes the response operation as well; construct a
	// minimal navigation-only script so the test asserts the navigation seam.
	script.Operations = []BrowserScriptOperation{{Expect: OperationExpectation{Type: OperationNavigate, URL: "https://fixture.test/next"}}}
	runtime, err := NewScriptedBrowserRuntime(script)
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime: %v", err)
	}
	if err := runtime.Navigate(context.Background(), "https://fixture.test/next"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if runtime.Generation() != 2 || runtime.Target().URL != "https://fixture.test/next" {
		t.Fatalf("navigation state generation=%d target=%#v", runtime.Generation(), runtime.Target())
	}

	badScript := script
	badScript.Operations = []BrowserScriptOperation{{Expect: OperationExpectation{Type: OperationEnableLifecycle}}}
	runtime, err = NewScriptedBrowserRuntime(badScript)
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime(badScript): %v", err)
	}
	_, err = runtime.Execute(context.Background(), OperationRequest{Type: OperationEnableWebMCP})
	if !errors.Is(err, ErrFixtureOperationMismatch) {
		t.Fatalf("mismatch error = %v, want ErrFixtureOperationMismatch", err)
	}
	if runtime.Outcome().Status != BrowserScriptDiverged {
		t.Fatalf("mismatch outcome = %+v", runtime.Outcome())
	}
}

func TestScriptedBrowserRuntimeSupportsCancellationAndCleanupOperations(t *testing.T) {
	script := BrowserScript{
		Version: BrowserScriptVersion,
		Endpoint: BrowserEndpoint{
			Version: EndpointVersionInfo{Browser: "Chrome/Test", ProtocolVersion: "1.3", WebSocketDebuggerURL: "ws://fixture/browser"},
			Targets: []BrowserTarget{{ID: "tab-1", Type: "page", Title: "Fixture", URL: "https://fixture.test/", WebSocketDebuggerURL: "ws://fixture/page/tab-1"}},
		},
		Operations: []BrowserScriptOperation{
			{Expect: OperationExpectation{Type: OperationEnableLifecycle}},
			{Expect: OperationExpectation{Type: OperationEnableWebMCP}},
			{Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "write_state", Input: json.RawMessage(`{"value":1}`)}, Result: json.RawMessage(`{"invocation_id":"inv-cancel"}`)},
			{Expect: OperationExpectation{Type: OperationCancelTool, InvocationID: "inv-cancel"}, Emit: []EmittedEvent{{Type: EmittedToolResponded, InvocationID: "inv-cancel", Status: "Canceled", Error: json.RawMessage(`{"code":"invocation_canceled"}`)}}},
			{Expect: OperationExpectation{Type: OperationNavigate}, Result: json.RawMessage(`{"ok":true}`)},
			{Expect: OperationExpectation{Type: OperationCloseTarget}},
		},
	}
	runtime, err := NewScriptedBrowserRuntime(script, WithFixtureClockFunc(func() uint64 { return 50 }))
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime: %v", err)
	}
	if err := runtime.EnableLifecycle(context.Background()); err != nil {
		t.Fatalf("EnableLifecycle: %v", err)
	}
	if err := runtime.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("EnableWebMCP: %v", err)
	}
	if _, err := runtime.InvokeTool(context.Background(), "frame-1", "write_state", json.RawMessage(`{"value":1}`)); err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if err := runtime.CancelTool(context.Background(), "inv-cancel"); err != nil {
		t.Fatalf("CancelTool: %v", err)
	}
	if err := runtime.Navigate(context.Background(), ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := runtime.CloseTarget(context.Background()); err != nil {
		t.Fatalf("CloseTarget: %v", err)
	}
	if !runtime.Outcome().OK() || len(runtime.PendingInvocations()) != 0 {
		t.Fatalf("terminal state = %+v pending=%#v", runtime.Outcome(), runtime.PendingInvocations())
	}
	if got := runtime.Target().URL; got != "https://fixture.test/" {
		t.Fatal("navigation with an omitted URL should retain target URL")
	}

	detachScript := script
	detachScript.Operations = []BrowserScriptOperation{{Expect: OperationExpectation{Type: OperationDetachTarget}}}
	detached, err := NewScriptedBrowserRuntime(detachScript)
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime(detach): %v", err)
	}
	if err := detached.DetachTarget(context.Background()); err != nil {
		t.Fatalf("DetachTarget: %v", err)
	}
	if !detached.Outcome().OK() {
		t.Fatalf("detach outcome = %+v", detached.Outcome())
	}
}

func TestScriptedBrowserRuntimeReportsIncompleteCancellationAndClockErrors(t *testing.T) {
	script := BrowserScript{
		Version: BrowserScriptVersion,
		Endpoint: BrowserEndpoint{
			Version: EndpointVersionInfo{Browser: "Chrome/Test", ProtocolVersion: "1.3", WebSocketDebuggerURL: "ws://fixture/browser"},
			Targets: []BrowserTarget{{ID: "tab-1", Type: "page", Title: "Fixture", URL: "https://fixture.test/", WebSocketDebuggerURL: "ws://fixture/page/tab-1"}},
		},
		Operations: []BrowserScriptOperation{{Expect: OperationExpectation{Type: OperationEnableLifecycle}}},
	}
	runtime, err := NewScriptedBrowserRuntime(script)
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime: %v", err)
	}
	if err := runtime.Close(); !errors.Is(err, ErrFixtureIncomplete) {
		t.Fatalf("Close error = %v, want incomplete", err)
	}
	if runtime.Outcome().Status != BrowserScriptIncomplete {
		t.Fatalf("close outcome = %+v", runtime.Outcome())
	}

	canceled, err := NewScriptedBrowserRuntime(script)
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime(canceled): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := canceled.EnableLifecycle(ctx); !errors.Is(err, ErrFixtureCanceled) {
		t.Fatalf("canceled operation error = %v", err)
	}
	if canceled.Outcome().Status != BrowserScriptCanceled {
		t.Fatalf("canceled outcome = %+v", canceled.Outcome())
	}

	clock := NewFakeClock(10)
	clockScript := BrowserScript{Version: BrowserScriptVersion, Endpoint: script.Endpoint, Operations: []BrowserScriptOperation{{Expect: OperationExpectation{Type: OperationEnableLifecycle}}, {Expect: OperationExpectation{Type: OperationEnableWebMCP}}}}
	clockRuntime, err := NewScriptedBrowserRuntime(clockScript, WithFixtureClock(clock))
	if err != nil {
		t.Fatalf("NewScriptedBrowserRuntime(clock): %v", err)
	}
	if err := clockRuntime.EnableLifecycle(context.Background()); err != nil {
		t.Fatalf("clock lifecycle: %v", err)
	}
	clock.Set(9)
	if err := clockRuntime.EnableWebMCP(context.Background()); !errors.Is(err, ErrFixtureClock) {
		t.Fatalf("clock error = %v, want ErrFixtureClock", err)
	}
}

func TestBrowserScriptLoadAliasesOptionsAndStateOracle(t *testing.T) {
	loaded, err := DecodeBrowserScript([]byte(validBrowserScriptJSON))
	if err != nil {
		t.Fatalf("DecodeBrowserScript: %v", err)
	}
	if _, err := LoadScript([]byte(validBrowserScriptJSON)); err != nil {
		t.Fatalf("LoadScript: %v", err)
	}
	if _, err := LoadBrowserScriptReader(strings.NewReader(validBrowserScriptJSON)); err != nil {
		t.Fatalf("LoadBrowserScriptReader: %v", err)
	}
	dir := t.TempDir()
	path := dir + "/browser-script.json"
	if err := os.WriteFile(path, []byte(validBrowserScriptJSON), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := LoadBrowserScriptFile(path); err != nil {
		t.Fatalf("LoadBrowserScriptFile: %v", err)
	}
	if _, err := LoadScriptFile(path); err != nil {
		t.Fatalf("LoadScriptFile: %v", err)
	}

	oracle, err := NewStateOracle(map[string]any{"value": 1})
	if err != nil {
		t.Fatalf("NewStateOracle: %v", err)
	}
	if got := string(oracle.Read()); got != `{"value":1}` {
		t.Fatalf("Read = %s", got)
	}
	if err := oracle.SetJSON(json.RawMessage(`{"value":2}`)); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	if err := oracle.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := string(oracle.Snapshot()); got != `{"value":1}` {
		t.Fatalf("Reset snapshot = %s", got)
	}
	if _, err := NewFixtureStateOracleJSON(json.RawMessage(``)); err == nil {
		t.Fatal("empty state JSON unexpectedly accepted")
	}
	if _, err := NewFixtureStateOracle(struct{ Bad chan int }{}); err == nil {
		t.Fatal("unmarshalable state unexpectedly accepted")
	}

	options := []RuntimeOption{
		WithRuntimeClock(NewFakeClock(3)),
		WithRuntimeIDSource(NewDeterministicIDSource("alias")),
		WithFixtureIDFunc(func(string) string { return "alias-id" }),
		WithFixtureTargetID("tab-1"),
		WithStateOracle(oracle),
	}
	for name, constructor := range map[string]func() (*ScriptedBrowserRuntime, error){
		"browser": func() (*ScriptedBrowserRuntime, error) { return NewBrowserScriptRuntime(loaded, options...) },
		"fixture": func() (*ScriptedBrowserRuntime, error) { return NewFixtureRuntime(loaded, options...) },
		"script":  func() (*ScriptedBrowserRuntime, error) { return NewScriptRuntime(loaded, options...) },
		"value":   func() (*ScriptedBrowserRuntime, error) { return NewRuntime(loaded, options...) },
		"pointer": func() (*ScriptedBrowserRuntime, error) { return NewRuntime(&loaded, options...) },
	} {
		t.Run(name, func(t *testing.T) {
			runtime, err := constructor()
			if err != nil {
				t.Fatalf("constructor: %v", err)
			}
			if runtime.StateOracle() != oracle || runtime.BrowserID() != "fixture-browser" || runtime.Target().ID != "tab-1" {
				t.Fatalf("runtime options were not applied: browser=%q target=%#v oracle=%p", runtime.BrowserID(), runtime.Target(), runtime.StateOracle())
			}
			if err := runtime.Complete(); !errors.Is(err, ErrFixtureIncomplete) {
				t.Fatalf("Complete before operations = %v, want incomplete", err)
			}
		})
	}
	if _, err := NewRuntime("not a script"); !errors.Is(err, ErrInvalidBrowserScript) {
		t.Fatalf("invalid NewRuntime value = %v", err)
	}
	if _, err := NewRuntime((*BrowserScript)(nil)); !errors.Is(err, ErrInvalidBrowserScript) {
		t.Fatalf("nil NewRuntime pointer = %v", err)
	}

	empty := BrowserScript{Version: BrowserScriptVersion, Endpoint: loaded.Endpoint}
	completed, err := NewScriptedBrowserRuntime(empty)
	if err != nil {
		t.Fatalf("empty runtime: %v", err)
	}
	if !completed.Outcome().OK() {
		t.Fatalf("empty outcome = %+v", completed.Outcome())
	}
	if err := completed.Finish(); err != nil {
		t.Fatalf("Finish after automatic completion: %v", err)
	}
	if err := completed.Close(); err != nil {
		t.Fatalf("Close after automatic completion: %v", err)
	}
	if err := completed.Err(); err != nil {
		t.Fatalf("Err after automatic completion: %v", err)
	}
	select {
	case <-completed.Done():
	default:
		t.Fatal("completed runtime Done channel is open")
	}
	if got := completed.Observations(); len(got) != 0 {
		t.Fatalf("empty observations = %#v", got)
	}
}

func TestFixtureValidationAndJSONComparisonBranches(t *testing.T) {
	if !jsonSemanticEqual(json.RawMessage(`{"a":[1,{"n":2}]}`), json.RawMessage(`{"a":[1,{"n":2}]}`)) {
		t.Fatal("equivalent JSON did not compare equal")
	}
	if jsonSemanticEqual(json.RawMessage(`{"a":[1]}`), json.RawMessage(`{"a":[2]}`)) {
		t.Fatal("different JSON compared equal")
	}
	if jsonSemanticEqual(json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":1} trailing`)) {
		t.Fatal("multiple JSON values compared equal")
	}

	for _, status := range []string{"Canceled", "Error"} {
		event := EmittedEvent{Type: EmittedToolResponded, InvocationID: "inv-1", Status: status, Error: json.RawMessage(`{"code":"safe","message":"page failed","details":{}}`)}
		if err := event.Validate(); err != nil {
			t.Fatalf("valid %s event: %v", status, err)
		}
	}
	for name, event := range map[string]EmittedEvent{
		"unknown error field": {Type: EmittedToolResponded, InvocationID: "inv-1", Status: "Error", Error: json.RawMessage(`{"code":"safe","extra":true}`)},
		"null error":          {Type: EmittedToolResponded, InvocationID: "inv-1", Status: "Error", Error: json.RawMessage(`null`)},
		"array error":         {Type: EmittedToolResponded, InvocationID: "inv-1", Status: "Error", Error: json.RawMessage(`[]`)},
		"empty string error":  {Type: EmittedToolResponded, InvocationID: "inv-1", Status: "Error", Error: json.RawMessage(`""`)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := event.Validate(); err == nil {
				t.Fatal("invalid stable error unexpectedly accepted")
			}
		})
	}

	requestErrors := []OperationRequest{
		{Type: OperationEnableLifecycle, ToolName: "extra"},
		{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "tool", Input: json.RawMessage(`[]`)},
		{Type: OperationCancelTool},
		{Type: OperationNavigate, URL: " ", FrameID: "frame-1"},
		{Type: OperationType("unknown")},
	}
	for _, request := range requestErrors {
		if err := validateOperationRequest(request); err == nil {
			t.Errorf("request %#v unexpectedly valid", request)
		}
	}
	if err := validateOperationRequest(OperationRequest{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "tool", Input: json.RawMessage(`null`)}); err == nil {
		t.Fatal("null invocation input unexpectedly accepted")
	}

	var nilRuntime *ScriptedBrowserRuntime
	if !errors.Is(nilRuntime.Err(), ErrFixtureClosed) || nilRuntime.StateOracle() != nil || nilRuntime.PageState() != nil || nilRuntime.BrowserID() != "" || nilRuntime.Generation() != 0 || nilRuntime.Target().ID != "" {
		t.Fatal("nil runtime accessors did not remain safe")
	}
	if err := nilRuntime.Close(); !errors.Is(err, ErrFixtureClosed) {
		t.Fatalf("nil runtime Close = %v", err)
	}
	if err := nilRuntime.Complete(); !errors.Is(err, ErrFixtureClosed) {
		t.Fatalf("nil runtime Complete = %v", err)
	}
	if _, err := nilRuntime.Execute(context.Background(), OperationRequest{}); !errors.Is(err, ErrFixtureClosed) {
		t.Fatalf("nil runtime Execute = %v", err)
	}
	if _, ok := <-nilRuntime.Events(); ok {
		t.Fatal("nil runtime event channel was not closed")
	}
	if _, ok := <-nilRuntime.Done(); ok {
		t.Fatal("nil runtime done channel was not closed")
	}

	var nilOracle *FixtureStateOracle
	if nilOracle.Snapshot() != nil || nilOracle.Read() != nil || nilOracle.Set(nil) == nil || nilOracle.SetJSON(json.RawMessage(`{}`)) == nil || nilOracle.Reset() == nil {
		t.Fatal("nil oracle accessors unexpectedly succeeded")
	}
}

const validBrowserScriptJSON = `{
  "version": "webmcp.browser-script.v1",
  "endpoint": {
    "version": {
      "Browser": "Chrome/Test",
      "Protocol-Version": "1.3",
      "webSocketDebuggerUrl": "ws://fixture/browser"
    },
    "targets": [
      {
        "id": "tab-1",
        "type": "page",
        "title": "Fixture",
        "url": "https://fixture.test/",
        "webSocketDebuggerUrl": "ws://fixture/page/tab-1"
      }
    ]
  },
  "operations": [
    {
      "expect": {"type": "enable_lifecycle"},
      "result": {}
    },
    {
      "expect": {"type": "enable_webmcp"},
      "result": {},
      "emit": [
        {
          "type": "tools_added",
          "tools": [
            {
              "name": "read_state",
              "description": "Read fixture state",
              "frame_id": "frame-1",
              "input_schema": {"type":"object","properties":{},"additionalProperties":false},
              "annotations": {"read_only":true}
            }
          ]
        }
      ]
    },
    {
      "expect": {
        "type": "invoke_tool",
        "frame_id": "frame-1",
        "tool_name": "read_state",
        "input": {"count":9007199254740993}
      },
      "result": {"invocation_id":"inv-1"},
      "emit": [
        {
          "type": "tool_responded",
          "invocation_id": "inv-1",
          "status": "Completed",
          "output": {"value":9007199254740993}
        }
      ]
    }
  ]
}`
