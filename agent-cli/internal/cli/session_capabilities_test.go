package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionBrowserBrokerForwardsTerminalResultsAndFixtureMutation(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-session", Product: "scripted", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-session",
		Type:      "page",
		Title:     "Session fixture",
		URL:       "https://fixture.test/",
		Origin:    "https://fixture.test",
	}
	pageTool := webmcp.ToolDescriptor{
		Name:        "write_fixture",
		Description: "Write a value to the session fixture.",
		FrameID:     "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"required":["value"],"additionalProperties":false}`),
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target,
			testkit.WithInitialCatalog(pageTool),
			testkit.WithAutoResponse(json.RawMessage(`{"mutated":true}`)),
		),
	))
	laneCandidate := discovery.BrowserCandidate{
		ID:       string(candidate.ID),
		Source:   discovery.SourceConfigured,
		Product:  candidate.Product,
		Protocol: "1.3",
		Loopback: true,
	}
	laneTarget := discovery.Target{
		BrowserID:             string(candidate.ID),
		ID:                    string(target.ID),
		Type:                  target.Type,
		Title:                 target.Title,
		URL:                   target.URL,
		Origin:                target.Origin,
		Generation:            1,
		WebSocketPresent:      true,
		WebMCP:                true,
		WebMCPKnown:           true,
		WebMCPDomainSupported: true,
		WebMCPDomainKnown:     true,
		PageToolsReady:        true,
		PageToolsKnown:        true,
		ToolCount:             1,
		ToolCountKnown:        true,
		Eligible:              true,
	}
	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Connection.CDPURL = "http://127.0.0.1:9222"
	productionFactory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionDiscovery(sessionBrokerDiscovery{candidate: laneCandidate, target: laneTarget}),
	)
	broker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, productionFactory)
	if err != nil {
		t.Fatalf("construct session broker: %v", err)
	}
	defer func() { _ = broker.Close() }()
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		t.Fatalf("select session fixture: %v", err)
	}

	refresher, ok := broker.(interface {
		SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
	})
	if !ok {
		t.Fatal("session broker dropped SelectedWithRefresh")
	}
	selected, err := refresher.SelectedWithRefresh(context.Background(), false)
	if err != nil || selected.Key.TargetID != target.ID {
		t.Fatalf("selected session context = %#v, err=%v", selected, err)
	}
	if _, ok := broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); !ok {
		t.Fatal("session broker dropped SelectWithOptions")
	}

	toolSet := webmcpTools.NewBrokerToolSet(broker)
	listResponse, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "list-call",
		Name:      webmcp.ListToolsToolName,
		Arguments: `{"include_schemas":true}`,
	})
	if err != nil {
		t.Fatalf("list session fixture tools: %v", err)
	}
	listEnvelope, err := webmcp.UnmarshalToolResult([]byte(listResponse.Content))
	if err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	var catalog struct {
		Tools []struct {
			Ref  webmcp.ToolRef `json:"ref"`
			Name string         `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listEnvelope.Data, &catalog); err != nil {
		t.Fatalf("decode list data: %v", err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != pageTool.Name || catalog.Tools[0].Ref == "" {
		t.Fatalf("session fixture catalog = %#v", catalog.Tools)
	}

	invokeResponse, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "invoke-call",
		Name:      webmcp.InvokeToolName,
		Arguments: `{"tool_ref":"` + string(catalog.Tools[0].Ref) + `","input_json":"{\"value\":7}","reason":"set the fixture value"}`,
	})
	if err != nil {
		t.Fatalf("invoke session fixture tool: %v", err)
	}
	if invokeResponse.ToolCallID != "invoke-call" || invokeResponse.Name != webmcp.InvokeToolName || len(invokeResponse.ContentParts) != 0 {
		t.Fatalf("session invocation response = %#v", invokeResponse)
	}
	invokeEnvelope, err := webmcp.UnmarshalToolResult([]byte(invokeResponse.Content))
	if err != nil {
		t.Fatalf("decode invoke result: %v", err)
	}
	var invokeData struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(invokeEnvelope.Data, &invokeData); err != nil {
		t.Fatalf("decode invoke data: %v", err)
	}
	if !invokeEnvelope.OK || invokeData.Status != string(webmcp.InvocationCompleted) || string(invokeData.Output) != `{"mutated":true}` {
		t.Fatalf("session invocation envelope = %#v data=%#v", invokeEnvelope, invokeData)
	}

	var mutations []testkit.Operation
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationInvoke {
			mutations = append(mutations, operation)
		}
	}
	if len(mutations) != 1 || mutations[0].ToolName != pageTool.Name || string(mutations[0].Input) != `{"value":7}` {
		t.Fatalf("session fixture mutations = %#v, want one terminal write_fixture mutation", mutations)
	}
}

func TestSessionBrowserBrokerRestoresPersistedSelectionBeforeFirstToolCall(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()
	runtime.mu.Lock()
	runtime.targets[0].Generation = 1
	runtime.mu.Unlock()

	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Connection.CDPURL = server.URL + "/json/version"
	browserConfig.Selection.Persist = true
	selectionStore := NewFileWebMCPSelectionStore(t.TempDir())
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
		WithWebMCPProductionSelectionStore(selectionStore),
	)

	// Seed the exact record through the same production composition used by a
	// prior CLI process, then construct a fresh session broker.
	seedBroker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct seed broker: %v", err)
	}
	if _, err := seedBroker.Select(context.Background(), webmcp.TargetSelector{BrowserID: webmcp.BrowserID(browserID), TargetID: webmcp.TargetID(targetID)}); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	if err := seedBroker.Close(); err != nil {
		t.Fatalf("close seed broker: %v", err)
	}
	if _, err := selectionStore.Load(); err != nil {
		t.Fatalf("seed selection was not persisted: %v", err)
	}
	runtime.resetOperations()

	sessionBroker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct session broker: %v", err)
	}
	defer func() { _ = sessionBroker.Close() }()

	initializer, ok := sessionBroker.(SessionCapabilityInitializer)
	if !ok {
		t.Fatal("session broker does not expose capability initialization")
	}
	if status := initializer.SessionCapabilityStatus(); status.State != SessionCapabilityInitializing || status.Err != nil {
		t.Fatalf("initial capability status = %+v, want initializing", status)
	}

	response, err := webmcpTools.NewBrokerToolSet(sessionBroker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "first-browser-call",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("first browser call: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode first browser result: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("first browser result failed: %+v", envelope.Error)
	}
	var contextData struct {
		BrowserID  string `json:"browser_id"`
		TargetID   string `json:"target_id"`
		Generation uint64 `json:"generation"`
		Connected  bool   `json:"connected"`
		Ready      bool   `json:"ready"`
	}
	if err := json.Unmarshal(envelope.Data, &contextData); err != nil {
		t.Fatalf("decode first browser context: %v", err)
	}
	if contextData.BrowserID != browserID || contextData.TargetID != targetID || contextData.Generation == 0 || !contextData.Connected || !contextData.Ready {
		t.Fatalf("first browser context = %+v, want exact ready persisted selection", contextData)
	}
	if status := initializer.SessionCapabilityStatus(); status.State != SessionCapabilityReady || status.Err != nil {
		t.Fatalf("final capability status = %+v, want ready", status)
	}
	toolsResponse, err := webmcpTools.NewBrokerToolSet(sessionBroker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "first-page-tools-call",
		Name:      webmcp.ListToolsToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("first page tools call: %v", err)
	}
	toolsEnvelope, err := webmcp.UnmarshalToolResult([]byte(toolsResponse.Content))
	if err != nil {
		t.Fatalf("decode first page tools result: %v", err)
	}
	if !toolsEnvelope.OK {
		t.Fatalf("first page tools result failed: %+v", toolsEnvelope.Error)
	}
	var catalog struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolsEnvelope.Data, &catalog); err != nil {
		t.Fatalf("decode first page tools data: %v", err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != "read_state" {
		t.Fatalf("first page tools = %#v, want persisted target catalog", catalog.Tools)
	}

	// The first provider-facing operation was get_context. Internal bootstrap
	// may probe the endpoint, but no model-driven list-tabs call is involved.
	if runtime.count("activate") != 0 {
		t.Fatalf("bootstrap unexpectedly activated the target: %v", runtime.operationSnapshot())
	}
}

// TestSessionBrowserBrokerKeepsBrowserUsableWhenPersistedTargetIsGone pins the
// customer contract for a remembered page that no longer exists. A stale
// persisted record is ordinary drift - the tab was closed, reloaded, or
// replaced - and the browser endpoint itself is still healthy. The session
// must therefore stay connected-but-unselected so the model can list tabs, ask
// the customer, and select an exact target, after which its page tools are
// published. Retaining the record as a permanently failed capability instead
// leaves a browser-enabled session with no page tools, no connected-unselected
// grounding, and no working browser control, which is what let the model tell
// the customer that browser access does not exist.
func TestSessionBrowserBrokerKeepsBrowserUsableWhenPersistedTargetIsGone(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()

	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Connection.CDPURL = server.URL + "/json/version"
	selectionStore := NewFileWebMCPSelectionStore(t.TempDir())
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
		WithWebMCPProductionSelectionStore(selectionStore),
	)

	seedBroker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct seed broker: %v", err)
	}
	if _, err := seedBroker.Select(context.Background(), webmcp.TargetSelector{BrowserID: webmcp.BrowserID(browserID), TargetID: webmcp.TargetID(targetID)}); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	if err := seedBroker.Close(); err != nil {
		t.Fatalf("close seed broker: %v", err)
	}
	runtime.mu.Lock()
	runtime.targets = nil
	runtime.mu.Unlock()
	runtime.resetOperations()

	broker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct stale session broker: %v", err)
	}
	defer func() { _ = broker.Close() }()

	executor := webmcpTools.NewBrokerToolSet(broker).Executor()
	response, err := executor.Execute(context.Background(), messages.ToolCall{
		ID:        "stale-first-browser-call",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("stale first browser call: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode stale browser result: %v", err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("stale browser envelope = %+v, want stale_selection", envelope)
	}

	// The browser control surface must remain usable: this is the exact path
	// the model takes to recover from a remembered page that is gone.
	listResponse, err := executor.Execute(context.Background(), messages.ToolCall{
		ID:        "stale-recovery-list-tabs",
		Name:      webmcp.ListTabsToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("list tabs after a stale persisted selection: %v", err)
	}
	listEnvelope, err := webmcp.UnmarshalToolResult([]byte(listResponse.Content))
	if err != nil {
		t.Fatalf("decode list tabs result: %v", err)
	}
	if !listEnvelope.OK {
		t.Fatalf("list tabs after a stale persisted selection = %+v, want a usable browser control", listEnvelope.Error)
	}

	initializer, ok := broker.(SessionCapabilityInitializer)
	if !ok {
		t.Fatal("stale session broker does not expose capability status")
	}
	status := initializer.SessionCapabilityStatus()
	if status.State != SessionCapabilityReady || status.Err != nil {
		t.Fatalf("stale capability status = %+v, want a ready capability", status)
	}
	if status.BrowserCapabilityState != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("stale browser capability state = %q, want connected_unselected", status.BrowserCapabilityState)
	}
	if runtime.count("attach") != 0 {
		t.Fatalf("stale bootstrap attached a replacement target: %v", runtime.operationSnapshot())
	}
}

func TestSessionBrowserBrokerSharesInitializationAcrossConcurrentFirstUse(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()

	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Connection.CDPURL = server.URL + "/json/version"
	selectionStore := NewFileWebMCPSelectionStore(t.TempDir())
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
		WithWebMCPProductionSelectionStore(selectionStore),
	)
	seedBroker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct seed broker: %v", err)
	}
	if _, err := seedBroker.Select(context.Background(), webmcp.TargetSelector{BrowserID: webmcp.BrowserID(browserID), TargetID: webmcp.TargetID(targetID)}); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	if err := seedBroker.Close(); err != nil {
		t.Fatalf("close seed broker: %v", err)
	}
	runtime.resetOperations()

	broker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct concurrent session broker: %v", err)
	}
	defer func() { _ = broker.Close() }()
	toolSet := webmcpTools.NewBrokerToolSet(broker)
	const callers = 6
	responses := make(chan messages.ToolCallResponse, callers)
	errorsCh := make(chan error, callers)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			response, callErr := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
				ID:        fmt.Sprintf("concurrent-%d", index),
				Name:      webmcp.GetContextToolName,
				Arguments: `{}`,
			})
			if callErr != nil {
				errorsCh <- callErr
				return
			}
			responses <- response
		}(index)
	}
	close(start)
	group.Wait()
	close(responses)
	close(errorsCh)
	for callErr := range errorsCh {
		t.Fatalf("concurrent first-use error: %v", callErr)
	}
	if len(responses) != callers {
		t.Fatalf("concurrent responses = %d, want %d", len(responses), callers)
	}
	for response := range responses {
		envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
		if err != nil || !envelope.OK {
			t.Fatalf("concurrent response = %s, decode error=%v", response.Content, err)
		}
	}
	initializer, ok := broker.(SessionCapabilityInitializer)
	if !ok {
		t.Fatal("concurrent session broker does not expose capability status")
	}
	if status := initializer.SessionCapabilityStatus(); status.State != SessionCapabilityReady || status.Err != nil {
		t.Fatalf("concurrent capability status = %+v, want ready", status)
	}
	// The production composition performs bounded capability probes while
	// reconnecting and while the broker adopts the exact target. Those probes
	// are all detached; concurrent first use must not multiply this fixed
	// restore/adoption sequence or leak a handle.
	if got := runtime.count("attach"); got != 4 || runtime.sessionCount("close") != got-1 {
		t.Fatalf("concurrent attach/close count = %d/%d, want one fixed restore sequence plus one live session: %v", got, runtime.sessionCount("close"), runtime.operationSnapshot())
	}
}

func TestSessionBrowserBrokerCloseCancelsInitializationAndClosesOnce(t *testing.T) {
	base := &capabilityBroker{}
	started := make(chan struct{})
	broker := &sessionBrowserBroker{
		Broker:    base,
		initDone:  make(chan struct{}),
		initState: SessionCapabilityInitializing,
		bootstrap: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- broker.InitializeSession(context.Background()) }()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- broker.Close() }()
	if err := <-firstDone; !errors.Is(err, context.Canceled) && !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("initialization cancellation error = %v, want cancellation", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close after initialization cancellation: %v", err)
	}
	if base.closeCalls != 1 {
		t.Fatalf("underlying broker close calls = %d, want one", base.closeCalls)
	}
	status := broker.SessionCapabilityStatus()
	if status.State != SessionCapabilityFailed || status.Err == nil {
		t.Fatalf("canceled capability status = %+v, want failed with cancellation", status)
	}
	if _, err := broker.Selected(context.Background()); !errors.Is(err, context.Canceled) && !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("post-cancel selection error = %v, want no dispatch after failed bootstrap", err)
	}
}

func TestSessionBrowserBrokerSuccessfulInitializationRetainsBrowserContext(t *testing.T) {
	base := &capabilityBroker{}
	contextCanceled := make(chan struct{})
	broker := &sessionBrowserBroker{
		Broker:    base,
		initDone:  make(chan struct{}),
		initState: SessionCapabilityInitializing,
		bootstrap: func(ctx context.Context) error {
			go func() {
				<-ctx.Done()
				close(contextCanceled)
			}()
			return nil
		},
	}

	if err := broker.InitializeSession(context.Background()); err != nil {
		t.Fatalf("successful initialization: %v", err)
	}
	select {
	case <-contextCanceled:
		t.Fatal("successful initialization canceled the context used by browser resources")
	default:
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("close after successful initialization: %v", err)
	}
	select {
	case <-contextCanceled:
	case <-time.After(time.Second):
		t.Fatal("broker close did not cancel the retained initialization context")
	}
}

type sessionBrokerDiscovery struct {
	candidate discovery.BrowserCandidate
	target    discovery.Target
}

func (d sessionBrokerDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	return []discovery.BrowserCandidate{d.candidate}, nil
}

func (d sessionBrokerDiscovery) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	return discovery.TargetSnapshot{
		Browsers:       []discovery.BrowserCandidate{d.candidate},
		Targets:        []discovery.Target{d.target},
		CandidateCount: 1,
		EligibleCount:  1,
	}, nil
}

func (d sessionBrokerDiscovery) Select(_ context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	return discovery.Selection{
		BrowserID:  request.BrowserID,
		TargetID:   request.TargetID,
		Generation: d.target.Generation,
		Target:     d.target,
	}, nil
}

func (d sessionBrokerDiscovery) Selected() (discovery.Selection, bool) {
	return discovery.Selection{BrowserID: d.candidate.ID, TargetID: d.target.ID, Generation: d.target.Generation, Target: d.target}, true
}

func (d sessionBrokerDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	selection, _ := d.Selected()
	return selection, nil
}

func TestSessionToolCapabilitiesFactoryKeepsDisabledBrowserCompositionInert(t *testing.T) {
	calls := 0
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		calls++
		return nil, errors.New("broker must not be constructed")
	})
	cfg := browserCapabilityConfig(false)

	capabilities, err := factory(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if calls != 0 {
		t.Fatalf("disabled browser constructed broker %d times", calls)
	}
	if capabilities.BrowserCapabilityState != webmcp.BrowserCapabilityDisabled {
		t.Fatalf("disabled browser capability state = %q, want disabled", capabilities.BrowserCapabilityState)
	}
	for _, definition := range capabilities.Definitions {
		if isBrokerToolName(definition.Name) {
			t.Fatalf("disabled definitions include broker tool %q", definition.Name)
		}
	}
	if len(capabilities.Definitions) == 0 {
		t.Fatal("disabled composition dropped the static definitions")
	}
}

func TestSessionToolCapabilitiesFactoryUsesDefaultFilesystemPolicyWithoutMetadata(t *testing.T) {
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, &sessionDisplaySurfaceFake{
		capability: cliTools.UnavailableDisplayCapability("headless test"),
	})

	capabilities, err := factory(&config.Config{Browser: config.DefaultBrowserConfig()})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.Executor == nil {
		t.Fatal("factory returned a nil executor")
	}

	outsideTarget := filepath.Join(t.TempDir(), "outside-session.txt")
	response, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{
		ID:        "scope-call",
		Name:      "write_file",
		Arguments: fmt.Sprintf(`{"path":%q,"content":"must-not-write"}`, outsideTarget),
	})
	if err != nil {
		t.Fatalf("outside write: %v", err)
	}
	if !strings.Contains(response.Content, "path escapes workspace") {
		t.Fatalf("outside write response = %q, want default-cwd confinement denial", response.Content)
	}
	if _, err := os.Stat(outsideTarget); !os.IsNotExist(err) {
		t.Fatalf("outside target = %v, want absent", err)
	}
}

func TestSessionToolCapabilitiesFactoryComposesFilteredStaticToolsWithRealBrokerToolSet(t *testing.T) {
	broker := &capabilityBroker{
		selected: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"},
			Generation: 1,
			Connected:  true,
			Ready:      true,
		},
	}
	var gotBrowser config.BrowserConfig
	factory := NewSessionToolCapabilitiesFactory(nil, func(browser config.BrowserConfig) (webmcp.Broker, error) {
		gotBrowser = browser
		return broker, nil
	})
	cfg := browserCapabilityConfig(true)

	capabilities, err := factory(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !gotBrowser.BrowserBackendEnabled() {
		t.Fatalf("factory received disabled browser config: %+v", gotBrowser)
	}
	if len(capabilities.Definitions) != 9 {
		t.Fatalf("definitions = %d, want one static plus six broker tools, open-tab, and show_page", len(capabilities.Definitions))
	}
	foundSleep := false
	for _, definition := range capabilities.Definitions {
		if definition.Name == "sleep" {
			foundSleep = true
			continue
		}
		if !isBrokerToolName(definition.Name) {
			t.Fatalf("composed definition %q is neither filtered static sleep nor a broker tool", definition.Name)
		}
	}
	if !foundSleep {
		t.Fatalf("composed definitions = %#v, want filtered sleep", capabilities.Definitions)
	}

	response, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{
		ID:        "context-call",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("broker execute: %v", err)
	}
	if response.ToolCallID != "context-call" || response.Name != webmcp.GetContextToolName || len(response.ContentParts) != 0 {
		t.Fatalf("broker response = %#v", response)
	}
	if _, err := webmcp.UnmarshalToolResult([]byte(response.Content)); err != nil {
		t.Fatalf("broker response is not one WebMCP result envelope: %v; content=%s", err, response.Content)
	}
}

func TestSessionToolCapabilitiesFactoryClosesBrokerWhenCompositionFails(t *testing.T) {
	closeErr := errors.New("broker close failed")
	broker := &capabilityBroker{closeErr: closeErr}
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return broker, errors.New("broker construction failed")
	})

	_, err := factory(browserCapabilityConfig(true))
	if err == nil || !strings.Contains(err.Error(), "broker construction failed") || !strings.Contains(err.Error(), "broker close failed") {
		t.Fatalf("factory error = %v, want construction and cleanup failures", err)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
	}
}

func TestSessionToolCapabilitiesFactoryTransfersIdempotentCloseHook(t *testing.T) {
	broker := &capabilityBroker{}
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return broker, nil
	})

	capabilities, err := factory(browserCapabilityConfig(true))
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.Close == nil {
		t.Fatal("enabled capabilities did not transfer a close hook")
	}
	if err := capabilities.Close(); err != nil {
		t.Fatalf("first capability close: %v", err)
	}
	if err := capabilities.Close(); err != nil {
		t.Fatalf("second capability close: %v", err)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one after repeated capability closes", broker.closeCalls)
	}
}

func TestSessionBrowserBrokerPreservesModelFacingOpenTab(t *testing.T) {
	delegate := &capabilityBroker{selected: webmcp.PageContext{
		Key:       webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-new"},
		URL:       "https://notes.example.test/",
		Connected: true,
		Ready:     true,
	}}
	broker := &sessionBrowserBroker{
		Broker:       delegate,
		bootstrap:    func(context.Context) error { return nil },
		initDone:     make(chan struct{}),
		initState:    SessionCapabilityInitializing,
		browserState: webmcp.BrowserCapabilityInitializing,
	}

	opened, err := broker.OpenTab(context.Background(), webmcp.OpenTabRequest{
		URL:      "https://notes.example.test/",
		Activate: true,
	})
	if err != nil {
		t.Fatalf("session broker open tab: %v", err)
	}
	if opened.Key.TargetID != "tab-new" || delegate.openCalls != 1 || delegate.openRequest.URL != "https://notes.example.test/" || !delegate.openRequest.Activate {
		t.Fatalf("opened page = %+v delegate calls=%d request=%+v", opened, delegate.openCalls, delegate.openRequest)
	}
}

func browserCapabilityConfig(enabled bool) *config.Config {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = enabled
	cfg := &config.Config{Browser: browser}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "sleep"})
	}
	return cfg
}

func isBrokerToolName(name string) bool {
	if name == webmcp.ShowPageToolName || name == webmcp.OpenTabToolName {
		return true
	}
	for _, candidate := range webmcp.StableToolNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

type capabilityBroker struct {
	selected    webmcp.PageContext
	discoverErr error
	selectErr   error
	selectCalls int
	selectOpts  webmcp.SelectOptions
	openRequest webmcp.OpenTabRequest
	openErr     error
	openCalls   int
	catalog     []webmcp.ToolDescriptor
	closeErr    error
	closeCalls  int
}

func (b *capabilityBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return nil, b.discoverErr
}

func (b *capabilityBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return nil, nil
}

func (b *capabilityBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	b.selectCalls++
	return b.selected, b.selectErr
}

func (b *capabilityBroker) SelectWithOptions(_ context.Context, _ webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	b.selectCalls++
	b.selectOpts = options
	return b.selected, b.selectErr
}

func (b *capabilityBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *capabilityBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return webmcp.ToolCatalogSnapshot{Context: b.selected, Generation: b.selected.Generation, Tools: append([]webmcp.ToolDescriptor(nil), b.catalog...)}, nil
}

func (b *capabilityBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{}, nil
}

func (b *capabilityBroker) Cancel(context.Context, webmcp.CancelRequest) error { return nil }

func (b *capabilityBroker) OpenTab(_ context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	b.openCalls++
	b.openRequest = request
	return b.selected, b.openErr
}

func (b *capabilityBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	return make(chan webmcp.BrokerEvent)
}

func (b *capabilityBroker) Close() error {
	b.closeCalls++
	return b.closeErr
}

var _ webmcp.Broker = (*capabilityBroker)(nil)

// TestSessionToolCapabilitiesRefreshAdvertisesFirstClassPageTools locks the
// first-class page-tool surface: after the capability bootstrap, the
// refreshed definition list contains every connected catalog tool under its
// own name with the page's schema, alongside the static and stable broker
// definitions - and a bare catalog-name call executes through the composed
// surface instead of dead-ending.
func TestSessionToolCapabilitiesRefreshAdvertisesFirstClassPageTools(t *testing.T) {
	broker := &capabilityBroker{
		selected: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"},
			Generation: 1,
			Connected:  true,
			Ready:      true,
		},
		catalog: []webmcp.ToolDescriptor{{
			Ref:         webmcp.ToolRef("webmcp.tool-ref.v1:cube-state"),
			Name:        "get_cube_state",
			Description: "Read the cube.",
			InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
	}
	factory := NewSessionToolCapabilitiesFactory(nil, func(config.BrowserConfig) (webmcp.Broker, error) {
		return broker, nil
	})
	capabilities, err := factory(browserCapabilityConfig(true))
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.RefreshDefinitions == nil {
		t.Fatalf("browser-enabled capabilities lack RefreshDefinitions")
	}
	refreshed := capabilities.RefreshDefinitions(context.Background())
	if len(refreshed) != len(capabilities.Definitions)+1 {
		t.Fatalf("refreshed = %d definitions, want composed %d plus one page tool", len(refreshed), len(capabilities.Definitions))
	}
	last := refreshed[len(refreshed)-1]
	if last.Name != "get_cube_state" || last.Description != "Read the cube." {
		t.Fatalf("page definition = %+v, want first-class get_cube_state", last)
	}

	response, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{
		ID:        "cube-call",
		Name:      "get_cube_state",
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("bare catalog-name call must not dead-end: %v", err)
	}
	if _, err := webmcp.UnmarshalToolResult([]byte(response.Content)); err != nil {
		t.Fatalf("page-tool response is not one WebMCP result envelope: %v; content=%s", err, response.Content)
	}
}
