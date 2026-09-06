package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestSessionAdvertisesConnectedPageToolsOnTheProviderWire is the customer-
// visible contract for a voice session with one connected WebMCP page: the
// page's own tools must appear by name in the `session.update` frame the
// OpenAI Realtime adapter writes to the websocket. Everything below the CLI
// composition root is production code; only the browser transport and the
// provider websocket are hermetic fakes. Asserting on the encoded wire frame
// (rather than on internal registration bookkeeping) is what makes this test
// track what the model can actually call.
func TestSessionAdvertisesConnectedPageToolsOnTheProviderWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	candidate := webmcp.BrowserCandidate{
		ID:       "browser-cube",
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
	getCubeState := webmcp.ToolDescriptor{
		Name:        "get_cube_state",
		Description: "Read the current cube state.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	queueCubeMoves := webmcp.ToolDescriptor{
		Name:        "queue_cube_moves",
		Description: "Queue rotation moves on the cube.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`),
	}

	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(cubeTarget,
			testkit.WithInitialCatalog(getCubeState, queueCubeMoves),
			testkit.WithAutoResponse(json.RawMessage(`{"ok":true}`)),
		),
	))
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			t.Errorf("close scripted browser runtime: %v", closeErr)
		}
	}()
	discoveryService := &singlePageWireDiscovery{
		candidate: discovery.BrowserCandidate{
			ID:       string(candidate.ID),
			Source:   discovery.SourceExplicitCDPHTTP,
			Product:  candidate.Product,
			Protocol: candidate.Protocol,
			Loopback: true,
		},
		target: ambiguousSessionLaneTarget(cubeTarget, 2),
	}

	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = candidate.HTTPURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	cfg := browserCapabilityConfig(t, true)
	cfg.Browser = browser
	cfg.Model = config.ModelConfig{
		Provider: config.ProviderOpenAI,
		OpenAI:   &config.OpenAIConfig{Model: "gpt-realtime", APIKey: "unused"},
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
	}()

	surface := resolveSessionToolSurface(ctx, capabilities)
	if surface.browserState != webmcp.BrowserCapabilitySelected {
		t.Fatalf("browser capability state = %q, want selected for a single eligible page", surface.browserState)
	}

	wire := newSessionUpdateWire()
	sessionCtx, cancelSession := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() {
		runErr <- servicetest.RunSessionWithInstructions(sessionCtx, io.Discard, servicetest.SessionRunOptions{
			Provider:               config.ProviderOpenAI,
			Model:                  "gpt-realtime",
			ModelCatalog:           providerswire.NewModelCatalog(),
			APIKey:                 "unused",
			LoadedConfig:           cfg,
			BrowserToolsEnabled:    true,
			WaitForClose:           true,
			WebSocketDialer:        sessionUpdateDialer{wire: wire},
			ToolExecutor:           surface.executor,
			ToolDefinitions:        surface.definitions,
			ToolDefinitionBase:     surface.base,
			RefreshToolDefinitions: surface.refresh,
			BrowserWatch:           surface.browserWatch,
		}, "You help the customer with the cube on the connected page.")
	}()
	defer func() {
		cancelSession()
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("session shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("session did not stop after cancellation")
		}
	}()

	advertised := waitForWireSessionUpdateTools(t, ctx, runErr, wire)
	for _, want := range []string{getCubeState.Name, queueCubeMoves.Name} {
		if !advertised[want] {
			t.Fatalf("session.update advertised tools = %v, want the connected page tool %q on the provider wire", sortedWireToolNames(advertised), want)
		}
	}
}

// waitForWireSessionUpdateTools returns the tool names carried by the first
// session.update frame written to the provider websocket.
func waitForWireSessionUpdateTools(t *testing.T, ctx context.Context, runErr <-chan error, wire *sessionUpdateWire) map[string]bool {
	t.Helper()
	select {
	case payload := <-wire.updates:
		var event struct {
			Session struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatalf("decode session.update frame: %v (payload=%s)", err, payload)
		}
		names := make(map[string]bool, len(event.Session.Tools))
		for _, tool := range event.Session.Tools {
			names[tool.Name] = true
		}
		return names
	case err := <-runErr:
		t.Fatalf("session ended before writing session.update: %v", err)
	case <-ctx.Done():
		t.Fatalf("waiting for session.update frame: %v", ctx.Err())
	}
	return nil
}

func sortedWireToolNames(names map[string]bool) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	return out
}

// singlePageWireDiscovery is a discovery service exposing exactly one eligible
// WebMCP page, which the session bootstrap auto-selects.
type singlePageWireDiscovery struct {
	candidate discovery.BrowserCandidate
	target    discovery.Target
}

func (d *singlePageWireDiscovery) DiscoverAll(context.Context, discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	return []discovery.BrowserCandidate{d.candidate}, nil
}

func (d *singlePageWireDiscovery) ListTargetSnapshot(context.Context, discovery.BrowserCandidate, ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	return discovery.TargetSnapshot{
		Browsers:       []discovery.BrowserCandidate{d.candidate},
		Targets:        []discovery.Target{d.target},
		CandidateCount: 1,
		EligibleCount:  1,
	}, nil
}

func (d *singlePageWireDiscovery) Select(_ context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	if request.BrowserID == d.target.BrowserID && request.TargetID == d.target.ID {
		return d.selection(), nil
	}
	return discovery.Selection{}, &discovery.DiscoveryError{
		Code:    discovery.CodeStaleSelection,
		Message: "the exact target is no longer available",
	}
}

func (d *singlePageWireDiscovery) Selected() (discovery.Selection, bool) {
	return discovery.Selection{}, false
}

func (d *singlePageWireDiscovery) RefreshSelection(context.Context) (discovery.Selection, error) {
	return d.selection(), nil
}

func (d *singlePageWireDiscovery) Reconnect(_ context.Context, _ discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	if len(options) > 0 && options[0].TargetID != "" && options[0].TargetID != d.target.ID {
		return discovery.Selection{}, &discovery.DiscoveryError{
			Code:    discovery.CodeStaleSelection,
			Message: "the exact target is no longer available",
		}
	}
	return d.selection(), nil
}

func (d *singlePageWireDiscovery) selection() discovery.Selection {
	return discovery.Selection{
		BrowserID:  d.target.BrowserID,
		TargetID:   d.target.ID,
		Generation: d.target.Generation,
		Target:     d.target,
	}
}

// sessionUpdateWire is a minimal OpenAI Realtime websocket peer. It records the
// encoded session.update frames the provider writes and keeps the connection
// open so the session stays alive until the test cancels it.
type sessionUpdateWire struct {
	updates chan json.RawMessage
	inbound chan []byte
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
}

func newSessionUpdateWire() *sessionUpdateWire {
	return &sessionUpdateWire{
		updates: make(chan json.RawMessage, 8),
		inbound: make(chan []byte, 16),
		done:    make(chan struct{}),
	}
}

func (w *sessionUpdateWire) WriteMessage(_ int, payload []byte) error {
	select {
	case <-w.done:
		return io.ErrClosedPipe
	default:
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if envelope.Type != "session.update" {
		return nil
	}
	frame := append(json.RawMessage(nil), payload...)
	select {
	case w.updates <- frame:
	default:
	}
	created, err := json.Marshal(map[string]any{
		"type":    "session.created",
		"session": map[string]any{"id": "sess_wire_fixture", "model": "gpt-realtime"},
	})
	if err != nil {
		return err
	}
	select {
	case w.inbound <- created:
	case <-w.done:
	}
	return nil
}

func (w *sessionUpdateWire) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-w.inbound:
		return 1, payload, nil
	case <-w.done:
		return 0, nil, io.EOF
	}
}

func (w *sessionUpdateWire) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.done)
	}
	return nil
}

type sessionUpdateDialer struct {
	wire *sessionUpdateWire
}

func (d sessionUpdateDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	return d.wire, nil
}

var _ transport.Dialer = sessionUpdateDialer{}
var _ transport.Conn = (*sessionUpdateWire)(nil)

// TestSessionRepublishesLateConnectedPageToolsOnTheProviderWire covers the
// live shape where a page registers (or re-registers) a tool after the voice
// session has already opened. The dynamic publisher owns that case, and the
// only thing that matters to the customer is that the new page tool reaches
// the provider in a session.update frame.
func TestSessionRepublishesLateConnectedPageToolsOnTheProviderWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	candidate := webmcp.BrowserCandidate{
		ID:       "browser-cube-late",
		Source:   webmcp.DiscoverySourceExplicit,
		Product:  "scripted",
		Protocol: "1.3",
		HTTPURL:  "http://127.0.0.1:9223",
		Loopback: true,
		Explicit: true,
	}
	cubeTarget := webmcp.Target{
		BrowserID:             candidate.ID,
		ID:                    "tab-cube-late",
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
	getCubeState := webmcp.ToolDescriptor{
		Name:        "get_cube_state",
		Description: "Read the current cube state.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	queueCubeMoves := webmcp.ToolDescriptor{
		Name:        "queue_cube_moves",
		Description: "Queue rotation moves on the cube.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`),
	}

	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(cubeTarget,
			testkit.WithInitialCatalog(getCubeState),
			testkit.WithAutoResponse(json.RawMessage(`{"ok":true}`)),
		),
	))
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			t.Errorf("close scripted browser runtime: %v", closeErr)
		}
	}()

	discoveryService := &singlePageWireDiscovery{
		candidate: discovery.BrowserCandidate{
			ID:       string(candidate.ID),
			Source:   discovery.SourceExplicitCDPHTTP,
			Product:  candidate.Product,
			Protocol: candidate.Protocol,
			Loopback: true,
		},
		target: ambiguousSessionLaneTarget(cubeTarget, 1),
	}

	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = candidate.HTTPURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	cfg := browserCapabilityConfig(t, true)
	cfg.Browser = browser
	cfg.Model = config.ModelConfig{
		Provider: config.ProviderOpenAI,
		OpenAI:   &config.OpenAIConfig{Model: "gpt-realtime", APIKey: "unused"},
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
	}()

	surface := resolveSessionToolSurface(ctx, capabilities)

	wire := newSessionUpdateWire()
	sessionCtx, cancelSession := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() {
		runErr <- servicetest.RunSessionWithInstructions(sessionCtx, io.Discard, servicetest.SessionRunOptions{
			Provider:               config.ProviderOpenAI,
			Model:                  "gpt-realtime",
			ModelCatalog:           providerswire.NewModelCatalog(),
			APIKey:                 "unused",
			LoadedConfig:           cfg,
			BrowserToolsEnabled:    true,
			WaitForClose:           true,
			WebSocketDialer:        sessionUpdateDialer{wire: wire},
			ToolExecutor:           surface.executor,
			ToolDefinitions:        surface.definitions,
			ToolDefinitionBase:     surface.base,
			RefreshToolDefinitions: surface.refresh,
			BrowserWatch:           surface.browserWatch,
		}, "You help the customer with the cube on the connected page.")
	}()
	defer func() {
		cancelSession()
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("session shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("session did not stop after cancellation")
		}
	}()

	initial := waitForWireSessionUpdateTools(t, ctx, runErr, wire)
	if !initial[getCubeState.Name] {
		t.Fatalf("initial session.update tools = %v, want %q", sortedWireToolNames(initial), getCubeState.Name)
	}

	handleValue, err := runtime.Open(ctx, candidate)
	if err != nil {
		t.Fatalf("open scripted browser: %v", err)
	}
	targetSession := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession(cubeTarget.ID)
	if targetSession == nil {
		t.Fatal("scripted target session is nil")
	}
	if err := targetSession.EmitToolsAdded(queueCubeMoves); err != nil {
		t.Fatalf("emit late page tool: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no session.update frame ever advertised the late page tool %q", queueCubeMoves.Name)
		}
		advertised := waitForWireSessionUpdateTools(t, ctx, runErr, wire)
		if advertised[queueCubeMoves.Name] {
			if !advertised[getCubeState.Name] {
				t.Fatalf("republished session.update dropped %q: %v", getCubeState.Name, sortedWireToolNames(advertised))
			}
			return
		}
	}
}

// TestSessionAdvertisesPageToolsOnTheWireAfterMidSessionSelection is the real
// live shape: two eligible pages, so the session opens connected-but-unselected
// with no page tools, the model asks the customer, and then selects the exact
// page mid-session. From that moment the page's tools must reach the provider
// in a session.update frame - otherwise the model has nothing to call and
// truthfully but wrongly tells the customer it cannot see the cube.
func TestSessionAdvertisesPageToolsOnTheWireAfterMidSessionSelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	candidate := webmcp.BrowserCandidate{
		ID:       "browser-cube-switch",
		Source:   webmcp.DiscoverySourceExplicit,
		Product:  "scripted",
		Protocol: "1.3",
		HTTPURL:  "http://127.0.0.1:9224",
		Loopback: true,
		Explicit: true,
	}
	cubeTarget := webmcp.Target{
		BrowserID:             candidate.ID,
		ID:                    "tab-cube-switch",
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
		ID:                    "tab-margin-switch",
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
	getCubeState := webmcp.ToolDescriptor{
		Name:        "get_cube_state",
		Description: "Read the current cube state.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	queueCubeMoves := webmcp.ToolDescriptor{
		Name:        "queue_cube_moves",
		Description: "Queue rotation moves on the cube.",
		FrameID:     "cube-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"string"}}},"required":["moves"],"additionalProperties":false}`),
	}
	marginTool := webmcp.ToolDescriptor{
		Name:        "get_document",
		Description: "Read the Margin document.",
		FrameID:     "margin-frame",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}

	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(cubeTarget,
			testkit.WithInitialCatalog(getCubeState, queueCubeMoves),
			testkit.WithAutoResponse(json.RawMessage(`{"page":"cube"}`)),
		),
		testkit.NewTargetConfig(marginTarget,
			testkit.WithInitialCatalog(marginTool),
			testkit.WithAutoResponse(json.RawMessage(`{"page":"margin"}`)),
		),
	))
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			t.Errorf("close scripted browser runtime: %v", closeErr)
		}
	}()

	discoveryService := &ambiguousSessionDiscovery{
		candidate: discovery.BrowserCandidate{
			ID:       string(candidate.ID),
			Source:   discovery.SourceExplicitCDPHTTP,
			Product:  candidate.Product,
			Protocol: candidate.Protocol,
			Loopback: true,
		},
		targets: []discovery.Target{
			ambiguousSessionLaneTarget(cubeTarget, 2),
			ambiguousSessionLaneTarget(marginTarget, 1),
		},
	}

	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = candidate.HTTPURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	cfg := browserCapabilityConfig(t, true)
	cfg.Browser = browser
	cfg.Model = config.ModelConfig{
		Provider: config.ProviderOpenAI,
		OpenAI:   &config.OpenAIConfig{Model: "gpt-realtime", APIKey: "unused"},
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
	}()

	surface := resolveSessionToolSurface(ctx, capabilities)
	if surface.browserState != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("browser capability state = %q, want connected_unselected for two eligible pages", surface.browserState)
	}

	wire := newSessionUpdateWire()
	sessionCtx, cancelSession := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() {
		runErr <- servicetest.RunSessionWithInstructions(sessionCtx, io.Discard, servicetest.SessionRunOptions{
			Provider:               config.ProviderOpenAI,
			Model:                  "gpt-realtime",
			ModelCatalog:           providerswire.NewModelCatalog(),
			APIKey:                 "unused",
			LoadedConfig:           cfg,
			BrowserToolsEnabled:    true,
			WaitForClose:           true,
			WebSocketDialer:        sessionUpdateDialer{wire: wire},
			ToolExecutor:           surface.executor,
			ToolDefinitions:        surface.definitions,
			ToolDefinitionBase:     surface.base,
			RefreshToolDefinitions: surface.refresh,
			BrowserWatch:           surface.browserWatch,
		}, "You help the customer with the cube on the connected page.")
	}()
	defer func() {
		cancelSession()
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("session shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("session did not stop after cancellation")
		}
	}()

	initial := waitForWireSessionUpdateTools(t, ctx, runErr, wire)
	if initial[getCubeState.Name] {
		t.Fatalf("unselected session.update already advertised page tools: %v", sortedWireToolNames(initial))
	}
	if !initial[webmcp.SelectTabToolName] {
		t.Fatalf("unselected session.update tools = %v, want the stable broker surface", sortedWireToolNames(initial))
	}

	selectEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, webmcp.SelectTabToolName,
		`{"browser_id":"`+string(candidate.ID)+`","target_id":"`+string(cubeTarget.ID)+`"}`)
	if !selectEnvelope.OK {
		t.Fatalf("exact mid-session Cubecade selection failed: %+v", selectEnvelope.Error)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no session.update frame advertised the selected page tools after an exact mid-session selection")
		}
		advertised := waitForWireSessionUpdateTools(t, ctx, runErr, wire)
		if advertised[getCubeState.Name] {
			if !advertised[queueCubeMoves.Name] {
				t.Fatalf("session.update advertised %v, want both selected Cubecade page tools", sortedWireToolNames(advertised))
			}
			if advertised[marginTool.Name] {
				t.Fatalf("session.update advertised the unselected page's tool: %v", sortedWireToolNames(advertised))
			}
			return
		}
	}
}
