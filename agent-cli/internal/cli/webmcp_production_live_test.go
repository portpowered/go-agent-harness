package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

func TestProductionWebMCPDirectCommandsRehydrateSelectionAndOperateLiveBroker(t *testing.T) {
	server, browserID, targetID, _ := newProductionTestEndpoint(t)
	defer server.Close()

	candidate := webmcp.BrowserCandidate{ID: webmcp.BrowserID(browserID)}
	target := webmcp.Target{
		BrowserID:        candidate.ID,
		ID:               "raw-tab",
		Type:             "page",
		Title:            "Live fixture",
		URL:              "https://fixture.test/live?secret=query#fragment",
		Origin:           "https://fixture.test",
		WebSocketURL:     "ws" + server.URL[len("http"):] + "/devtools/page/raw-tab",
		ContinuityMarker: "document-live",
	}
	tool := webmcp.ToolDescriptor{
		Name:        "set_state",
		Description: "Mutate fixture state",
		FrameID:     "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`),
		Origin:      target.Origin,
	}
	runtime := newReopeningProductionRuntime(candidate, target, tool, json.RawMessage(`{"mutated":true}`))
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
	store := NewFileWebMCPSelectionStore(configDir)
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
		WithWebMCPProductionSelectionStore(store),
	)

	selected := executeDirectCommand(t, configDir, store, factory, "select", "--browser", browserID, "--tab", targetID, "--json")
	selectedEnvelope := requireDirectSuccess(t, selected)
	var selectedData WebMCPDirectContext
	decodeDirectData(t, selectedEnvelope.Data, &selectedData)
	if selectedData.BrowserID != browserID || selectedData.TargetID != targetID || selectedData.Generation != 1 || selectedData.ToolCount != 1 {
		t.Fatalf("selected context = %+v", selectedData)
	}

	contextResult := executeDirectCommand(t, configDir, store, factory, "context", "--json")
	contextEnvelope := requireDirectSuccess(t, contextResult)
	var contextData WebMCPDirectContext
	decodeDirectData(t, contextEnvelope.Data, &contextData)
	if contextData.BrowserID != browserID || contextData.TargetID != targetID || !contextData.CatalogReady || contextData.CatalogGeneration != 1 || contextData.ToolCount != 1 {
		t.Fatalf("rehydrated context = %+v", contextData)
	}

	toolsResult := executeDirectCommand(t, configDir, store, factory, "tools", "--json")
	toolsEnvelope := requireDirectSuccess(t, toolsResult)
	var toolsData WebMCPDirectToolsData
	decodeDirectData(t, toolsEnvelope.Data, &toolsData)
	if toolsData.BrowserID != browserID || toolsData.TargetID != targetID || toolsData.Generation != 1 || len(toolsData.Tools) != 1 || !webmcp.IsValidToolRef(webmcp.ToolRef(toolsData.Tools[0].Ref)) {
		t.Fatalf("rehydrated tools = %+v", toolsData)
	}

	invokeResult := executeDirectCommand(t, configDir, store, factory, "invoke", "--tool-ref", toolsData.Tools[0].Ref, "--input-json", `{"value":7}`, "--timeout", "5s", "--json")
	invokeEnvelope := requireDirectSuccess(t, invokeResult)
	var invokeData WebMCPDirectInvocation
	decodeDirectData(t, invokeEnvelope.Data, &invokeData)
	if invokeData.Status != string(webmcp.InvocationCompleted) || invokeData.ToolRef != toolsData.Tools[0].Ref || string(invokeData.Output) != `{"mutated":true}` {
		t.Fatalf("live invocation = %+v", invokeData)
	}

	watch := executeDirectCommand(t, configDir, store, factory, "watch", "--once", "--json")
	watchEnvelope := requireDirectSuccess(t, watch)
	var watchData WebMCPDirectWatchData
	decodeDirectData(t, watchEnvelope.Data, &watchData)
	if watchData.Status != webmcpDirectWatchStatusOnce || len(watchData.Events) != 1 || watchData.Events[0].Type != string(webmcp.BrokerEventSelected) || watchData.Events[0].Version != webmcp.BrowserEventsVersion || watchData.Events[0].BrowserID != browserID || watchData.Events[0].TargetID != targetID || watchData.Events[0].Sequence == 0 {
		t.Fatalf("live watch = %+v", watchData)
	}

	operations := runtime.operations()
	attachCount := countProductionRuntimeOperations(operations, testkit.OperationAttach)
	detachCount := countProductionRuntimeOperations(operations, testkit.OperationDetach)
	if attachCount == 0 || detachCount != attachCount {
		t.Fatalf("external attach/detach counts = %d/%d; operations=%+v", attachCount, detachCount, operations)
	}
	if hasTestkitOperation(operations, testkit.OperationCloseTarget) {
		t.Fatalf("production direct commands closed an externally owned target: %+v", operations)
	}
	if openCount := countProductionRuntimeOperations(operations, testkit.OperationOpen); openCount != countProductionRuntimeOperations(operations, testkit.OperationCloseHandle) {
		t.Fatalf("browser handle cleanup count = %d opens/%d closes; operations=%+v", openCount, countProductionRuntimeOperations(operations, testkit.OperationCloseHandle), operations)
	}
	foundInvocation := false
	for _, operation := range operations {
		if operation.Kind != testkit.OperationInvoke {
			continue
		}
		foundInvocation = true
		if operation.TargetID != target.ID || operation.FrameID != tool.FrameID || operation.ToolName != tool.Name || string(operation.Input) != `{"value":7}` {
			t.Fatalf("live invocation operation = %+v", operation)
		}
	}
	if !foundInvocation {
		t.Fatalf("live runtime did not receive invocation: %+v", operations)
	}
	for _, secret := range []string{"secret", "#fragment", "query"} {
		if containsDirectProductionOutput(secret, selected.stdout, contextResult.stdout, toolsResult.stdout, invokeResult.stdout, watch.stdout) {
			t.Fatalf("direct production output exposed %q", secret)
		}
	}
}

type reopeningProductionRuntime struct {
	mu        sync.Mutex
	candidate webmcp.BrowserCandidate
	target    webmcp.Target
	tool      webmcp.ToolDescriptor
	output    json.RawMessage

	children []*testkit.ScriptedBrowserRuntime
	sessions []*testkit.ScriptedTargetSession
}

func newReopeningProductionRuntime(candidate webmcp.BrowserCandidate, target webmcp.Target, tool webmcp.ToolDescriptor, output json.RawMessage) *reopeningProductionRuntime {
	return &reopeningProductionRuntime{
		candidate: candidate,
		target:    target,
		tool:      tool,
		output:    append(json.RawMessage(nil), output...),
	}
}

func (r *reopeningProductionRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	child := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(r.candidate, testkit.NewTargetConfig(r.target, testkit.WithInitialCatalog(r.tool), testkit.WithAutoResponse(r.output))))
	handle, err := child.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.children = append(r.children, child)
	r.mu.Unlock()
	return &reopeningProductionHandle{owner: r, delegate: handle}, nil
}

func (r *reopeningProductionRuntime) recordSession(session *testkit.ScriptedTargetSession) {
	r.mu.Lock()
	r.sessions = append(r.sessions, session)
	r.mu.Unlock()
}

func (r *reopeningProductionRuntime) operations() []testkit.Operation {
	r.mu.Lock()
	children := append([]*testkit.ScriptedBrowserRuntime(nil), r.children...)
	r.mu.Unlock()
	var operations []testkit.Operation
	for _, child := range children {
		operations = append(operations, child.Operations()...)
	}
	return operations
}

type reopeningProductionHandle struct {
	owner    *reopeningProductionRuntime
	delegate webmcp.BrowserHandle
}

func (h *reopeningProductionHandle) Candidate() webmcp.BrowserCandidate {
	return h.delegate.Candidate()
}

func (h *reopeningProductionHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	return h.delegate.ListTargets(ctx)
}

func (h *reopeningProductionHandle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	return h.delegate.Activate(ctx, targetID)
}

func (h *reopeningProductionHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	session, err := h.delegate.Attach(ctx, targetID, ownership)
	if err != nil {
		return nil, err
	}
	if scripted, ok := session.(*testkit.ScriptedTargetSession); ok {
		h.owner.recordSession(scripted)
	}
	return session, nil
}

func (h *reopeningProductionHandle) Close() error { return h.delegate.Close() }

func countProductionRuntimeOperations(operations []testkit.Operation, want testkit.OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == want {
			count++
		}
	}
	return count
}

func containsDirectProductionOutput(needle string, outputs ...string) bool {
	for _, output := range outputs {
		if len(output) > 0 && strings.Contains(output, needle) {
			return true
		}
	}
	return false
}

var _ webmcp.BrowserRuntime = (*reopeningProductionRuntime)(nil)
var _ webmcp.BrowserHandle = (*reopeningProductionHandle)(nil)
