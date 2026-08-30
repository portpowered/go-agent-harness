package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionBrowserBrokerSelectsExactTargetAfterAutomaticAmbiguity(t *testing.T) {
	candidate := webmcp.BrowserCandidate{
		ID:       "browser-exact-selection",
		Product:  "Chrome/Test",
		Protocol: "1.3",
		Loopback: true,
	}
	publicTargetID := func(rawID string) webmcp.TargetID {
		return webmcp.TargetID(discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{
			BrowserID: string(candidate.ID),
			RawID:     rawID,
		}))
	}

	targets := []struct {
		rawID string
		title string
		url   string
	}{
		{rawID: "raw-tab-a", title: "Alpha page", url: "https://alpha.fixture.test/"},
		{rawID: "raw-tab-b", title: "Beta page", url: "https://beta.fixture.test/"},
	}
	pageTool := webmcp.ToolDescriptor{
		Name:        "read_state",
		Description: "Read fixture state.",
		FrameID:     "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
	runtimeTargets := make([]testkit.TargetConfig, 0, len(targets))
	laneTargets := make([]discovery.Target, 0, len(targets))
	publicIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		publicID := publicTargetID(target.rawID)
		publicIDs = append(publicIDs, string(publicID))
		runtimeTargets = append(runtimeTargets, testkit.NewTargetConfig(webmcp.Target{
			BrowserID: candidate.ID,
			ID:        webmcp.TargetID(target.rawID),
			Type:      "page",
			Title:     target.title,
			URL:       target.url,
			Origin:    target.url[:len(target.url)-1],
			Eligible:  true,
		}, testkit.WithInitialCatalog(pageTool)))
		laneTargets = append(laneTargets, discovery.Target{
			BrowserID:             string(candidate.ID),
			ID:                    string(publicID),
			Type:                  "page",
			Title:                 target.title,
			URL:                   target.url,
			Origin:                target.url[:len(target.url)-1],
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
		})
	}

	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate, runtimeTargets...))
	laneCandidate := discovery.BrowserCandidate{
		ID:       string(candidate.ID),
		Source:   discovery.SourceConfigured,
		Product:  candidate.Product,
		Protocol: candidate.Protocol,
		Loopback: true,
	}
	discoveryService := &ambiguousAutomaticSelectionDiscovery{
		candidate: laneCandidate,
		targets:   laneTargets,
	}
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Tools.Backend = "webmcp"
	browser.Connection.CDPURL = "http://127.0.0.1:9222"
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionDiscovery(discoveryService),
	)
	broker, err := newSessionBrowserBrokerWithDoctorFactory(browser, factory)
	if err != nil {
		t.Fatalf("construct session broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("close session broker: %v", err)
		}
		if err := runtime.Close(); err != nil {
			t.Errorf("close fixture runtime: %v", err)
		}
	})

	arguments, err := json.Marshal(map[string]string{
		"browser_id": string(candidate.ID),
		"target_id":  publicIDs[1],
	})
	if err != nil {
		t.Fatalf("marshal exact selector: %v", err)
	}
	response, err := webmcpTools.NewBrokerToolSet(broker).Executor().Execute(context.Background(), messages.ToolCall{
		ID:        "exact-selection",
		Name:      webmcp.SelectTabToolName,
		Arguments: string(arguments),
	})
	if err != nil {
		t.Fatalf("exact select tool: %v", err)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode exact selection result: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("exact selection failed after automatic ambiguity: %+v", envelope.Error)
	}
	var selected struct {
		BrowserID webmcp.BrowserID `json:"browser_id"`
		TargetID  webmcp.TargetID  `json:"target_id"`
		Connected bool             `json:"connected"`
		Ready     bool             `json:"ready"`
	}
	if err := json.Unmarshal(envelope.Data, &selected); err != nil {
		t.Fatalf("decode selected data: %v", err)
	}
	if selected.BrowserID != candidate.ID || selected.TargetID != webmcp.TargetID(publicIDs[1]) || !selected.Connected || !selected.Ready {
		t.Fatalf("selected context = %+v, want exact connected ready target %s", selected, publicIDs[1])
	}
	if discoveryService.reconnectCount() != 1 {
		t.Fatalf("automatic reconnect calls = %d, want one", discoveryService.reconnectCount())
	}

	operations := runtime.Operations()
	counts := make(map[testkit.OperationKind]int)
	for _, operation := range operations {
		counts[operation.Kind]++
		if operation.Kind == testkit.OperationAttach && operation.TargetID != webmcp.TargetID("raw-tab-b") {
			t.Fatalf("attached target = %q, want exact raw target raw-tab-b", operation.TargetID)
		}
	}
	if counts[testkit.OperationOpen] != 1 || counts[testkit.OperationListTargets] != 1 || counts[testkit.OperationAttach] != 1 {
		t.Fatalf("exact selection operations = %#v, want one open/list/attach", operations)
	}
	if counts[testkit.OperationActivate] != 0 {
		t.Fatalf("exact selection unexpectedly activated a target: %#v", operations)
	}
}

type ambiguousAutomaticSelectionDiscovery struct {
	candidate discovery.BrowserCandidate
	targets   []discovery.Target
	mu        sync.Mutex
	reconnect int
}

func (d *ambiguousAutomaticSelectionDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	return []discovery.BrowserCandidate{d.candidate}, nil
}

func (d *ambiguousAutomaticSelectionDiscovery) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	targets := append([]discovery.Target(nil), d.targets...)
	return discovery.TargetSnapshot{
		Browsers:       []discovery.BrowserCandidate{d.candidate},
		Targets:        targets,
		CandidateCount: len(targets),
		EligibleCount:  len(targets),
	}, nil
}

func (d *ambiguousAutomaticSelectionDiscovery) Select(_ context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	for _, target := range d.targets {
		if target.ID == request.TargetID {
			return discovery.Selection{
				BrowserID:  request.BrowserID,
				TargetID:   request.TargetID,
				Origin:     target.Origin,
				Generation: target.Generation,
				Target:     target,
			}, nil
		}
	}
	return discovery.Selection{}, fmt.Errorf("target %q not found", request.TargetID)
}

func (d *ambiguousAutomaticSelectionDiscovery) Selected() (discovery.Selection, bool) {
	return discovery.Selection{}, false
}

func (d *ambiguousAutomaticSelectionDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	return discovery.Selection{}, nil
}

func (d *ambiguousAutomaticSelectionDiscovery) Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error) {
	d.mu.Lock()
	d.reconnect++
	d.mu.Unlock()
	ids := make([]string, 0, len(d.targets))
	for _, target := range d.targets {
		ids = append(ids, target.ID)
	}
	return discovery.Selection{}, &discovery.DiscoveryError{
		Code:      discovery.CodeAmbiguousTab,
		Message:   "multiple browser tabs matched; an exact target ID is required",
		Retryable: true,
		Details: map[string]any{
			"browser_id":           d.candidate.ID,
			"candidate_target_ids": ids,
		},
	}
}

func (d *ambiguousAutomaticSelectionDiscovery) reconnectCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reconnect
}

var _ WebMCPDiscoveryService = (*ambiguousAutomaticSelectionDiscovery)(nil)
var _ sessionSelectionReconnector = (*ambiguousAutomaticSelectionDiscovery)(nil)
