package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
	directops "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestWebMCPQueryTraceReproducesLiveDirectDivergence follows one query through
// the resolution, dispatch, browser-event, broker, and caller boundaries. The
// stale response is injected before the next invocation's event is consumed,
// which models a terminal event left by another CDP client. The test is a
// diagnosis fixture for story 001: the pre-fix broker accepts that response;
// the corrected broker may instead return a classified freshness failure.
func TestWebMCPQueryTraceReproducesLiveDirectDivergence(t *testing.T) {
	trace := newQueryTraceFixture(t)
	staleOutput := json.RawMessage(`{"count":0,"documents":[]}`)
	freshOutput := json.RawMessage(`{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`)
	// DeterministicIDs has not allocated an invocation yet. This response is
	// therefore the protocol terminal that the first live call will receive
	// before its own EventToolInvoked has crossed the broker boundary.
	staleID := webmcp.InvocationID("inv-000001")
	if err := trace.session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: staleID,
		Status:       "Completed",
		Output:       staleOutput,
		Generation:   trace.selected.Generation,
	}); err != nil {
		t.Fatalf("inject stale terminal response: %v", err)
	}

	liveEnvelope := trace.executeLiveQuery(t)
	trace.assertLiveDispatch(t, staleID)
	liveData := trace.assertLiveOutcome(t, liveEnvelope, staleID, staleOutput)

	directResult, directData := trace.runDirectQuery(t, freshOutput)
	if directResult.State != webmcp.InvocationCompleted || directResult.BrowserInvocationID == staleID || directData.ToolRef != string(trace.resolved.Ref) || directData.Status != string(webmcp.InvocationCompleted) || !jsonEqual(directData.Output, freshOutput) {
		t.Fatalf("direct result = %+v / %+v, want fresh completed payload", directResult, directData)
	}
	trace.assertSelectionUnchanged(t)
	directOperation, ok := firstQueryTraceOperation(trace.runtime.Operations(), testkit.OperationInvoke, directResult.BrowserInvocationID)
	if !ok {
		t.Fatalf("runtime operations after direct call = %#v, want direct invoke", trace.runtime.Operations())
	}
	if !trace.matchesResolvedDispatch(directOperation.BrowserID, directOperation.TargetID, directOperation.Generation, directOperation.FrameID, directOperation.ToolName, directOperation.Input) {
		t.Fatalf("direct dispatch = %+v, want same resolved target/frame/name/generation and input", directOperation)
	}
	assertQueryTraceEventOrder(t, trace.runtime.PublishedEvents(), staleID, directResult.BrowserInvocationID, trace.resolved.Generation)

	if liveEnvelope.OK && !jsonEqual(liveData.Output, staleOutput) {
		t.Fatalf("live output changed from controlled stale payload: %s", liveData.Output)
	}
	if !jsonEqual(directData.Output, freshOutput) {
		t.Fatalf("direct output = %s, want %s", directData.Output, freshOutput)
	}
}

// queryTraceFixture is one selected Margin page with a resolved read-only
// list_documents tool, published through the first-class page tool set.
type queryTraceFixture struct {
	runtime   *testkit.ScriptedBrowserRuntime
	broker    webmcp.Broker
	session   *testkit.ScriptedTargetSession
	selected  webmcp.PageContext
	resolved  webmcp.ToolDescriptor
	pageTools *webmcpTools.BrokerToolSet
}

type queryTraceLiveData struct {
	InvocationID webmcp.InvocationID `json:"invocation_id"`
	ToolRef      webmcp.ToolRef      `json:"tool_ref"`
	Status       string              `json:"status"`
	Output       json.RawMessage     `json:"output"`
}

func newQueryTraceFixture(t *testing.T) *queryTraceFixture {
	t.Helper()
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDSource("query-trace")
	candidate := webmcp.BrowserCandidate{ID: "browser-margin", Product: "fixture", Loopback: true}
	target := webmcp.Target{BrowserID: candidate.ID, ID: "tab-margin", Type: "page", Title: "Margin", URL: "https://margin.fixture/", Origin: "https://margin.fixture"}
	readOnly := true
	tool := webmcp.ToolDescriptor{
		Name:        "list_documents",
		Description: "List documents in the current Margin page.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly},
		FrameID:     "frame-margin",
		Origin:      target.Origin,
	}
	f := &queryTraceFixture{}
	f.runtime = testkit.NewScriptedBrowserRuntimeWithOptions(testkit.RuntimeOptions{Clock: clock, IDs: ids}, testkit.BrowserConfig{
		Candidate: candidate,
		Targets: []testkit.TargetConfig{testkit.NewTargetConfig(target,
			testkit.WithContext(webmcp.PageContext{Generation: 1, CatalogReady: true, CatalogEvidence: "scripted_fixture"}),
			testkit.WithInitialCatalog(tool),
		)},
	})
	f.broker = webmcp.NewBroker(webmcp.BrokerOptions{Runtime: f.runtime, IDs: ids, Clock: clock, ToolRefFactory: webmcp.StableToolRef})
	t.Cleanup(func() {
		closeForTest(t, f.broker.Close)
		closeForTest(t, f.runtime.Close)
	})
	var err error
	f.selected, err = f.broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID})
	if err != nil {
		t.Fatalf("select target: %v", err)
	}
	if f.selected.Key.BrowserID != candidate.ID || f.selected.Key.TargetID != target.ID || f.selected.Generation != 1 {
		t.Fatalf("selected page = %+v, want browser %q target %q generation 1", f.selected, candidate.ID, target.ID)
	}
	f.resolved = resolveQueryTraceTool(t, f.broker, tool.Name)
	f.pageTools = webmcpTools.NewBrokerToolSet(f.broker)
	definitions, err := f.pageTools.PageToolDefinitionsWithError(context.Background())
	if err != nil {
		t.Fatalf("publish live page definitions: %v", err)
	}
	if len(definitions) != 1 || definitions[0].Name != tool.Name {
		t.Fatalf("live page definitions = %#v, want list_documents", definitions)
	}
	if f.session = f.runtime.Browser(candidate.ID).TargetSession(target.ID); f.session == nil {
		t.Fatal("scripted target session is nil")
	}
	return f
}

func resolveQueryTraceTool(t *testing.T, broker webmcp.Broker, name string) webmcp.ToolDescriptor {
	t.Helper()
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("resolve page catalog: %v", err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != name {
		t.Fatalf("resolved catalog = %#v, want %s", snapshot.Tools, name)
	}
	return snapshot.Tools[0]
}

// matchesResolvedDispatch reports whether a dispatch carries the resolved
// tool's exact target, frame, name, generation, and empty input.
func (f *queryTraceFixture) matchesResolvedDispatch(browserID webmcp.BrowserID, targetID webmcp.TargetID, generation uint64, frameID webmcp.FrameID, toolName string, input json.RawMessage) bool {
	resolved := f.resolved
	return browserID == resolved.BrowserID && targetID == resolved.TargetID && generation == resolved.Generation &&
		frameID == resolved.FrameID && toolName == resolved.Name && jsonEqual(input, []byte(`{}`))
}

func (f *queryTraceFixture) executeLiveQuery(t *testing.T) webmcp.ToolResultEnvelope {
	t.Helper()
	liveContext, cancelLive := context.WithTimeout(context.Background(), time.Second)
	defer cancelLive()
	liveResponse, err := f.pageTools.Executor().Execute(liveContext, messages.ToolCall{ID: "call-live-list-documents", Name: "list_documents", Arguments: `{}`})
	if err != nil {
		t.Fatalf("execute live page query: %v", err)
	}
	if liveResponse.ToolCallID != "call-live-list-documents" || liveResponse.Name != "list_documents" {
		t.Fatalf("live caller correlation = %+v, want original call ID and name", liveResponse)
	}
	liveEnvelope, err := webmcp.UnmarshalToolResult([]byte(liveResponse.Content))
	if err != nil {
		t.Fatalf("decode live page result: %v; content=%s", err, liveResponse.Content)
	}
	return liveEnvelope
}

func (f *queryTraceFixture) assertLiveDispatch(t *testing.T, staleID webmcp.InvocationID) {
	t.Helper()
	operations := f.runtime.Operations()
	liveOperation, ok := firstQueryTraceOperation(operations, testkit.OperationInvoke, staleID)
	if !ok {
		t.Fatalf("runtime operations = %#v, want live invoke for %q", operations, staleID)
	}
	if !f.matchesResolvedDispatch(liveOperation.BrowserID, liveOperation.TargetID, liveOperation.Generation, liveOperation.FrameID, liveOperation.ToolName, liveOperation.Input) {
		t.Fatalf("live dispatch = %+v, want resolved target/frame/name/generation and input", liveOperation)
	}
	invocation, err := f.session.WaitForInvocation(context.Background())
	if err != nil {
		t.Fatalf("observe live target invocation: %v", err)
	}
	if invocation.ID != staleID || !f.matchesResolvedDispatch(invocation.BrowserID, invocation.TargetID, invocation.Generation, invocation.FrameID, invocation.ToolName, invocation.Input) {
		t.Fatalf("live target invocation = %+v, want exact dispatch provenance", invocation)
	}
}

// assertLiveOutcome accepts either the pre-fix stale success or a corrected
// classified freshness failure, and logs which one the broker produced.
func (f *queryTraceFixture) assertLiveOutcome(t *testing.T, liveEnvelope webmcp.ToolResultEnvelope, staleID webmcp.InvocationID, staleOutput json.RawMessage) queryTraceLiveData {
	t.Helper()
	var liveData queryTraceLiveData
	if !liveEnvelope.OK {
		if liveEnvelope.Error == nil || !webmcp.IsKnownErrorCode(webmcp.ErrorCode(liveEnvelope.Error.Code)) {
			t.Fatalf("live freshness failure = %#v, want a known classified error", liveEnvelope.Error)
		}
		t.Logf("corrected broker rejected the unproven early terminal at the result boundary: code=%s details=%v", liveEnvelope.Error.Code, liveEnvelope.Error.Details)
		return liveData
	}
	if err := json.Unmarshal(liveEnvelope.Data, &liveData); err != nil {
		t.Fatalf("decode live invocation data: %v", err)
	}
	if liveData.InvocationID != staleID || liveData.ToolRef != f.resolved.Ref || liveData.Status != string(webmcp.InvocationCompleted) || !jsonEqual(liveData.Output, staleOutput) {
		t.Fatalf("live caller payload = %+v, want the injected stale empty result", liveData)
	}
	t.Logf("confirmed first divergence at broker result reconciliation: an early terminal for %q became a live success before EventToolInvoked provenance was consumed", staleID)
	return liveData
}

func (f *queryTraceFixture) runDirectQuery(t *testing.T, freshOutput json.RawMessage) (webmcp.InvokeResult, WebMCPDirectInvocation) {
	t.Helper()
	var directResult webmcp.InvokeResult
	directDataValue, directErr := direct.RunOperation(context.Background(), func(ctx context.Context, directBroker webmcp.Broker, _ config.BrowserConfig) (any, error) {
		var err error
		directResult, err = f.invokeDirect(ctx, directBroker, freshOutput)
		if err != nil {
			return nil, err
		}
		return WebMCPDirectInvocation{
			InvocationID: string(directResult.InvocationID),
			ToolRef:      string(f.resolved.Ref),
			Status:       string(directResult.State),
			Output:       append(json.RawMessage(nil), directResult.Output...),
		}, nil
	}, f.broker, config.BrowserConfig{})
	if directErr != nil {
		t.Fatalf("execute direct query path: %v", directErr)
	}
	directData, ok := directDataValue.(WebMCPDirectInvocation)
	if !ok {
		t.Fatalf("direct result type = %T, want WebMCPDirectInvocation", directDataValue)
	}
	return directResult, directData
}

// invokeDirect dispatches the resolved tool through the direct broker,
// proves the target received the exact invocation, then releases it with the
// fresh output and waits for the terminal result.
func (f *queryTraceFixture) invokeDirect(ctx context.Context, directBroker webmcp.Broker, freshOutput json.RawMessage) (webmcp.InvokeResult, error) {
	eventCursor := f.runtime.EventCursor()
	result, err := directBroker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: f.resolved.Ref, Input: json.RawMessage(`{}`), Reason: "direct query trace"})
	if err != nil {
		return result, err
	}
	invocation, err := f.session.WaitForInvocation(ctx)
	if err != nil {
		return result, err
	}
	if invocation.ID != result.BrowserInvocationID || !f.matchesResolvedDispatch(invocation.BrowserID, invocation.TargetID, invocation.Generation, invocation.FrameID, invocation.ToolName, invocation.Input) {
		return result, &queryTraceMismatchError{label: "direct target invocation", got: invocation, want: "exact selected target/frame/name/generation and input"}
	}
	if _, err := f.runtime.WaitForPublishedEvent(ctx, eventCursor, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.InvocationID == result.BrowserInvocationID
	}); err != nil {
		return result, err
	}
	if _, err := directBroker.Selected(ctx); err != nil {
		return result, err
	}
	if err := f.session.ReleaseInvocation(result.BrowserInvocationID, freshOutput); err != nil {
		return result, err
	}
	return directops.WaitInvocation(ctx, directBroker, result)
}

func (f *queryTraceFixture) assertSelectionUnchanged(t *testing.T) {
	t.Helper()
	selectedAfter, err := f.broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("read selected page after trace: %v", err)
	}
	if selectedAfter.Key != f.selected.Key || selectedAfter.Generation != f.selected.Generation {
		t.Fatalf("selected page changed during trace: before=%+v after=%+v", f.selected, selectedAfter)
	}
}

// assertQueryTraceEventOrder requires the stale terminal to precede the live
// invocation event, and the direct terminal to follow its own invocation.
func assertQueryTraceEventOrder(t *testing.T, events []testkit.PublishedEvent, staleID, directID webmcp.InvocationID, generation uint64) {
	t.Helper()
	find := func(eventType webmcp.BrowserEventType, id webmcp.InvocationID, label string) webmcp.BrowserEvent {
		event, ok := firstQueryTracePublishedEvent(events, eventType, id)
		if !ok {
			t.Fatalf("published events = %#v, want %s", events, label)
		}
		return event
	}
	staleTerminal := find(webmcp.EventToolResponded, staleID, "injected stale terminal")
	liveInvoked := find(webmcp.EventToolInvoked, staleID, "live invocation event")
	if staleTerminal.Sequence >= liveInvoked.Sequence || staleTerminal.Generation != liveInvoked.Generation {
		t.Fatalf("browser event order = stale terminal %+v, live invocation %+v; want terminal before invocation in same generation", staleTerminal, liveInvoked)
	}
	directInvoked := find(webmcp.EventToolInvoked, directID, "direct invocation event")
	directTerminal := find(webmcp.EventToolResponded, directID, "direct terminal event")
	if directInvoked.Sequence >= directTerminal.Sequence || directTerminal.Generation != generation {
		t.Fatalf("direct browser event order = invocation %+v, terminal %+v; want terminal after invocation in generation %d", directInvoked, directTerminal, generation)
	}
	if directID == staleID {
		t.Fatalf("direct protocol ID reused stale ID %q", staleID)
	}
}

type queryTraceMismatchError struct {
	label string
	got   any
	want  string
}

func (e *queryTraceMismatchError) Error() string {
	return e.label + " mismatch: got " + marshalQueryTraceValue(e.got) + ", want " + e.want
}

func marshalQueryTraceValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<unencodable>"
	}
	return string(encoded)
}

func firstQueryTraceOperation(operations []testkit.Operation, kind testkit.OperationKind, id webmcp.InvocationID) (testkit.Operation, bool) {
	for _, operation := range operations {
		if operation.Kind == kind && operation.InvocationID == id {
			return operation, true
		}
	}
	return testkit.Operation{}, false
}

func firstQueryTracePublishedEvent(events []testkit.PublishedEvent, eventType webmcp.BrowserEventType, id webmcp.InvocationID) (webmcp.BrowserEvent, bool) {
	for _, published := range events {
		if published.Event.Type == eventType && published.Event.InvocationID == id {
			return published.Event, true
		}
	}
	return webmcp.BrowserEvent{}, false
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
