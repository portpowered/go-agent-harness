package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionManagedDefaultUsesSingleTargetAndFirstClassPageTools(t *testing.T) {
	configDir := t.TempDir()
	control := &managedCompositionTestControl{}
	var starts atomic.Int32
	manager := newManagedCompositionTestManager(configDir, control, &starts)

	candidate := webmcp.BrowserCandidate{
		ID:       "managed-browser",
		Product:  "Chrome/managed",
		Protocol: "1.3",
		Loopback: true,
	}
	target := webmcp.Target{
		BrowserID:             candidate.ID,
		ID:                    "managed-tab",
		Type:                  "page",
		Title:                 "Managed fixture",
		URL:                   "https://fixture.test/managed",
		Origin:                "https://fixture.test",
		WebSocketURL:          "ws://127.0.0.1/devtools/page/managed-tab",
		Generation:            1,
		WebMCPDomainSupported: true,
		PageToolsReady:        true,
		PageToolsKnown:        true,
		Eligible:              true,
	}
	tool := webmcp.ToolDescriptor{
		Name:        "read_managed_state",
		Description: "Read state from the managed fixture.",
		FrameID:     "frame-managed",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool), testkit.WithAutoResponse(json.RawMessage(`{"managed":true}`))),
	))

	laneTarget := discovery.Target{
		BrowserID:             string(candidate.ID),
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
		ToolCount:             1,
		ToolCountKnown:        true,
		Eligible:              true,
	}
	selected := discovery.Selection{
		BrowserID:  string(candidate.ID),
		TargetID:   string(target.ID),
		Generation: target.Generation,
		Origin:     target.Origin,
		Target:     laneTarget,
	}
	discoveryService := &managedSessionDiscovery{
		sessionBrokerDiscovery: sessionBrokerDiscovery{
			candidate: discovery.BrowserCandidate{
				ID:       string(candidate.ID),
				Source:   discovery.SourceExplicitCDPHTTP,
				Product:  candidate.Product,
				Protocol: candidate.Protocol,
				Loopback: true,
			},
			target: laneTarget,
		},
		selected: selected,
	}

	browserConfig := config.DefaultBrowserConfig()
	browserConfig.Tools.Enabled = true
	browserConfig.Managed.Open = "about:blank"
	browserConfig.Managed.CloseOnExit = true
	browserConfig.Selection.Persist = false
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionConfigDir(configDir),
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionManagedBrowserManager(manager),
		WithWebMCPProductionHTTPClient(&http.Client{Transport: managedCompositionVersionTransport{}}),
		WithWebMCPProductionDiscovery(discoveryService),
	)
	broker, err := newSessionBrowserBrokerWithDoctorFactory(browserConfig, factory)
	if err != nil {
		t.Fatalf("construct managed session broker: %v", err)
	}
	defer func() {
		_ = broker.Close()
		_ = runtime.Close()
	}()

	toolSet := webmcpTools.NewBrokerToolSet(broker)
	contextResponse, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "managed-context",
		Name:      webmcp.GetContextToolName,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("get managed context: %v", err)
	}
	contextEnvelope, err := webmcp.UnmarshalToolResult([]byte(contextResponse.Content))
	if err != nil {
		t.Fatalf("decode managed context: %v", err)
	}
	if !contextEnvelope.OK {
		t.Fatalf("managed context failed: %+v", contextEnvelope.Error)
	}
	var contextData struct {
		BrowserID string `json:"browser_id"`
		TargetID  string `json:"target_id"`
		Ready     bool   `json:"ready"`
	}
	if err := json.Unmarshal(contextEnvelope.Data, &contextData); err != nil {
		t.Fatalf("decode managed context data: %v", err)
	}
	if contextData.BrowserID != string(candidate.ID) || contextData.TargetID != string(target.ID) || !contextData.Ready {
		t.Fatalf("managed context = %+v, want one ready target", contextData)
	}

	discoveryService.mu.Lock()
	reconnectOptions := append([]discovery.ReconnectOptions(nil), discoveryService.reconnectOptions...)
	discoveryInputs := append([]discovery.ConnectionInputs(nil), discoveryService.reconnectInputs...)
	discoveryService.mu.Unlock()
	if len(reconnectOptions) != 1 || reconnectOptions[0].AutoSelect != discovery.AutoSelectSingle {
		t.Fatalf("managed reconnect options = %+v, want one single-target reconnect", reconnectOptions)
	}
	if len(discoveryInputs) == 0 {
		t.Fatal("managed discovery received no connection inputs")
	}
	for _, inputs := range discoveryInputs {
		if inputs.CDPURL == "" || inputs.UserDataDir != "" || inputs.AllowProcessScan {
			t.Fatalf("managed discovery inputs = %+v, want manager endpoint only", discoveryInputs)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("managed browser starts = %d, want one", starts.Load())
	}

	pageDefinitions, err := toolSet.PageToolDefinitionsWithError(context.Background())
	if err != nil {
		t.Fatalf("publish managed page tools: %v", err)
	}
	if len(pageDefinitions) != 1 || pageDefinitions[0].Name != tool.Name {
		t.Fatalf("managed page definitions = %+v, want %q", pageDefinitions, tool.Name)
	}
	pageResponse, err := toolSet.Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "managed-page-call",
		Name:      tool.Name,
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("invoke managed first-class page tool: %v", err)
	}
	pageEnvelope, err := webmcp.UnmarshalToolResult([]byte(pageResponse.Content))
	if err != nil {
		t.Fatalf("decode managed page response: %v", err)
	}
	var pageData struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(pageEnvelope.Data, &pageData); err != nil {
		t.Fatalf("decode managed page response data: %v", err)
	}
	if !pageEnvelope.OK || pageData.Status != string(webmcp.InvocationCompleted) || string(pageData.Output) != `{"managed":true}` {
		t.Fatalf("managed page response = %+v data=%+v, want successful fixture output", pageEnvelope, pageData)
	}
}

type managedSessionDiscovery struct {
	sessionBrokerDiscovery
	selected         discovery.Selection
	mu               sync.Mutex
	reconnectOptions []discovery.ReconnectOptions
	reconnectInputs  []discovery.ConnectionInputs
}

func (d *managedSessionDiscovery) DiscoverAll(ctx context.Context, inputs discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	d.mu.Lock()
	d.reconnectInputs = append(d.reconnectInputs, inputs)
	d.mu.Unlock()
	return d.sessionBrokerDiscovery.DiscoverAll(ctx, inputs)
}

func (d *managedSessionDiscovery) Reconnect(_ context.Context, inputs discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	d.mu.Lock()
	d.reconnectInputs = append(d.reconnectInputs, inputs)
	d.reconnectOptions = append(d.reconnectOptions, options...)
	d.mu.Unlock()
	return d.selected, nil
}

func (d *managedSessionDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{}, false, nil
}

var (
	_ WebMCPDiscoveryService      = (*managedSessionDiscovery)(nil)
	_ sessionSelectionReconnector = (*managedSessionDiscovery)(nil)
)
