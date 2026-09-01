package webmcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcptools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type freshnessFixture struct {
	broker  *webmcp.StatefulBroker
	session *testkit.ScriptedTargetSession
	runtime *testkit.ScriptedBrowserRuntime
	ref     webmcp.ToolRef
}

func newFreshnessFixture(t *testing.T) freshnessFixture {
	t.Helper()
	readOnly := true
	tool := pageTool("list_documents", "frame-1", `{"type":"object","additionalProperties":false}`)
	tool.Annotations.ReadOnly = &readOnly
	return newFreshnessFixtureWithTool(t, tool)
}

func newFreshnessFixtureWithTool(t *testing.T, tool webmcp.ToolDescriptor) freshnessFixture {
	t.Helper()
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-freshness", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{
				testkit.NewTargetConfig(
					webmcp.Target{BrowserID: candidate.ID, ID: "tab-a", Type: "page", URL: "https://freshness.fixture/"},
					testkit.WithInitialCatalog(tool),
				),
			},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        staticDiscoverer{candidate},
		IDs:               ids,
		Clock:             clock,
		InvocationTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = broker.Close() })
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: "tab-a"}); err != nil {
		t.Fatalf("select target: %v", err)
	}
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(catalog.Tools) != 1 {
		t.Fatalf("catalog = %#v, want one query tool", catalog.Tools)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open fixture handle: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession("tab-a")
	if session == nil {
		t.Fatal("fixture session is nil")
	}
	return freshnessFixture{broker: broker, session: session, runtime: runtime, ref: catalog.Tools[0].Ref}
}

func TestStatefulBrokerFreshnessPreservesFreshNonEmptyAndEmptyResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "nonempty", output: `{"documents":[{"id":"welcome"}]}`},
		{name: "empty", output: `{"documents":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFreshnessFixture(t)
			fixture.session.BlockInvocations()

			dispatched, err := fixture.broker.Invoke(context.Background(), webmcp.InvokeRequest{
				ToolRef: fixture.ref,
				Input:   json.RawMessage(`{}`),
				Reason:  "freshness test",
			})
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if dispatched.State != webmcp.InvocationDispatched || dispatched.BrowserInvocationID == "" {
				t.Fatalf("dispatch result = %#v, want a browser-correlated dispatch", dispatched)
			}
			invoked, err := waitForFreshnessInvocation(t, fixture, dispatched.BrowserInvocationID)
			if err != nil {
				t.Fatalf("wait for target invocation: %v", err)
			}
			if invoked.ID != dispatched.BrowserInvocationID || invoked.Generation == 0 || invoked.FrameID != "frame-1" || invoked.ToolName != "list_documents" {
				t.Fatalf("target invocation = %#v, want exact dispatch provenance", invoked)
			}
			if err := fixture.session.ReleaseInvocation(dispatched.BrowserInvocationID, json.RawMessage(test.output)); err != nil {
				t.Fatalf("release invocation: %v", err)
			}
			terminal, err := fixture.broker.WaitInvocation(context.Background(), dispatched.InvocationID)
			if err != nil {
				t.Fatalf("wait terminal result: %v", err)
			}
			if terminal.State != webmcp.InvocationCompleted || string(terminal.Output) != test.output || terminal.ErrorCode != "" {
				t.Fatalf("terminal result = %#v, want fresh successful output", terminal)
			}
		})
	}
}

func TestStatefulBrokerAcceptsSynchronousOwnedTerminalAfterInvokedProvenance(t *testing.T) {
	fixture := newFreshnessFixture(t)
	fixture.session.SetAutoResponse("Completed", json.RawMessage(`{"documents":[{"id":"synchronous"}]}`))

	result, err := fixture.broker.Invoke(context.Background(), webmcp.InvokeRequest{
		ToolRef: fixture.ref,
		Input:   json.RawMessage(`{}`),
		Reason:  "synchronous deterministic target response",
	})
	if err != nil {
		t.Fatalf("invoke with synchronous target response: %v", err)
	}
	terminal, err := fixture.broker.WaitInvocation(context.Background(), result.InvocationID)
	if err != nil {
		t.Fatalf("wait synchronous target result: %v", err)
	}
	if terminal.State != webmcp.InvocationCompleted || terminal.ErrorCode != "" || string(terminal.Output) != `{"documents":[{"id":"synchronous"}]}` {
		t.Fatalf("synchronous terminal = %#v, want provenance-backed success", terminal)
	}
}

func TestStatefulBrokerRejectsEarlyTerminalWithRetryableFreshnessEnvelope(t *testing.T) {
	fixture := newFreshnessFixture(t)
	staleID := webmcp.InvocationID("inv-000001")
	staleOutput := `{"documents":[]}`
	if err := fixture.session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: staleID,
		Status:       "Completed",
		Output:       json.RawMessage(staleOutput),
	}); err != nil {
		t.Fatalf("inject stale terminal: %v", err)
	}

	arguments, err := json.Marshal(map[string]string{
		"tool_ref":   string(fixture.ref),
		"input_json": `{}`,
		"reason":     "verify stale result handling",
	})
	if err != nil {
		t.Fatalf("encode invocation arguments: %v", err)
	}
	callContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := webmcptools.NewBrokerToolSet(fixture.broker).Executor().Execute(callContext, messages.ToolCall{
		ID:        "freshness-call",
		Name:      webmcp.InvokeToolName,
		Arguments: string(arguments),
	})
	if err != nil {
		t.Fatalf("execute broker tool: %v", err)
	}
	envelope, err := webmcptools.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode freshness envelope: %v; content=%s", err, response.Content)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) {
		t.Fatalf("freshness envelope = %#v, want classified failure", envelope)
	}
	if !envelope.Error.Retryable {
		t.Fatalf("freshness error = %#v, want safe read-only retry", envelope.Error)
	}
	details := envelope.Error.Details
	if details["phase"] != "result_freshness" || details["freshness_phase"] != "terminal_provenance" || details["reason_code"] == "" || details["terminal_observed"] != true || details["tool_ref"] != string(fixture.ref) || details["target_id"] != "tab-a" {
		t.Fatalf("freshness details = %#v, want bounded correlation and recovery metadata", details)
	}
	if _, leaked := details["safe_retryable"]; leaked || strings.Contains(response.Content, staleOutput) {
		t.Fatalf("freshness response leaked internal retry marker or stale output: %s", response.Content)
	}
	if pending := fixture.broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending invocations = %#v, want terminal failure", pending)
	}
	if invoked, err := waitForFreshnessInvocation(t, fixture, staleID); err != nil || invoked.ID != staleID {
		t.Fatalf("target invocation = %#v/%v, want the newly dispatched correlated call", invoked, err)
	}
}

func TestStatefulBrokerDoesNotRecommendRetryForUnprovenMutation(t *testing.T) {
	readOnly := false
	tool := pageTool("update_document", "frame-1", `{"type":"object","additionalProperties":false}`)
	tool.Annotations.ReadOnly = &readOnly
	fixture := newFreshnessFixtureWithTool(t, tool)
	staleID := webmcp.InvocationID("inv-000001")
	staleOutput := `{"updated":true}`
	if err := fixture.session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: staleID,
		Status:       "Completed",
		Output:       json.RawMessage(staleOutput),
	}); err != nil {
		t.Fatalf("inject out-of-order mutation terminal: %v", err)
	}

	arguments, err := json.Marshal(map[string]string{
		"tool_ref":   string(fixture.ref),
		"input_json": `{}`,
		"reason":     "verify mutation freshness handling",
	})
	if err != nil {
		t.Fatalf("encode mutation invocation arguments: %v", err)
	}
	response, err := webmcptools.NewBrokerToolSet(fixture.broker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "mutation-freshness-call",
		Name:      webmcp.InvokeToolName,
		Arguments: string(arguments),
	})
	if err != nil {
		t.Fatalf("execute mutation broker tool: %v", err)
	}
	envelope, err := webmcptools.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode mutation freshness envelope: %v; content=%s", err, response.Content)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) {
		t.Fatalf("mutation freshness envelope = %#v, want classified failure", envelope)
	}
	if envelope.Error.Retryable {
		t.Fatalf("mutation freshness error = %#v, want non-retryable side-effect uncertainty", envelope.Error)
	}
	recovery, _ := envelope.Error.Details["recovery"].(string)
	if !strings.Contains(strings.ToLower(recovery), "do not retry") || !strings.Contains(strings.ToLower(recovery), "reconcile") {
		t.Fatalf("mutation recovery = %q, want explicit no-retry reconciliation guidance", recovery)
	}
	if envelope.Error.Details["phase"] != "result_freshness" || envelope.Error.Details["freshness_phase"] != "terminal_provenance" || envelope.Error.Details["reason_code"] != "terminal_before_invocation" || envelope.Error.Details["side_effect_unknown"] != true {
		t.Fatalf("mutation freshness details = %#v, want out-of-order provenance classification", envelope.Error.Details)
	}
	if strings.Contains(response.Content, staleOutput) {
		t.Fatalf("mutation freshness response leaked stale page output: %s", response.Content)
	}
	if invoked, err := waitForFreshnessInvocation(t, fixture, staleID); err != nil || invoked.ID != staleID {
		t.Fatalf("mutation target invocation = %#v/%v, want dispatched invocation", invoked, err)
	}
}

func TestStatefulBrokerRejectsOutstandingObservedInvocationIDReuse(t *testing.T) {
	fixture := newFreshnessFixture(t)
	watchContext, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	events := fixture.broker.Watch(watchContext)
	collisionID := webmcp.InvocationID("inv-000001")
	if err := fixture.session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		Generation:   1,
		FrameID:      "frame-1",
		ToolName:     "list_documents",
		InvocationID: collisionID,
		Input:        json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("emit outstanding external invocation: %v", err)
	}
	observed := waitForBrokerEvent(t, events, webmcp.BrokerEventInvocationCreated)
	if observed.InvocationID != collisionID || observed.Reason != "browser_observed" {
		t.Fatalf("observed invocation event = %#v, want outstanding external invocation", observed)
	}

	result, err := fixture.broker.Invoke(context.Background(), webmcp.InvokeRequest{
		ToolRef: fixture.ref,
		Input:   json.RawMessage(`{}`),
		Reason:  "verify invocation ID collision fence",
	})
	if err == nil {
		t.Fatal("invoke with outstanding protocol ID succeeded; want correlation failure")
	}
	if result.State != webmcp.InvocationError || result.ErrorCode != string(webmcp.ErrorInvocationFailed) || result.BrowserInvocationID != "" {
		t.Fatalf("collision result = %#v, want terminal correlation failure without owned browser ID", result)
	}
	if result.ErrorDetails["phase"] != "correlation" || result.ErrorDetails["side_effect_unknown"] != true {
		t.Fatalf("collision details = %#v, want side-effect-unknown correlation failure", result.ErrorDetails)
	}

	if err := fixture.session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: collisionID,
		Status:       "Completed",
		Output:       json.RawMessage(`{"poison":true}`),
	}); err != nil {
		t.Fatalf("emit delayed external terminal: %v", err)
	}
	terminal, err := fixture.broker.WaitInvocation(context.Background(), result.InvocationID)
	if err != nil {
		t.Fatalf("wait collision result: %v", err)
	}
	if terminal.ErrorCode != string(webmcp.ErrorInvocationFailed) || len(terminal.Output) != 0 || terminal.ErrorDetails["phase"] != "correlation" {
		t.Fatalf("collision terminal = %#v, want original failure after delayed external terminal", terminal)
	}
	if err := fixture.session.ReleaseInvocation(collisionID, json.RawMessage(`{"owned":true}`)); err != nil {
		t.Fatalf("release rejected owned target record: %v", err)
	}
}

func TestStatefulBrokerDoesNotCrossCorrelateUnrelatedOrConsecutiveResults(t *testing.T) {
	fixture := newFreshnessFixture(t)
	fixture.session.BlockInvocations()

	first, err := fixture.broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: fixture.ref, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("first invoke: %v", err)
	}
	if _, err := waitForFreshnessInvocation(t, fixture, first.BrowserInvocationID); err != nil {
		t.Fatalf("wait for first target invocation: %v", err)
	}
	if err := fixture.session.Emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		InvocationID: "unrelated-invocation",
		Status:       "Completed",
		Output:       json.RawMessage(`{"poison":true}`),
	}); err != nil {
		t.Fatalf("inject unrelated terminal: %v", err)
	}
	if err := fixture.session.ReleaseInvocation(first.BrowserInvocationID, json.RawMessage(`{"call":1}`)); err != nil {
		t.Fatalf("release first invocation: %v", err)
	}
	firstTerminal, err := fixture.broker.WaitInvocation(context.Background(), first.InvocationID)
	if err != nil {
		t.Fatalf("wait first terminal: %v", err)
	}
	if string(firstTerminal.Output) != `{"call":1}` {
		t.Fatalf("first terminal output = %s, want first call output", firstTerminal.Output)
	}

	second, err := fixture.broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: fixture.ref, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("second invoke: %v", err)
	}
	if second.BrowserInvocationID == first.BrowserInvocationID {
		t.Fatalf("consecutive browser invocation IDs reused: first=%q second=%q", first.BrowserInvocationID, second.BrowserInvocationID)
	}
	if _, err := waitForFreshnessInvocation(t, fixture, second.BrowserInvocationID); err != nil {
		t.Fatalf("wait for second target invocation: %v", err)
	}
	if err := fixture.session.ReleaseInvocation(second.BrowserInvocationID, json.RawMessage(`{"call":2}`)); err != nil {
		t.Fatalf("release second invocation: %v", err)
	}
	secondTerminal, err := fixture.broker.WaitInvocation(context.Background(), second.InvocationID)
	if err != nil {
		t.Fatalf("wait second terminal: %v", err)
	}
	if string(secondTerminal.Output) != `{"call":2}` {
		t.Fatalf("second terminal output = %s, want second call output", secondTerminal.Output)
	}
	if pending := fixture.broker.PendingInvocations(); len(pending) != 0 {
		t.Fatalf("pending invocations = %#v, want empty after consecutive calls", pending)
	}
}

func waitForFreshnessInvocation(t *testing.T, fixture freshnessFixture, id webmcp.InvocationID) (testkit.InvocationRecord, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	invocation, err := fixture.session.WaitForInvocation(ctx)
	if err != nil {
		return testkit.InvocationRecord{}, err
	}
	if _, err := fixture.runtime.WaitForPublishedEvent(ctx, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.InvocationID == id
	}); err != nil {
		return testkit.InvocationRecord{}, err
	}
	if _, err := fixture.broker.Selected(ctx); err != nil {
		return testkit.InvocationRecord{}, err
	}
	return invocation, nil
}
