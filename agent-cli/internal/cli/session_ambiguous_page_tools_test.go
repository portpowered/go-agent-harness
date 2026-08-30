package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionAmbiguousTabsPublishOnlySelectedPageTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	candidate := webmcp.BrowserCandidate{
		ID:       "browser-ambiguous",
		Source:   webmcp.DiscoverySourceExplicit,
		Product:  "scripted",
		Protocol: "1.3",
		HTTPURL:  "http://127.0.0.1:9222",
		Loopback: true,
		Explicit: true,
	}
	cubeTarget := webmcp.Target{
		BrowserID:             candidate.ID,
		ID:                    "tab-cube",
		Type:                  "page",
		Title:                 "Cubecade",
		URL:                   "https://cube.example.test/",
		Origin:                "https://cube.example.test",
		Generation:            1,
		WebMCPDomainSupported: true,
		PageToolsReady:        true,
		PageToolsKnown:        true,
		Eligible:              true,
	}
	marginTarget := webmcp.Target{
		BrowserID:             candidate.ID,
		ID:                    "tab-margin",
		Type:                  "page",
		Title:                 "Margin",
		URL:                   "https://margin.example.test/",
		Origin:                "https://margin.example.test",
		Generation:            1,
		WebMCPDomainSupported: true,
		PageToolsReady:        true,
		PageToolsKnown:        true,
		Eligible:              true,
	}
	cubeTool := webmcp.ToolDescriptor{
		Name:        "get_cube_state",
		Description: "Read the Cubecade state.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	marginTool := webmcp.ToolDescriptor{
		Name:        "get_margin_state",
		Description: "Read the Margin state.",
		FrameID:     "margin-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(cubeTarget, testkit.WithInitialCatalog(cubeTool), testkit.WithAutoResponse(json.RawMessage(`{"page":"cube"}`))),
		testkit.NewTargetConfig(marginTarget, testkit.WithInitialCatalog(marginTool), testkit.WithAutoResponse(json.RawMessage(`{"page":"margin"}`))),
	))
	discoveryService := &ambiguousSessionDiscovery{
		candidate: discovery.BrowserCandidate{
			ID:       string(candidate.ID),
			Source:   discovery.SourceExplicitCDPHTTP,
			Product:  candidate.Product,
			Protocol: candidate.Protocol,
			Loopback: true,
		},
		targets: []discovery.Target{
			ambiguousSessionLaneTarget(cubeTarget, 1),
			ambiguousSessionLaneTarget(marginTarget, 1),
		},
	}

	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = candidate.HTTPURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	cfg := browserCapabilityConfig(true)
	cfg.Browser = browser
	cfg.Model = config.ModelConfig{
		Provider: config.ProviderGrok,
		Grok:     &config.GrokConfig{Model: "ambiguous-session", APIKey: "unused"},
	}
	productionFactory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionDiscovery(discoveryService),
	)
	capabilities, err := NewSessionToolCapabilitiesFactory(nil, func(browser config.BrowserConfig) (webmcp.Broker, error) {
		return newSessionBrowserBrokerWithDoctorFactory(browser, productionFactory)
	})(cfg)
	if err != nil {
		t.Fatalf("construct session capabilities: %v", err)
	}
	defer func() {
		if closeErr := capabilities.Close(); closeErr != nil {
			t.Errorf("close session capabilities: %v", closeErr)
		}
		if closeErr := runtime.Close(); closeErr != nil {
			t.Errorf("close scripted browser runtime: %v", closeErr)
		}
	}()

	surface := resolveSessionToolSurface(ctx, capabilities)
	if surface.browserState != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("initial browser state = %q, want connected_unselected", surface.browserState)
	}
	assertAmbiguousPageSurface(t, surface.definitions, surface.base, nil, "initial CLI surface")
	unselectedRefresh, err := surface.refresh(ctx)
	if err != nil {
		t.Fatalf("unselected page refresh: %v", err)
	}
	assertAmbiguousPageSurface(t, unselectedRefresh, surface.base, nil, "unselected refresh surface")

	providerSession := newAmbiguousPageToolsSession()
	provider := &ambiguousPageToolsInferencer{session: providerSession}
	sessionCtx, cancelSession := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSession(sessionCtx, io.Discard, services.SessionRunOptions{
			Provider:               config.ProviderGrok,
			Model:                  "ambiguous-session",
			APIKey:                 "unused",
			LoadedConfig:           cfg,
			BrowserToolsEnabled:    true,
			WaitForClose:           true,
			ToolExecutor:           surface.executor,
			ToolDefinitions:        surface.definitions,
			ToolDefinitionBase:     surface.base,
			RefreshToolDefinitions: surface.refresh,
			BrowserWatch:           surface.browserWatch,
			SessionInferencer:      provider,
		})
	}()
	defer func() {
		cancelSession()
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("session loop shutdown: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("session loop did not stop after cancellation")
		}
	}()

	initialDefinitions := readAmbiguousPageToolsSessionUpdate(t, ctx, runErr, providerSession)
	assertAmbiguousPageSurface(t, initialDefinitions, surface.base, nil, "initial provider surface")

	listEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, webmcp.ListTabsToolName, `{"include_zero_tool_pages":true}`)
	if !listEnvelope.OK {
		t.Fatalf("list ambiguous tabs failed: %+v", listEnvelope.Error)
	}
	var tabs struct {
		Targets []struct {
			BrowserID string `json:"browser_id"`
			TargetID  string `json:"target_id"`
			Title     string `json:"title"`
			Eligible  bool   `json:"eligible"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(listEnvelope.Data, &tabs); err != nil {
		t.Fatalf("decode ambiguous tab list: %v", err)
	}
	if len(tabs.Targets) != 2 {
		t.Fatalf("ambiguous tab list = %#v, want both eligible tabs", tabs.Targets)
	}
	listed := make(map[string]string, len(tabs.Targets))
	for _, target := range tabs.Targets {
		if !target.Eligible || target.BrowserID != string(candidate.ID) {
			t.Fatalf("listed ambiguous target = %+v, want eligible target on %q", target, candidate.ID)
		}
		listed[target.Title] = target.TargetID
	}
	if listed[cubeTarget.Title] == "" || listed[marginTarget.Title] == "" {
		t.Fatalf("listed ambiguous target identities = %#v, want Cubecade and Margin", listed)
	}
	assertRuntimeHasNoOperation(t, runtime, testkit.OperationAttach, testkit.OperationEnableWebMCP, testkit.OperationInvoke)

	selectEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, webmcp.SelectTabToolName, `{"browser_id":"`+string(candidate.ID)+`","target_id":"`+listed[cubeTarget.Title]+`"}`)
	if !selectEnvelope.OK {
		t.Fatalf("exact Cubecade selection failed: %+v", selectEnvelope.Error)
	}
	selectedDefinitions := readAmbiguousPageToolsSessionUpdate(t, ctx, runErr, providerSession)
	assertAmbiguousPageSurface(t, selectedDefinitions, surface.base, []string{cubeTool.Name}, "selected provider surface")

	pageEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, cubeTool.Name, `{}`)
	if !pageEnvelope.OK {
		t.Fatalf("selected Cubecade page tool failed: %+v", pageEnvelope.Error)
	}
	var pageData struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(pageEnvelope.Data, &pageData); err != nil {
		t.Fatalf("decode selected Cubecade page result: %v", err)
	}
	if pageData.Status != string(webmcp.InvocationCompleted) || string(pageData.Output) != `{"page":"cube"}` {
		t.Fatalf("selected Cubecade page result = %+v, want one completed cube response", pageData)
	}
	if provider.connections() != 1 {
		t.Fatalf("provider connections = %d, want one session connection", provider.connections())
	}

	operations := runtime.Operations()
	var attaches, enables, invokes []testkit.Operation
	for _, operation := range operations {
		switch operation.Kind {
		case testkit.OperationAttach:
			attaches = append(attaches, operation)
		case testkit.OperationEnableWebMCP:
			enables = append(enables, operation)
		case testkit.OperationInvoke:
			invokes = append(invokes, operation)
		}
	}
	if len(attaches) != 1 || attaches[0].TargetID != cubeTarget.ID {
		t.Fatalf("attach operations = %#v, want exactly selected Cubecade target", attaches)
	}
	if len(enables) != 1 || enables[0].TargetID != cubeTarget.ID {
		t.Fatalf("WebMCP enable operations = %#v, want exactly selected Cubecade target", enables)
	}
	if len(invokes) != 1 || invokes[0].TargetID != cubeTarget.ID || invokes[0].ToolName != cubeTool.Name {
		t.Fatalf("invoke operations = %#v, want exactly one selected Cubecade call", invokes)
	}
	for _, operation := range append(append(attaches, enables...), invokes...) {
		if operation.TargetID == marginTarget.ID {
			t.Fatalf("unchosen Margin target received browser operation: %#v", operation)
		}
	}
}

func assertAmbiguousPageSurface(t *testing.T, definitions, base []messages.ToolDefinition, pageNames []string, label string) {
	t.Helper()
	want := make(map[string]struct{}, len(base)+len(pageNames))
	for _, definition := range base {
		want[definition.Name] = struct{}{}
	}
	for _, name := range pageNames {
		want[name] = struct{}{}
	}
	got := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		got[definition.Name] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("%s names = %v, want %v", label, sortedAmbiguousDefinitionNames(got), sortedAmbiguousDefinitionNames(want))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("%s missing %q: got %v want %v", label, name, sortedAmbiguousDefinitionNames(got), sortedAmbiguousDefinitionNames(want))
		}
	}
}

func sortedAmbiguousDefinitionNames(names map[string]struct{}) []string {
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func executeAmbiguousPageToolsCall(t *testing.T, ctx context.Context, executor messages.ToolExecutor, name, arguments string) webmcp.ToolResultEnvelope {
	t.Helper()
	response, err := executor.Execute(ctx, messages.ToolCall{ID: "ambiguous-" + name, Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode %s result: %v; content=%s", name, err, response.Content)
	}
	return envelope
}

func assertRuntimeHasNoOperation(t *testing.T, runtime *testkit.ScriptedBrowserRuntime, kinds ...testkit.OperationKind) {
	t.Helper()
	for _, operation := range runtime.Operations() {
		for _, kind := range kinds {
			if operation.Kind == kind {
				t.Fatalf("runtime operation before exact selection = %#v, want no %s", operation, kind)
			}
		}
	}
}

func readAmbiguousPageToolsSessionUpdate(t *testing.T, ctx context.Context, runErr <-chan error, session *ambiguousPageToolsSession) []messages.ToolDefinition {
	t.Helper()
	for {
		select {
		case message := <-session.sent:
			if message.Type != messages.StreamTypeSessionUpdate {
				continue
			}
			value, ok := message.Value.(*messages.SessionUpdateValue)
			if !ok || value == nil {
				t.Fatalf("provider SESSION.UPDATE value = %T", message.Value)
			}
			return append([]messages.ToolDefinition(nil), value.Tools...)
		case err := <-runErr:
			if err == nil {
				t.Fatal("session loop ended before receiving SESSION.UPDATE")
			}
			t.Fatalf("session loop ended before receiving SESSION.UPDATE: %v", err)
		case <-ctx.Done():
			t.Fatalf("waiting for provider SESSION.UPDATE: %v", ctx.Err())
		}
	}
}

type ambiguousSessionDiscovery struct {
	candidate discovery.BrowserCandidate
	targets   []discovery.Target
	mu        sync.Mutex
}

func (d *ambiguousSessionDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	return []discovery.BrowserCandidate{d.candidate}, nil
}

func (d *ambiguousSessionDiscovery) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	targets := append([]discovery.Target(nil), d.targets...)
	return discovery.TargetSnapshot{
		Browsers:       []discovery.BrowserCandidate{d.candidate},
		Targets:        targets,
		CandidateCount: len(targets),
		EligibleCount:  len(targets),
	}, nil
}

func (d *ambiguousSessionDiscovery) Select(_ context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, target := range d.targets {
		if target.BrowserID == request.BrowserID && target.ID == request.TargetID {
			return discovery.Selection{
				BrowserID:  request.BrowserID,
				TargetID:   request.TargetID,
				Generation: target.Generation,
				Target:     target,
			}, nil
		}
	}
	return discovery.Selection{}, &discovery.DiscoveryError{
		Code:    discovery.CodeStaleSelection,
		Message: "the exact target is no longer available",
	}
}

func (d *ambiguousSessionDiscovery) Selected() (discovery.Selection, bool) {
	return discovery.Selection{}, false
}

func (d *ambiguousSessionDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

func (d *ambiguousSessionDiscovery) Reconnect(_ context.Context, _ discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	if len(options) > 0 && options[0].AutoSelect == discovery.AutoSelectSingle && options[0].TargetID == "" {
		d.mu.Lock()
		candidateTargetIDs := make([]string, 0, len(d.targets))
		for _, target := range d.targets {
			candidateTargetIDs = append(candidateTargetIDs, target.ID)
		}
		d.mu.Unlock()
		return discovery.Selection{}, &discovery.DiscoveryError{
			Code:      discovery.CodeAmbiguousTab,
			Message:   "multiple browser tabs matched; an exact target ID is required",
			Retryable: true,
			Details: map[string]any{
				"browser_id":           d.candidate.ID,
				"candidate_target_ids": candidateTargetIDs,
			},
		}
	}
	return discovery.Selection{}, &discovery.DiscoveryError{
		Code:    discovery.CodeStaleSelection,
		Message: "unexpected reconnect request",
	}
}

func ambiguousSessionLaneTarget(target webmcp.Target, toolCount int) discovery.Target {
	return discovery.Target{
		BrowserID:             string(target.BrowserID),
		ID:                    string(target.ID),
		Type:                  target.Type,
		Title:                 target.Title,
		URL:                   target.URL,
		Origin:                target.Origin,
		Generation:            target.Generation,
		WebSocketPresent:      true,
		WebMCP:                true,
		WebMCPKnown:           true,
		WebMCPDomainSupported: true,
		WebMCPDomainKnown:     true,
		PageToolsReady:        true,
		PageToolsKnown:        true,
		ToolCount:             toolCount,
		ToolCountKnown:        true,
		Eligible:              true,
	}
}

type ambiguousPageToolsInferencer struct {
	mu          sync.Mutex
	session     *ambiguousPageToolsSession
	connectionN int
}

func (i *ambiguousPageToolsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.mu.Lock()
	i.connectionN++
	session := i.session
	i.mu.Unlock()
	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("ambiguous-session", "fake"),
	}) {
		return nil, ctx.Err()
	}
	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("ambiguous-session", "ambiguous-session"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

func (i *ambiguousPageToolsInferencer) connections() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectionN
}

type ambiguousPageToolsSession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	sent      chan messages.StreamMessage
	done      chan struct{}
	closeOnce sync.Once
}

func newAmbiguousPageToolsSession() *ambiguousPageToolsSession {
	return &ambiguousPageToolsSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](16),
		sent: make(chan messages.StreamMessage, 32),
		done: make(chan struct{}),
	}
}

func (s *ambiguousPageToolsSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.sent <- message:
		return true
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *ambiguousPageToolsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *ambiguousPageToolsSession) Done() <-chan struct{} { return s.done }

func (s *ambiguousPageToolsSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

var (
	_ WebMCPDiscoveryService      = (*ambiguousSessionDiscovery)(nil)
	_ sessionSelectionReconnector = (*ambiguousSessionDiscovery)(nil)
	_ messages.SessionInferencer  = (*ambiguousPageToolsInferencer)(nil)
)
