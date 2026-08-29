package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
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
	for _, definition := range capabilities.Definitions {
		if isStableBrokerName(definition.Name) {
			t.Fatalf("disabled definitions include broker tool %q", definition.Name)
		}
	}
	if len(capabilities.Definitions) == 0 {
		t.Fatal("disabled composition dropped the static definitions")
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
	if len(capabilities.Definitions) != 7 {
		t.Fatalf("definitions = %d, want one static plus six broker tools", len(capabilities.Definitions))
	}
	if capabilities.Definitions[0].Name != "sleep" {
		t.Fatalf("static definition = %q, want filtered sleep", capabilities.Definitions[0].Name)
	}
	for _, definition := range capabilities.Definitions[1:] {
		if !isStableBrokerName(definition.Name) {
			t.Fatalf("composed definition %q is not a broker tool", definition.Name)
		}
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

func browserCapabilityConfig(enabled bool) *config.Config {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = enabled
	cfg := &config.Config{Browser: browser}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "sleep"})
	}
	return cfg
}

func isStableBrokerName(name string) bool {
	for _, candidate := range webmcp.StableToolNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

type capabilityBroker struct {
	selected   webmcp.PageContext
	closeErr   error
	closeCalls int
}

func (b *capabilityBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return nil, nil
}

func (b *capabilityBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return nil, nil
}

func (b *capabilityBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *capabilityBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *capabilityBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return webmcp.ToolCatalogSnapshot{Context: b.selected, Generation: b.selected.Generation}, nil
}

func (b *capabilityBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{}, nil
}

func (b *capabilityBroker) Cancel(context.Context, webmcp.CancelRequest) error { return nil }

func (b *capabilityBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	return make(chan webmcp.BrokerEvent)
}

func (b *capabilityBroker) Close() error {
	b.closeCalls++
	return b.closeErr
}

var _ webmcp.Broker = (*capabilityBroker)(nil)
