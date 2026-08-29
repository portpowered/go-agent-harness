package webmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcptools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestStatefulBrokerReevaluatesLateCatalogOnTheSameAttachment(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-late", Product: "fixture", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(webmcp.Target{
			BrowserID: candidate.ID,
			ID:        "tab-late",
			Type:      "page",
			URL:       "https://fixture.test/late",
		}),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:     runtime,
		Discoverer:  staticDiscoverer{candidate},
		CatalogWait: 25 * time.Millisecond,
	})
	defer func() { _ = broker.Close() }()

	_, err := broker.Select(context.Background(), webmcp.TargetSelector{
		BrowserID: candidate.ID,
		TargetID:  "tab-late",
	})
	if err == nil {
		t.Fatal("select succeeded without catalog evidence")
	}
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("select error = %T %v, want classified catalog error", err, err)
	}
	if classified.Code != webmcp.ErrorBrowserProtocol || !classified.Retryable {
		t.Fatalf("select error = %+v, want retryable browser protocol error", classified)
	}
	if classified.Details["reason_code"] != "page_tools_unverified" || classified.Details["reason"] != "deadline_exceeded" {
		t.Fatalf("select details = %#v, want page_tools_unverified/deadline_exceeded", classified.Details)
	}
	if classified.Details["browser_id"] != string(candidate.ID) || classified.Details["target_id"] != "tab-late" || classified.Details["generation"] != uint64(1) {
		t.Fatalf("select identity details = %#v, want original browser/target/generation", classified.Details)
	}

	selected, err := broker.Selected(context.Background())
	if err != nil {
		t.Fatalf("selected after diagnostic timeout: %v", err)
	}
	if !selected.Connected || selected.Ready || selected.CatalogReady || selected.Generation != 1 {
		t.Fatalf("selected after timeout = %+v, want connected unready generation one", selected)
	}

	toolSet := webmcptools.NewBrokerToolSet(broker)
	response, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "late-list-before-registration",
		Name:      webmcp.ListToolsToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("model-facing list before registration: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode retryable model result: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserProtocol) || !envelope.Error.Retryable {
		t.Fatalf("model-facing list result = %#v, want retryable browser protocol failure", envelope)
	}
	if envelope.Error.Details["reason_code"] != "page_tools_unverified" || envelope.Error.Details["reason"] != "deadline_exceeded" {
		t.Fatalf("model-facing details = %#v, want catalog deadline details", envelope.Error.Details)
	}

	handle := runtime.Browser(candidate.ID)
	if handle == nil {
		t.Fatal("runtime did not retain selected browser handle")
	}
	session := handle.TargetSession("tab-late")
	if session == nil {
		t.Fatal("runtime did not retain selected target session")
	}
	lateTool := webmcp.ToolDescriptor{
		Name:        "late_tool",
		FrameID:     "frame-late",
		Description: "late fixture tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	session.SetAutoResponse("Completed", json.RawMessage(`{"registered":true}`))
	if err := session.EmitToolsAdded(lateTool); err != nil {
		t.Fatalf("emit late catalog: %v", err)
	}

	snapshot, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list late catalog: %v", err)
	}
	if snapshot.Generation != 1 || len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != lateTool.Name {
		t.Fatalf("late catalog = %+v, want one generation-one tool", snapshot)
	}
	if !snapshot.Context.Connected || !snapshot.Context.CatalogReady || !snapshot.Context.Ready {
		t.Fatalf("late catalog context = %+v, want ready connected selection", snapshot.Context)
	}

	invocation, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{
		ToolRef: snapshot.Tools[0].Ref,
		Input:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("invoke late tool: %v", err)
	}
	terminal, err := broker.WaitInvocation(context.Background(), invocation.InvocationID)
	if err != nil {
		t.Fatalf("wait late invocation: %v", err)
	}
	if terminal.State != webmcp.InvocationCompleted || string(terminal.Output) != `{"registered":true}` {
		t.Fatalf("late invocation result = %+v, want one completed response", terminal)
	}

	operations := runtime.Operations()
	counts := map[testkit.OperationKind]int{}
	for _, operation := range operations {
		counts[operation.Kind]++
	}
	if counts[testkit.OperationAttach] != 1 || counts[testkit.OperationOpen] != 1 || counts[testkit.OperationListTargets] != 2 {
		t.Fatalf("attachment operations = %#v, want one open, two attach target lookups, and one attach", counts)
	}
	if counts[testkit.OperationInvoke] != 1 {
		t.Fatalf("invoke operations = %#v, want one late-tool invocation", counts)
	}
}
