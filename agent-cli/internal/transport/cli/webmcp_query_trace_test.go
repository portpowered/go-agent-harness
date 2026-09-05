package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
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
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDSource("query-trace")
	candidate := webmcp.BrowserCandidate{ID: "browser-margin", Product: "fixture", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-margin",
		Type:      "page",
		Title:     "Margin",
		URL:       "https://margin.fixture/",
		Origin:    "https://margin.fixture",
	}
	readOnly := true
	tool := webmcp.ToolDescriptor{
		Name:        "list_documents",
		Description: "List documents in the current Margin page.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly},
		FrameID:     "frame-margin",
		Origin:      target.Origin,
	}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				target,
				testkit.WithContext(webmcp.PageContext{
					Generation:      1,
					CatalogReady:    true,
					CatalogEvidence: "scripted_fixture",
				}),
				testkit.WithInitialCatalog(tool),
			)},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        runtime,
		IDs:            ids,
		Clock:          clock,
		ToolRefFactory: webmcp.StableToolRef,
	})
	t.Cleanup(func() {
		_ = broker.Close()
		_ = runtime.Close()
	})

	selected, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID})
	if err != nil {
		t.Fatalf("select target: %v", err)
	}
	if selected.Key.BrowserID != candidate.ID || selected.Key.TargetID != target.ID || selected.Generation != 1 {
		t.Fatalf("selected page = %+v, want browser %q target %q generation 1", selected, candidate.ID, target.ID)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("resolve page catalog: %v", err)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != tool.Name {
		t.Fatalf("resolved catalog = %#v, want list_documents", snapshot.Tools)
	}
	resolved := snapshot.Tools[0]

	pageTools := webmcpTools.NewBrokerToolSet(broker)
	definitions, err := pageTools.PageToolDefinitionsWithError(context.Background())
	if err != nil {
		t.Fatalf("publish live page definitions: %v", err)
	}
	if len(definitions) != 1 || definitions[0].Name != tool.Name {
		t.Fatalf("live page definitions = %#v, want list_documents", definitions)
	}

	session := runtime.Browser(candidate.ID).TargetSession(target.ID)
	if session == nil {
		t.Fatal("scripted target session is nil")
	}
	staleOutput := json.RawMessage(`{"count":0,"documents":[]}`)
	freshOutput := json.RawMessage(`{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`)
	// DeterministicIDs has not allocated an invocation yet. This response is
	// therefore the protocol terminal that the first live call will receive
	// before its own EventToolInvoked has crossed the broker boundary.
	staleID := webmcp.InvocationID("inv-000001")
	if err := session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: staleID,
		Status:       "Completed",
		Output:       staleOutput,
		Generation:   selected.Generation,
	}); err != nil {
		t.Fatalf("inject stale terminal response: %v", err)
	}

	liveContext, cancelLive := context.WithTimeout(context.Background(), time.Second)
	liveResponse, err := pageTools.Executor().Execute(liveContext, messages.ToolCall{
		ID:        "call-live-list-documents",
		Name:      "list_documents",
		Arguments: `{}`,
	})
	cancelLive()
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

	operations := runtime.Operations()
	liveOperation, ok := firstQueryTraceOperation(operations, testkit.OperationInvoke, staleID)
	if !ok {
		t.Fatalf("runtime operations = %#v, want live invoke for %q", operations, staleID)
	}
	if liveOperation.BrowserID != resolved.BrowserID || liveOperation.TargetID != resolved.TargetID || liveOperation.Generation != resolved.Generation || liveOperation.FrameID != resolved.FrameID || liveOperation.ToolName != resolved.Name || !jsonEqual(liveOperation.Input, []byte(`{}`)) {
		t.Fatalf("live dispatch = %+v, want resolved target/frame/name/generation and input", liveOperation)
	}
	liveTargetInvocation, err := session.WaitForInvocation(context.Background())
	if err != nil {
		t.Fatalf("observe live target invocation: %v", err)
	}
	if liveTargetInvocation.ID != staleID || liveTargetInvocation.BrowserID != resolved.BrowserID || liveTargetInvocation.TargetID != resolved.TargetID || liveTargetInvocation.Generation != resolved.Generation || liveTargetInvocation.FrameID != resolved.FrameID || liveTargetInvocation.ToolName != resolved.Name || !jsonEqual(liveTargetInvocation.Input, []byte(`{}`)) {
		t.Fatalf("live target invocation = %+v, want exact dispatch provenance", liveTargetInvocation)
	}

	var liveData struct {
		InvocationID webmcp.InvocationID `json:"invocation_id"`
		ToolRef      webmcp.ToolRef      `json:"tool_ref"`
		Status       string              `json:"status"`
		Output       json.RawMessage     `json:"output"`
	}
	if liveEnvelope.OK {
		if err := json.Unmarshal(liveEnvelope.Data, &liveData); err != nil {
			t.Fatalf("decode live invocation data: %v", err)
		}
		if liveData.InvocationID != staleID || liveData.ToolRef != resolved.Ref || liveData.Status != string(webmcp.InvocationCompleted) || !jsonEqual(liveData.Output, staleOutput) {
			t.Fatalf("live caller payload = %+v, want the injected stale empty result", liveData)
		}
		t.Logf("confirmed first divergence at broker result reconciliation: an early terminal for %q became a live success before EventToolInvoked provenance was consumed", staleID)
	} else {
		if liveEnvelope.Error == nil || !webmcp.IsKnownErrorCode(webmcp.ErrorCode(liveEnvelope.Error.Code)) {
			t.Fatalf("live freshness failure = %#v, want a known classified error", liveEnvelope.Error)
		}
		t.Logf("corrected broker rejected the unproven early terminal at the result boundary: code=%s details=%v", liveEnvelope.Error.Code, liveEnvelope.Error.Details)
	}

	var directResult webmcp.InvokeResult
	directDataValue, directErr := runWebMCPDirectOperation(context.Background(), func(ctx context.Context, directBroker webmcp.Broker, _ config.BrowserConfig) (any, error) {
		eventCursor := runtime.EventCursor()
		directResult, err = directBroker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: resolved.Ref, Input: json.RawMessage(`{}`), Reason: "direct query trace"})
		if err != nil {
			return nil, err
		}
		directTargetInvocation, waitErr := session.WaitForInvocation(ctx)
		if waitErr != nil {
			return nil, waitErr
		}
		if directTargetInvocation.ID != directResult.BrowserInvocationID || directTargetInvocation.BrowserID != resolved.BrowserID || directTargetInvocation.TargetID != resolved.TargetID || directTargetInvocation.Generation != resolved.Generation || directTargetInvocation.FrameID != resolved.FrameID || directTargetInvocation.ToolName != resolved.Name || !jsonEqual(directTargetInvocation.Input, []byte(`{}`)) {
			return nil, &queryTraceMismatchError{label: "direct target invocation", got: directTargetInvocation, want: "exact selected target/frame/name/generation and input"}
		}
		if _, waitErr := runtime.WaitForPublishedEvent(ctx, eventCursor, func(event webmcp.BrowserEvent) bool {
			return event.Type == webmcp.EventToolInvoked && event.InvocationID == directResult.BrowserInvocationID
		}); waitErr != nil {
			return nil, waitErr
		}
		if _, waitErr := directBroker.Selected(ctx); waitErr != nil {
			return nil, waitErr
		}
		if err := session.ReleaseInvocation(directResult.BrowserInvocationID, freshOutput); err != nil {
			return nil, err
		}
		directResult, err = waitDirectInvocation(ctx, directBroker, directResult)
		if err != nil {
			return nil, err
		}
		return WebMCPDirectInvocation{
			InvocationID: string(directResult.InvocationID),
			ToolRef:      string(resolved.Ref),
			Status:       string(directResult.State),
			Output:       append(json.RawMessage(nil), directResult.Output...),
		}, nil
	}, broker, config.BrowserConfig{})
	if directErr != nil {
		t.Fatalf("execute direct query path: %v", directErr)
	}
	directData, ok := directDataValue.(WebMCPDirectInvocation)
	if !ok {
		t.Fatalf("direct result type = %T, want WebMCPDirectInvocation", directDataValue)
	}
	if directResult.State != webmcp.InvocationCompleted || directResult.BrowserInvocationID == staleID || directData.ToolRef != string(resolved.Ref) || directData.Status != string(webmcp.InvocationCompleted) || !jsonEqual(directData.Output, freshOutput) {
		t.Fatalf("direct result = %+v / %+v, want fresh completed payload", directResult, directData)
	}

	if selectedAfter, err := broker.Selected(context.Background()); err != nil {
		t.Fatalf("read selected page after trace: %v", err)
	} else if selectedAfter.Key != selected.Key || selectedAfter.Generation != selected.Generation {
		t.Fatalf("selected page changed during trace: before=%+v after=%+v", selected, selectedAfter)
	}
	directOperation, ok := firstQueryTraceOperation(runtime.Operations(), testkit.OperationInvoke, directResult.BrowserInvocationID)
	if !ok {
		t.Fatalf("runtime operations after direct call = %#v, want direct invoke", runtime.Operations())
	}
	if directOperation.BrowserID != resolved.BrowserID || directOperation.TargetID != resolved.TargetID || directOperation.Generation != resolved.Generation || directOperation.FrameID != resolved.FrameID || directOperation.ToolName != resolved.Name || !jsonEqual(directOperation.Input, []byte(`{}`)) {
		t.Fatalf("direct dispatch = %+v, want same resolved target/frame/name/generation and input", directOperation)
	}

	events := runtime.PublishedEvents()
	staleTerminal, ok := firstQueryTracePublishedEvent(events, webmcp.EventToolResponded, staleID)
	if !ok {
		t.Fatalf("published events = %#v, want injected stale terminal", events)
	}
	liveInvoked, ok := firstQueryTracePublishedEvent(events, webmcp.EventToolInvoked, staleID)
	if !ok {
		t.Fatalf("published events = %#v, want live invocation event", events)
	}
	if staleTerminal.Sequence >= liveInvoked.Sequence || staleTerminal.Generation != liveInvoked.Generation {
		t.Fatalf("browser event order = stale terminal %+v, live invocation %+v; want terminal before invocation in same generation", staleTerminal, liveInvoked)
	}
	directInvoked, ok := firstQueryTracePublishedEvent(events, webmcp.EventToolInvoked, directResult.BrowserInvocationID)
	if !ok {
		t.Fatalf("published events = %#v, want direct invocation event", events)
	}
	directTerminal, ok := firstQueryTracePublishedEvent(events, webmcp.EventToolResponded, directResult.BrowserInvocationID)
	if !ok {
		t.Fatalf("published events = %#v, want direct terminal event", events)
	}
	if directInvoked.Sequence >= directTerminal.Sequence || directTerminal.Generation != resolved.Generation {
		t.Fatalf("direct browser event order = invocation %+v, terminal %+v; want terminal after invocation in generation %d", directInvoked, directTerminal, resolved.Generation)
	}
	if directResult.BrowserInvocationID == staleID {
		t.Fatalf("direct protocol ID reused stale ID %q", staleID)
	}

	if liveEnvelope.OK && !jsonEqual(liveData.Output, staleOutput) {
		t.Fatalf("live output changed from controlled stale payload: %s", liveData.Output)
	}
	if !jsonEqual(directData.Output, freshOutput) {
		t.Fatalf("direct output = %s, want %s", directData.Output, freshOutput)
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
