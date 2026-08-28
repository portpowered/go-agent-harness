package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestBrowserScriptAdapterDrivesBrokerWithoutTransport(t *testing.T) {
	script := browserScriptAdapterScript(false)
	clock := NewFakeClock(100)
	runtime, err := NewBrowserScriptRuntime(script, WithFixtureClock(clock), WithFixtureIDSource(NewDeterministicIDSource("adapter")))
	if err != nil {
		t.Fatalf("NewBrowserScriptRuntime: %v", err)
	}
	adapter, err := NewBrowserScriptAdapter(script, runtime)
	if err != nil {
		t.Fatalf("NewBrowserScriptAdapter: %v", err)
	}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        adapter,
		Discoverer:     adapter,
		IDs:            NewDeterministicIDSource("broker"),
		Clock:          clock,
		Timers:         clock,
		Ownership:      webmcp.TargetOwnershipHarnessOwned,
		ToolRefFactory: webmcp.StableToolRef,
	})

	candidates, err := broker.Discover(context.Background(), webmcp.DiscoverOptions{BrowserID: "fixture-browser", ExplicitOnly: true})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("Discover = %#v, %v", candidates, err)
	}
	page, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: "fixture-browser", TargetID: "tab-1"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if page.Origin != "https://fixture.test" || !page.Ready {
		t.Fatalf("selected page = %+v", page)
	}
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil || len(catalog.Tools) != 1 {
		t.Fatalf("ListTools = %#v, %v", catalog, err)
	}
	tool := catalog.Tools[0]
	invocation, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: tool.Ref, Input: json.RawMessage(`{}`), Reason: "adapter test"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	terminal, err := broker.WaitInvocation(context.Background(), invocation.InvocationID)
	if err != nil {
		t.Fatalf("WaitInvocation: %v", err)
	}
	if terminal.State != webmcp.InvocationCompleted || string(terminal.Output) != `{"ok":true}` {
		t.Fatalf("terminal result = %+v", terminal)
	}

	if err := adapter.Navigate(context.Background(), "https://fixture.test/next"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	selected, err := broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected after navigation: %v", err)
	}
	if selected.Generation != 2 || selected.URL != "https://fixture.test/next" || selected.Ready {
		t.Fatalf("navigated page = %+v", selected)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("broker Close: %v", err)
	}
	if err := runtime.Complete(); err != nil {
		t.Fatalf("runtime Complete: %v", err)
	}
	operations := runtime.Operations()
	if len(operations) != 5 || operations[0].Type != OperationEnableLifecycle || operations[1].Type != OperationEnableWebMCP || operations[2].Type != OperationInvokeTool || operations[3].Type != OperationNavigate || operations[4].Type != OperationCloseTarget {
		t.Fatalf("consumed operations = %#v", operations)
	}
}

func TestBrowserScriptAdapterCancellationUsesBrowserCorrelationID(t *testing.T) {
	script := browserScriptAdapterScript(true)
	runtime, err := NewBrowserScriptRuntime(script)
	if err != nil {
		t.Fatalf("NewBrowserScriptRuntime: %v", err)
	}
	adapter, err := NewBrowserScriptAdapter(script, runtime)
	if err != nil {
		t.Fatalf("NewBrowserScriptAdapter: %v", err)
	}
	clock := NewFakeClock(0)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        adapter,
		Discoverer:     adapter,
		IDs:            NewDeterministicIDSource("broker"),
		Clock:          clock,
		Timers:         clock,
		Ownership:      webmcp.TargetOwnershipHarnessOwned,
		ToolRefFactory: webmcp.StableToolRef,
	})
	if _, err := broker.Discover(context.Background(), webmcp.DiscoverOptions{}); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: "fixture-browser", TargetID: "tab-1"}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	invocation, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: catalog.Tools[0].Ref, Input: json.RawMessage(`{}`), Reason: "cancel test"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := broker.Cancel(context.Background(), webmcp.CancelRequest{InvocationID: invocation.InvocationID, Reason: "test cancellation"}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	terminal, err := broker.WaitInvocation(context.Background(), invocation.InvocationID)
	if err != nil {
		t.Fatalf("WaitInvocation: %v", err)
	}
	if terminal.State != webmcp.InvocationCanceled {
		t.Fatalf("terminal state = %q, want canceled", terminal.State)
	}
	if len(runtime.PendingInvocationIDs()) != 0 {
		t.Fatalf("fixture pending invocations = %#v", runtime.PendingInvocationIDs())
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("broker Close: %v", err)
	}
	if err := runtime.Complete(); err != nil {
		t.Fatalf("runtime Complete: %v", err)
	}
}

func TestBrowserScriptAdapterRejectsWrongEndpoint(t *testing.T) {
	script := browserScriptAdapterScript(false)
	runtime, err := NewBrowserScriptRuntime(script)
	if err != nil {
		t.Fatalf("NewBrowserScriptRuntime: %v", err)
	}
	adapter, err := NewBrowserScriptAdapter(script, runtime)
	if err != nil {
		t.Fatalf("NewBrowserScriptAdapter: %v", err)
	}
	if _, err := adapter.Open(context.Background(), webmcp.BrowserCandidate{ID: "other-browser"}); !errors.Is(err, webmcp.ErrBrowserNotFound) {
		t.Fatalf("Open wrong browser = %v, want ErrBrowserNotFound", err)
	}
	if _, err := adapter.Discover(context.Background(), webmcp.DiscoverOptions{BrowserID: "other-browser"}); !errors.Is(err, webmcp.ErrBrowserNotFound) {
		t.Fatalf("Discover wrong browser = %v, want ErrBrowserNotFound", err)
	}
	_ = runtime.Close()
}

func browserScriptAdapterScript(cancellable bool) BrowserScript {
	operations := []BrowserScriptOperation{
		{Expect: OperationExpectation{Type: OperationEnableLifecycle}, Result: json.RawMessage(`{}`)},
		{Expect: OperationExpectation{Type: OperationEnableWebMCP}, Result: json.RawMessage(`{}`), Emit: []EmittedEvent{{
			Type: EmittedToolsAdded,
			Tools: []ToolDescriptor{{
				Name:        "read_state",
				Description: "Read fixture state",
				FrameID:     "frame-1",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			}},
		}}},
	}
	if cancellable {
		operations = append(operations,
			BrowserScriptOperation{Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "read_state", Input: json.RawMessage(`{}`)}, Result: json.RawMessage(`{"invocation_id":"browser-invocation"}`)},
			BrowserScriptOperation{Expect: OperationExpectation{Type: OperationCancelTool, InvocationID: "browser-invocation"}, Emit: []EmittedEvent{{Type: EmittedToolResponded, InvocationID: "browser-invocation", Status: "Canceled", Error: json.RawMessage(`{"code":"canceled"}`)}}},
		)
	} else {
		operations = append(operations,
			BrowserScriptOperation{Expect: OperationExpectation{Type: OperationInvokeTool, FrameID: "frame-1", ToolName: "read_state", Input: json.RawMessage(`{}`)}, Result: json.RawMessage(`{"invocation_id":"browser-invocation"}`), Emit: []EmittedEvent{{Type: EmittedToolResponded, InvocationID: "browser-invocation", Status: "Completed", Output: json.RawMessage(`{"ok":true}`)}}},
			BrowserScriptOperation{Expect: OperationExpectation{Type: OperationNavigate, URL: "https://fixture.test/next"}},
		)
	}
	operations = append(operations, BrowserScriptOperation{Expect: OperationExpectation{Type: OperationCloseTarget}})
	return BrowserScript{
		Version: BrowserScriptVersion,
		Endpoint: BrowserEndpoint{
			Version: EndpointVersionInfo{Browser: "Chrome/Fixture", ProtocolVersion: "1.3", WebSocketDebuggerURL: "ws://fixture/browser"},
			Targets: []BrowserTarget{{ID: "tab-1", Type: "page", Title: "Fixture", URL: "https://fixture.test/", WebSocketDebuggerURL: "ws://fixture/page/tab-1"}},
		},
		Operations: operations,
	}
}
