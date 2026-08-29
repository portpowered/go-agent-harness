package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestProductionWebMCPDirectCommandsCancelAcrossFreshProcessesAndRecover(t *testing.T) {
	server, browserID, targetID, _ := newProductionTestEndpoint(t)
	defer server.Close()

	candidate := webmcp.BrowserCandidate{ID: webmcp.BrowserID(browserID)}
	target := webmcp.Target{
		BrowserID:        candidate.ID,
		ID:               "raw-tab",
		Type:             "page",
		Title:            "Pending fixture",
		URL:              "https://fixture.test/pending?secret=query#fragment",
		Origin:           "https://fixture.test",
		WebSocketURL:     "ws" + server.URL[len("http"):] + "/devtools/page/raw-tab",
		ContinuityMarker: "document-pending",
		Generation:       1,
	}
	tool := webmcp.ToolDescriptor{
		Name:        "set_state",
		Description: "Mutate fixture state",
		FrameID:     "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`),
		Origin:      target.Origin,
	}
	runtime := newCrossProcessProductionRuntime(candidate, target, tool)
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
	requireDirectSuccess(t, selected)
	toolsResult := executeDirectCommand(t, configDir, store, factory, "tools", "--json")
	toolsEnvelope := requireDirectSuccess(t, toolsResult)
	var toolsData WebMCPDirectToolsData
	decodeDirectData(t, toolsEnvelope.Data, &toolsData)
	if len(toolsData.Tools) != 1 {
		t.Fatalf("live cancellation tools = %+v", toolsData.Tools)
	}

	invokeDone := make(chan directCommandResult, 1)
	go func() {
		invokeDone <- executeDirectCommand(t, configDir, store, factory, "invoke", "--tool-ref", toolsData.Tools[0].Ref, "--input-json", `{"value":7}`, "--timeout", "5s", "--json")
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	browserInvocationID, err := runtime.waitForDispatch(waitCtx)
	waitCancel()
	if err != nil {
		t.Fatalf("wait for pending dispatch: %v", err)
	}

	cancelResult := executeDirectCommand(t, configDir, store, factory, "cancel", "--invocation", string(browserInvocationID), "--json")
	if cancelResult.err != nil {
		t.Fatalf("fresh-process cancel: %v\nstdout=%s\nstderr=%s", cancelResult.err, cancelResult.stdout, cancelResult.stderr)
	}
	cancelEnvelope := requireDirectSuccess(t, cancelResult)
	var cancelData WebMCPDirectCancelData
	decodeDirectData(t, cancelEnvelope.Data, &cancelData)
	if cancelData.InvocationID != string(browserInvocationID) || cancelData.Status != "cancel_requested" {
		t.Fatalf("live cancellation result = %+v", cancelData)
	}

	var invokeResult directCommandResult
	select {
	case invokeResult = <-invokeDone:
	case <-time.After(time.Second):
		t.Fatal("pending invoke did not observe cancellation")
	}
	if invokeResult.err == nil {
		t.Fatal("pending invoke unexpectedly succeeded after cancellation")
	}
	var receipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(invokeResult.stderr), &receipt); err != nil {
		t.Fatalf("decode live dispatch receipt: %v; stderr=%q", err, invokeResult.stderr)
	}
	if receipt.InvocationID != string(browserInvocationID) || receipt.ToolRef != toolsData.Tools[0].Ref || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("live dispatch receipt = %+v", receipt)
	}
	invokeEnvelope := decodeDirectEnvelope(t, invokeResult.stdout)
	if invokeEnvelope.OK || invokeEnvelope.Error == nil || invokeEnvelope.Error.Code != string(webmcp.ErrorInvocationCanceled) || invokeEnvelope.Error.Details["invocation_id"] != string(browserInvocationID) {
		t.Fatalf("canceled invoke envelope = %+v", invokeEnvelope)
	}

	recovered := executeDirectCommand(t, configDir, store, factory, "invoke", "--tool-ref", toolsData.Tools[0].Ref, "--input-json", `{"value":8}`, "--timeout", "5s", "--json")
	recoveredEnvelope := requireDirectSuccess(t, recovered)
	var recoveredData WebMCPDirectInvocation
	decodeDirectData(t, recoveredEnvelope.Data, &recoveredData)
	if recoveredData.Status != string(webmcp.InvocationCompleted) || string(recoveredData.Output) != `{"recovered":true}` {
		t.Fatalf("post-cancel recovery = %+v", recoveredData)
	}
	if runtime.cancelCount() != 1 || runtime.invocationCount() != 2 {
		t.Fatalf("live cancellation operations = cancels:%d invokes:%d", runtime.cancelCount(), runtime.invocationCount())
	}
	for _, output := range []string{cancelResult.stdout, invokeResult.stdout, invokeResult.stderr, recovered.stdout, recovered.stderr} {
		for _, secret := range []string{"secret", "#fragment", "query", "127.0.0.1"} {
			if strings.Contains(output, secret) {
				t.Fatalf("live cancellation output exposed %q: %s", secret, output)
			}
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

// crossProcessProductionRuntime keeps browser-side invocation state shared
// across independently opened handles while giving each command its own event
// stream. That is the production boundary exercised by the fresh cancel
// command: the second broker has no local invocation registry, but the browser
// still knows the dispatched protocol ID.
type crossProcessProductionRuntime struct {
	mu          sync.Mutex
	candidate   webmcp.BrowserCandidate
	target      webmcp.Target
	tool        webmcp.ToolDescriptor
	invocations map[webmcp.InvocationID]struct{}
	invokeCount int
	sessions    map[*crossProcessProductionSession]struct{}
	dispatched  chan webmcp.InvocationID
	canceled    []webmcp.InvocationID
}

func newCrossProcessProductionRuntime(candidate webmcp.BrowserCandidate, target webmcp.Target, tool webmcp.ToolDescriptor) *crossProcessProductionRuntime {
	return &crossProcessProductionRuntime{
		candidate:   candidate,
		target:      target,
		tool:        tool,
		invocations: make(map[webmcp.InvocationID]struct{}),
		sessions:    make(map[*crossProcessProductionSession]struct{}),
		dispatched:  make(chan webmcp.InvocationID, 4),
	}
}

func (r *crossProcessProductionRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &crossProcessProductionHandle{runtime: r, candidate: candidate}, nil
}

func (r *crossProcessProductionRuntime) waitForDispatch(ctx context.Context) (webmcp.InvocationID, error) {
	select {
	case invocationID := <-r.dispatched:
		return invocationID, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r *crossProcessProductionRuntime) invocationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.invokeCount
}

func (r *crossProcessProductionRuntime) cancelCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.canceled)
}

func (r *crossProcessProductionRuntime) broadcastLocked(event webmcp.BrowserEvent) {
	for session := range r.sessions {
		session.send(event)
	}
}

type crossProcessProductionHandle struct {
	runtime   *crossProcessProductionRuntime
	candidate webmcp.BrowserCandidate
	mu        sync.Mutex
	closed    bool
}

func (h *crossProcessProductionHandle) Candidate() webmcp.BrowserCandidate { return h.candidate }

func (h *crossProcessProductionHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, webmcp.ErrClosed
	}
	h.runtime.mu.Lock()
	target := h.runtime.target
	h.runtime.mu.Unlock()
	target.BrowserID = h.candidate.ID
	return []webmcp.Target{target}, nil
}

func (h *crossProcessProductionHandle) Activate(ctx context.Context, _ webmcp.TargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return webmcp.ErrClosed
	}
	return nil
}

func (h *crossProcessProductionHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	h.mu.Unlock()
	h.runtime.mu.Lock()
	target := h.runtime.target
	tool := h.runtime.tool
	if targetID != target.ID {
		h.runtime.mu.Unlock()
		return nil, webmcp.ErrTargetNotFound
	}
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: h.candidate.ID, TargetID: target.ID},
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		Connected:  true,
	}
	session := newCrossProcessProductionSession(h.runtime, target, page, ownership, tool)
	h.runtime.sessions[session] = struct{}{}
	h.runtime.mu.Unlock()
	return session, nil
}

func (h *crossProcessProductionHandle) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

type crossProcessProductionSession struct {
	runtime   *crossProcessProductionRuntime
	target    webmcp.Target
	page      webmcp.PageContext
	ownership webmcp.TargetOwnership
	tool      webmcp.ToolDescriptor
	events    chan webmcp.BrowserEvent
	done      chan struct{}
	mu        sync.Mutex
	closed    bool
	ready     bool
}

func newCrossProcessProductionSession(runtime *crossProcessProductionRuntime, target webmcp.Target, page webmcp.PageContext, ownership webmcp.TargetOwnership, tool webmcp.ToolDescriptor) *crossProcessProductionSession {
	return &crossProcessProductionSession{
		runtime:   runtime,
		target:    target,
		page:      page,
		ownership: ownership,
		tool:      tool,
		events:    make(chan webmcp.BrowserEvent, 32),
		done:      make(chan struct{}),
	}
}

func (s *crossProcessProductionSession) send(event webmcp.BrowserEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- event
}

func (s *crossProcessProductionSession) Context() webmcp.PageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := s.page
	page.Ready = s.ready
	return page
}

func (s *crossProcessProductionSession) Ownership() webmcp.TargetOwnership { return s.ownership }

func (s *crossProcessProductionSession) EnableWebMCP(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	s.ready = true
	s.mu.Unlock()
	tool := s.tool
	tool.BrowserID = s.target.BrowserID
	tool.TargetID = s.target.ID
	tool.Generation = s.target.Generation
	s.send(webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, BrowserID: s.target.BrowserID, TargetID: s.target.ID, Generation: s.target.Generation, Tools: []webmcp.ToolDescriptor{tool}})
	return nil
}

func (s *crossProcessProductionSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *crossProcessProductionSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.runtime.mu.Lock()
	s.runtime.invokeCount++
	invocationID := webmcp.InvocationID(fmt.Sprintf("browser-live-%d", s.runtime.invokeCount))
	s.runtime.invocations[invocationID] = struct{}{}
	s.runtime.dispatched <- invocationID
	respond := s.runtime.invokeCount > 1
	if respond {
		delete(s.runtime.invocations, invocationID)
	}
	s.runtime.broadcastLocked(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		BrowserID:    s.target.BrowserID,
		TargetID:     s.target.ID,
		Generation:   s.target.Generation,
		FrameID:      frameID,
		ToolName:     toolName,
		Input:        append(json.RawMessage(nil), input...),
		InvocationID: invocationID,
	})
	if respond {
		s.runtime.broadcastLocked(webmcp.BrowserEvent{
			Type:         webmcp.EventToolResponded,
			BrowserID:    s.target.BrowserID,
			TargetID:     s.target.ID,
			Generation:   s.target.Generation,
			InvocationID: invocationID,
			Status:       "Completed",
			Output:       json.RawMessage(`{"recovered":true}`),
		})
	}
	s.runtime.mu.Unlock()
	return invocationID, nil
}

func (s *crossProcessProductionSession) CancelWebMCP(ctx context.Context, invocationID webmcp.InvocationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.runtime.mu.Lock()
	if _, ok := s.runtime.invocations[invocationID]; !ok {
		s.runtime.mu.Unlock()
		return webmcp.ErrInvocationNotFound
	}
	delete(s.runtime.invocations, invocationID)
	s.runtime.canceled = append(s.runtime.canceled, invocationID)
	s.runtime.broadcastLocked(webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		BrowserID:    s.target.BrowserID,
		TargetID:     s.target.ID,
		Generation:   s.target.Generation,
		InvocationID: invocationID,
		Status:       "Canceled",
	})
	s.runtime.mu.Unlock()
	return nil
}

func (s *crossProcessProductionSession) Done() <-chan struct{} { return s.done }

func (s *crossProcessProductionSession) Err() error { return nil }

func (s *crossProcessProductionSession) Close() error {
	s.runtime.mu.Lock()
	if s.closed {
		s.runtime.mu.Unlock()
		return nil
	}
	delete(s.runtime.sessions, s)
	s.mu.Lock()
	s.closed = true
	close(s.events)
	close(s.done)
	s.mu.Unlock()
	s.runtime.mu.Unlock()
	return nil
}

var (
	_ webmcp.BrowserRuntime = (*crossProcessProductionRuntime)(nil)
	_ webmcp.BrowserHandle  = (*crossProcessProductionHandle)(nil)
	_ webmcp.TargetSession  = (*crossProcessProductionSession)(nil)
)
