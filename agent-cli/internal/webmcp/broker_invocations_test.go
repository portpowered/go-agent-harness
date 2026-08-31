package webmcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestStatefulBrokerSerializesTargetAdmissionsUntilTerminalResponse(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "fixture", Loopback: true}
	readOnly := true
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", URL: "https://fixture.test/"},
					testkit.WithInitialCatalog(
						webmcp.ToolDescriptor{Name: "write_state", FrameID: "frame-1", InputSchema: []byte(`{"type":"object"}`)},
						webmcp.ToolDescriptor{Name: "read_state", FrameID: "frame-1", InputSchema: []byte(`{"type":"object"}`), Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly}},
					),
				),
			},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: staticDiscoverer{candidate},
		IDs:        ids,
		Clock:      clock,
	})
	defer func() { _ = broker.Close() }()

	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	refs := make(map[string]webmcp.ToolRef, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		refs[tool.Name] = tool.Ref
	}
	if len(refs) != 2 {
		t.Fatalf("catalog = %#v, want two tools", snapshot.Tools)
	}

	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open fixture handle: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession("tab-a")
	if session == nil {
		t.Fatal("fixture session is nil")
	}
	session.BlockInvocations()

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	watch := broker.Watch(watchCtx)

	firstDone := make(chan invocationCall, 1)
	go func() {
		result, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{
			ToolRef:     refs["write_state"],
			Input:       []byte(`{"step":1}`),
			ModelCallID: "model-call-1",
			SessionID:   "session-1",
			ResponseID:  "response-1",
		})
		firstDone <- invocationCall{result: result, err: invokeErr}
	}()
	firstCreated := assertInvocationCreated(t, watch, refs["write_state"])
	first := receiveInvocationCall(t, firstDone)
	if first.err != nil || first.result.State != webmcp.InvocationDispatched || first.result.InvocationID == "" {
		t.Fatalf("first invoke = %#v, %v; want dispatched", first.result, first.err)
	}
	if firstCreated.InvocationID != first.result.InvocationID {
		t.Fatalf("first creation ID = %q, dispatched ID = %q; want one public ID", firstCreated.InvocationID, first.result.InvocationID)
	}
	firstStarted := assertInvocationStarted(t, watch, first.result.InvocationID, refs["write_state"])
	if firstStarted.ToolName != "write_state" || firstStarted.Reason != "dispatched" {
		t.Fatalf("first dispatch event = %#v, want canonical tool and dispatched reason", firstStarted)
	}
	firstRecord, err := session.WaitForInvocation(context.Background())
	if err != nil {
		t.Fatalf("observe first target invocation: %v", err)
	}
	if firstRecord.ID != first.result.InvocationID {
		t.Fatalf("first IDs = broker %q, target %q; want correlation", first.result.InvocationID, firstRecord.ID)
	}
	firstSnapshot, ok := broker.Invocation(first.result.InvocationID)
	if !ok {
		t.Fatal("first invocation missing from registry while response is blocked")
	}
	if firstSnapshot.Operation != webmcp.OperationUnknown || firstSnapshot.ModelCallID != "model-call-1" || firstSnapshot.SessionID != "session-1" || firstSnapshot.ResponseID != "response-1" || firstSnapshot.Deadline.IsZero() {
		t.Fatalf("first registry snapshot = %#v, want correlation metadata and unknown operation", firstSnapshot)
	}

	secondDone := make(chan invocationCall, 1)
	go func() {
		result, invokeErr := broker.Invoke(context.Background(), webmcp.InvokeRequest{
			ToolRef: refs["read_state"],
			Input:   []byte(`{"step":2}`),
		})
		secondDone <- invocationCall{result: result, err: invokeErr}
	}()
	secondCreated := assertInvocationCreated(t, watch, refs["read_state"])
	if pending := broker.PendingInvocations(); len(pending) != 2 || pending[0].ID != first.result.InvocationID || pending[1].ID != secondCreated.InvocationID || pending[1].State != webmcp.InvocationQueued {
		t.Fatalf("broker pending registry after second admission = %#v, want dispatched head and queued tail", pending)
	}
	if invocations := session.Invocations(); len(invocations) != 1 {
		t.Fatalf("target invocations before first terminal = %#v, want only FIFO head", invocations)
	}
	select {
	case second := <-secondDone:
		t.Fatalf("second invoke returned before first terminal: %#v", second)
	default:
	}

	if err := session.ReleaseInvocation(first.result.InvocationID, []byte(`{"first":90071992547409931234567890}`)); err != nil {
		t.Fatalf("release first invocation: %v", err)
	}
	secondRecord, err := session.WaitForInvocation(context.Background())
	if err != nil {
		t.Fatalf("observe second target invocation: %v", err)
	}
	assertInvocationTerminal(t, watch, first.result.InvocationID)
	second := receiveInvocationCall(t, secondDone)
	if second.err != nil || second.result.State != webmcp.InvocationDispatched || second.result.InvocationID != secondRecord.ID {
		t.Fatalf("second invoke = %#v, %v; target record = %#v", second.result, second.err, secondRecord)
	}
	assertInvocationStarted(t, watch, second.result.InvocationID, refs["read_state"])
	if pending := session.PendingInvocations(); len(pending) != 1 || pending[0].ID != second.result.InvocationID {
		t.Fatalf("target pending after first terminal = %#v, want only second", pending)
	}

	if err := session.ReleaseInvocation(second.result.InvocationID, []byte(`["second",true]`)); err != nil {
		t.Fatalf("release second invocation: %v", err)
	}
	firstResult, err := broker.WaitInvocation(context.Background(), first.result.InvocationID)
	if err != nil {
		t.Fatalf("wait first terminal result: %v", err)
	}
	if firstResult.State != webmcp.InvocationCompleted || string(firstResult.Output) != `{"first":90071992547409931234567890}` {
		t.Fatalf("first terminal result = %#v, want raw object output", firstResult)
	}
	secondResult, err := broker.WaitInvocation(context.Background(), second.result.InvocationID)
	if err != nil {
		t.Fatalf("wait second terminal result: %v", err)
	}
	if secondResult.State != webmcp.InvocationCompleted || string(secondResult.Output) != `["second",true]` {
		t.Fatalf("second terminal result = %#v, want raw array output", secondResult)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("broker pending registry = %#v, want empty after terminal reconciliation", pending)
	}
	if pending := session.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("target pending registry = %#v, want empty", pending)
	}

	var invokeOperations []testkit.Operation
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationInvoke {
			invokeOperations = append(invokeOperations, operation)
		}
	}
	if len(invokeOperations) != 2 || invokeOperations[0].InvocationID != first.result.InvocationID || invokeOperations[1].InvocationID != second.result.InvocationID || invokeOperations[0].Sequence >= invokeOperations[1].Sequence {
		t.Fatalf("invoke operation order = %#v, want correlated FIFO order", invokeOperations)
	}
}

func TestStatefulBrokerBoundsSerializedInvocationResults(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page"},
				testkit.WithInitialCatalog(pageTool("read_state", "frame-1", `{}`)),
			)},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        runtime,
		Discoverer:     staticDiscoverer{candidate},
		IDs:            ids,
		Clock:          clock,
		MaxResultBytes: 128,
	})
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open fixture handle: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession("tab-a")
	session.BlockInvocations()
	dispatched, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: snapshot.Tools[0].Ref, Input: []byte(`{}`)})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if _, err := session.WaitForInvocation(context.Background()); err != nil {
		t.Fatalf("observe invocation: %v", err)
	}
	if err := session.ReleaseInvocation(dispatched.InvocationID, []byte(`{"large":"this output exceeds the configured envelope limit"}`)); err != nil {
		t.Fatalf("release invocation: %v", err)
	}
	terminal, err := broker.WaitInvocation(context.Background(), dispatched.InvocationID)
	if err != nil {
		t.Fatalf("wait terminal result: %v", err)
	}
	if terminal.State != webmcp.InvocationError || terminal.ErrorCode != string(webmcp.ErrorResultTooLarge) || terminal.Output != nil {
		t.Fatalf("terminal result = %#v, want result_too_large without output", terminal)
	}
	if terminal.ErrorDetails["tool_ref"] != string(snapshot.Tools[0].Ref) || terminal.ErrorDetails["limit_bytes"] != 128 {
		t.Fatalf("result-too-large details = %#v, want bounded safe details", terminal.ErrorDetails)
	}
	if pending := broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending registry = %#v, want empty", pending)
	}
}

type invocationCall struct {
	result webmcp.InvokeResult
	err    error
}

func receiveInvocationCall(t *testing.T, results <-chan invocationCall) invocationCall {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for broker invocation admission")
		return invocationCall{}
	}
}

func assertInvocationCreated(t *testing.T, events <-chan webmcp.BrokerEvent, ref webmcp.ToolRef) webmcp.BrokerEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != webmcp.BrokerEventInvocationCreated || event.ToolRef != ref || event.State != webmcp.InvocationQueued || event.InvocationID == "" {
			t.Fatalf("broker event = %#v, want queued creation for %q", event, ref)
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for invocation-created event")
		return webmcp.BrokerEvent{}
	}
}

func assertInvocationStarted(t *testing.T, events <-chan webmcp.BrokerEvent, id webmcp.InvocationID, ref webmcp.ToolRef) webmcp.BrokerEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != webmcp.BrokerEventInvocationCreated || event.ToolRef != ref || event.State != webmcp.InvocationDispatched || event.InvocationID != id {
			t.Fatalf("broker event = %#v, want dispatched start for %q", event, id)
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for invocation-dispatched event")
		return webmcp.BrokerEvent{}
	}
}

func assertInvocationTerminal(t *testing.T, events <-chan webmcp.BrokerEvent, id webmcp.InvocationID) webmcp.BrokerEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != webmcp.BrokerEventInvocationTerminal || event.InvocationID != id {
			t.Fatalf("broker event = %#v, want terminal for %q", event, id)
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for invocation-terminal event")
		return webmcp.BrokerEvent{}
	}
}
