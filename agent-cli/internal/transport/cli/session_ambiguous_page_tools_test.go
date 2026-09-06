package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

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
		runErr <- servicetest.RunSession(sessionCtx, io.Discard, servicetest.SessionRunOptions{
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

func TestSessionAmbiguousCubeConversationRequiresChoiceBeforePageWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
	cubeMoveTool := webmcp.ToolDescriptor{
		Name:        "queue_cube_moves",
		Description: "Queue moves on the Cubecade.",
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
		testkit.NewTargetConfig(cubeTarget, testkit.WithInitialCatalog(cubeTool, cubeMoveTool), testkit.WithAutoResponse(json.RawMessage(`{"page":"cube"}`))),
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
			ambiguousSessionLaneTarget(cubeTarget, 2),
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

	providerSession := newAmbiguousCubeConversationSession()
	provider := &ambiguousCubeConversationInferencer{session: providerSession}
	answer := make(chan servicetest.ScheduledAudioInput, 1)
	var output strings.Builder
	sessionCtx, cancelSession := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	runComplete := make(chan struct{})
	go func() {
		err := servicetest.RunSessionWithInstructions(sessionCtx, &output, servicetest.SessionRunOptions{
			Provider:               config.ProviderGrok,
			Model:                  "ambiguous-session",
			APIKey:                 "unused",
			ConfigDir:              t.TempDir(),
			Prompt:                 "Inspect the cube on the connected browser.",
			PromptProvided:         true,
			LoadedConfig:           cfg,
			BrowserToolsEnabled:    true,
			WaitForClose:           true,
			ToolExecutor:           surface.executor,
			ToolDefinitions:        surface.definitions,
			ToolDefinitionBase:     surface.base,
			RefreshToolDefinitions: surface.refresh,
			BrowserWatch:           surface.browserWatch,
			BrowserCapabilityState: surface.browserState,
			AudioInterruptions:     answer,
			SessionInferencer:      provider,
		}, "You are a careful cube assistant.")
		runErr <- err
		close(runComplete)
	}()
	defer func() {
		cancelSession()
		select {
		case err := <-runErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("session loop shutdown: %v", err)
			}
		case <-runComplete:
			// The final assertion consumed the result.
		case <-time.After(time.Second):
			t.Error("session loop did not stop after cancellation")
		}
	}()

	initialUpdate := readAmbiguousCubeConversationUpdate(t, ctx, runComplete, providerSession, func(update *messages.SessionUpdateValue) bool {
		return update.Instructions != ""
	})
	assertAmbiguousPageSurface(t, initialUpdate.Tools, surface.base, nil, "initial provider surface")
	for _, required := range []string{
		"browser endpoint is connected",
		"no page is selected",
		webmcp.ListTabsToolName,
		"ask the customer which page to use",
		"exact browser_id and target_id",
		"do not invoke page tools",
	} {
		if !strings.Contains(initialUpdate.Instructions, required) {
			t.Fatalf("initial provider instructions missing %q: %s", required, initialUpdate.Instructions)
		}
	}
	for _, name := range []string{cubeTool.Name, cubeMoveTool.Name, marginTool.Name} {
		if containsAmbiguousDefinition(initialUpdate.Tools, name) {
			t.Fatalf("initial provider surface advertised an unselected page tool %q: %#v", name, initialUpdate.Tools)
		}
	}

	waitAmbiguousConversationSignal(t, ctx, runComplete, providerSession.questionSent, "provider choice question")
	assistantCalls := providerSession.assistantCallsSnapshot()
	if len(assistantCalls) != 1 || assistantCalls[0].Name != webmcp.ListTabsToolName {
		t.Fatalf("assistant calls before customer choice = %#v, want one list-tabs call", assistantCalls)
	}
	toolResults := providerSession.toolResultsSnapshot()
	if len(toolResults) == 0 || !strings.Contains(toolResults[0].Arguments, "tab-cube") || !strings.Contains(toolResults[0].Arguments, "tab-margin") {
		t.Fatalf("list-tabs result = %#v, want both exact tab identities", toolResults)
	}
	assertRuntimeHasNoOperation(t, runtime, testkit.OperationAttach, testkit.OperationEnableWebMCP, testkit.OperationInvoke)

	answer <- servicetest.ScheduledAudioInput{PCM: []byte{1, 2, 3}, EndOfTurn: true}
	waitAmbiguousConversationSignal(t, ctx, runComplete, providerSession.selectionCallSent, "exact tab selection call")
	assistantCalls = providerSession.assistantCallsSnapshot()
	if len(assistantCalls) != 2 || assistantCalls[1].Name != webmcp.SelectTabToolName {
		t.Fatalf("assistant calls after customer choice = %#v, want list-tabs then select-tab", assistantCalls)
	}
	var selectionArgs struct {
		BrowserID string `json:"browser_id"`
		TargetID  string `json:"target_id"`
	}
	if err := json.Unmarshal([]byte(assistantCalls[1].Arguments), &selectionArgs); err != nil {
		t.Fatalf("decode exact selection arguments: %v", err)
	}
	if selectionArgs.BrowserID != string(candidate.ID) || selectionArgs.TargetID != string(cubeTarget.ID) {
		t.Fatalf("selection arguments = %+v, want browser %q and target %q", selectionArgs, candidate.ID, cubeTarget.ID)
	}

	selectedUpdate := readAmbiguousCubeConversationUpdate(t, ctx, runComplete, providerSession, func(update *messages.SessionUpdateValue) bool {
		return containsAmbiguousDefinition(update.Tools, cubeTool.Name)
	})
	assertAmbiguousPageSurface(t, selectedUpdate.Tools, surface.base, []string{cubeTool.Name, cubeMoveTool.Name}, "selected provider surface")
	waitAmbiguousConversationSignal(t, ctx, runComplete, providerSession.pageCallSent, "selected page tool call")
	assistantCalls = providerSession.assistantCallsSnapshot()
	if len(assistantCalls) != 3 || assistantCalls[2].Name != cubeTool.Name {
		t.Fatalf("assistant calls after selection = %#v, want list-tabs, select-tab, get-cube-state", assistantCalls)
	}
	if containsAmbiguousCall(assistantCalls, marginTool.Name) {
		t.Fatalf("assistant called unselected Margin tool: %#v", assistantCalls)
	}

	select {
	case <-runComplete:
		if err := <-runErr; err != nil {
			t.Fatalf("ambiguous cube conversation returned an error: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for ambiguous cube conversation completion: %v", ctx.Err())
	}

	outputText := output.String()
	for _, expected := range []string{"Cubecade", "https://cube.example.test", "Margin", "https://margin.example.test", "Use Cubecade", "Cubecade is ready for inspection."} {
		if !strings.Contains(outputText, expected) {
			t.Fatalf("conversation output missing %q: %s", expected, outputText)
		}
	}
	lowerOutput := strings.ToLower(outputText)
	for _, forbidden := range []string{"upload", "share a link", "describe the arrangement", "browser unavailable", "manual page"} {
		if strings.Contains(lowerOutput, forbidden) {
			t.Fatalf("conversation output fabricated a workaround %q: %s", forbidden, outputText)
		}
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

func containsAmbiguousDefinition(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func containsAmbiguousCall(calls []messages.ToolCall, name string) bool {
	for _, call := range calls {
		if call.Name == name {
			return true
		}
	}
	return false
}

func readAmbiguousCubeConversationUpdate(t *testing.T, ctx context.Context, runComplete <-chan struct{}, session *ambiguousCubeConversationSession, want func(*messages.SessionUpdateValue) bool) *messages.SessionUpdateValue {
	t.Helper()
	for {
		select {
		case update := <-session.updates:
			if update != nil && want(update) {
				return update
			}
		case <-runComplete:
			t.Fatal("session loop ended before receiving expected provider SESSION.UPDATE")
		case <-ctx.Done():
			t.Fatalf("waiting for expected provider SESSION.UPDATE: %v", ctx.Err())
		}
	}
}

func waitAmbiguousConversationSignal(t *testing.T, ctx context.Context, runComplete <-chan struct{}, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-runComplete:
		t.Fatalf("session loop ended before %s", label)
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", label, ctx.Err())
	}
}

type ambiguousCubeConversationPhase uint8

const (
	ambiguousCubeConversationInitial ambiguousCubeConversationPhase = iota
	ambiguousCubeConversationAwaitingListResult
	ambiguousCubeConversationAwaitingChoice
	ambiguousCubeConversationAwaitingSelectionResult
	ambiguousCubeConversationWaitingForPageTools
	ambiguousCubeConversationAwaitingPageResult
	ambiguousCubeConversationComplete
)

type ambiguousCubeConversationSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	updates           chan *messages.SessionUpdateValue
	questionSent      chan struct{}
	selectionCallSent chan struct{}
	pageCallSent      chan struct{}
	pageToolsReady    chan struct{}

	closeOnce          sync.Once
	questionOnce       sync.Once
	selectionCallOnce  sync.Once
	pageCallOnce       sync.Once
	pageToolsReadyOnce sync.Once

	mu             sync.Mutex
	phase          ambiguousCubeConversationPhase
	lastToolResult string
	assistantCalls []messages.ToolCall
	toolResults    []messages.ToolCallEndValue
}

func newAmbiguousCubeConversationSession() *ambiguousCubeConversationSession {
	return &ambiguousCubeConversationSession{
		recv:              messages.NewTypedBuffer[messages.StreamMessage](128),
		done:              make(chan struct{}),
		updates:           make(chan *messages.SessionUpdateValue, 32),
		questionSent:      make(chan struct{}),
		selectionCallSent: make(chan struct{}),
		pageCallSent:      make(chan struct{}),
		pageToolsReady:    make(chan struct{}),
		phase:             ambiguousCubeConversationInitial,
	}
}

func (s *ambiguousCubeConversationSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
	}

	var emitList, emitQuestion, emitSelection, emitFinal bool
	var emitPageCall bool
	s.mu.Lock()
	switch message.Type {
	case messages.StreamTypeSessionUpdate:
		value, ok := message.Value.(*messages.SessionUpdateValue)
		if ok && value != nil {
			update := *value
			update.Tools = append([]messages.ToolDefinition(nil), value.Tools...)
			select {
			case s.updates <- &update:
			default:
			}
			if containsAmbiguousDefinition(update.Tools, "get_cube_state") {
				s.pageToolsReadyOnce.Do(func() { close(s.pageToolsReady) })
			}
		}
	case messages.StreamTypeTextDelta:
		if s.phase == ambiguousCubeConversationInitial {
			s.phase = ambiguousCubeConversationAwaitingListResult
			emitList = true
		}
	case messages.StreamTypeToolCallEnd:
		value, ok := message.Value.(*messages.ToolCallEndValue)
		if ok && value != nil {
			s.lastToolResult = value.Name
			s.toolResults = append(s.toolResults, *value)
			switch value.Name {
			case webmcp.ListTabsToolName:
				if s.phase == ambiguousCubeConversationAwaitingListResult {
					s.phase = ambiguousCubeConversationAwaitingChoice
				}
			case webmcp.SelectTabToolName:
				if s.phase == ambiguousCubeConversationAwaitingSelectionResult {
					s.phase = ambiguousCubeConversationWaitingForPageTools
				}
			}
		}
	case messages.StreamTypeResponseCreate:
		switch {
		case s.phase == ambiguousCubeConversationAwaitingChoice && s.lastToolResult == webmcp.ListTabsToolName:
			emitQuestion = true
		case s.phase == ambiguousCubeConversationWaitingForPageTools && s.lastToolResult == webmcp.SelectTabToolName:
			emitPageCall = true
		case s.phase == ambiguousCubeConversationAwaitingPageResult && s.lastToolResult == "get_cube_state":
			s.phase = ambiguousCubeConversationComplete
			emitFinal = true
		}
	case messages.StreamTypeMessageEnd:
		if s.phase == ambiguousCubeConversationAwaitingChoice {
			s.phase = ambiguousCubeConversationAwaitingSelectionResult
			emitSelection = true
		}
	case messages.StreamTypeSessionClose:
		s.closeOnce.Do(func() { close(s.done) })
	}
	s.mu.Unlock()

	if emitList {
		s.emitAssistantToolCall("call-list-tabs", webmcp.ListTabsToolName, `{"include_zero_tool_pages":true}`)
	}
	if emitQuestion {
		s.emitChoiceQuestion()
	}
	if emitSelection {
		s.emitSelectionTurn()
	}
	if emitPageCall {
		go s.emitPageToolWhenReady(ctx)
	}
	if emitFinal {
		s.emitAssistantText("Cubecade is ready for inspection.")
		s.write(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("ambiguous-session", "complete")})
	}
	return true
}

func (s *ambiguousCubeConversationSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *ambiguousCubeConversationSession) Done() <-chan struct{} { return s.done }

func (s *ambiguousCubeConversationSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *ambiguousCubeConversationSession) emitAssistantToolCall(id, name, arguments string) {
	s.mu.Lock()
	s.assistantCalls = append(s.assistantCalls, messages.ToolCall{ID: id, Name: name, Arguments: arguments})
	s.mu.Unlock()
	if name == webmcp.SelectTabToolName {
		s.selectionCallOnce.Do(func() { close(s.selectionCallSent) })
	}
	if name == "get_cube_state" {
		s.pageCallOnce.Do(func() { close(s.pageCallSent) })
	}
	s.write(
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(id, name)},
		messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(id, name, arguments)},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)
}

func (s *ambiguousCubeConversationSession) emitChoiceQuestion() {
	s.questionOnce.Do(func() {
		s.emitAssistantText("Which page should I use: Cubecade (https://cube.example.test) or Margin (https://margin.example.test)?")
		close(s.questionSent)
	})
}

func (s *ambiguousCubeConversationSession) emitSelectionTurn() {
	s.write(
		messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValue("Use Cubecade.")},
	)
	s.emitAssistantToolCall(webmcpSelectionCallID, webmcp.SelectTabToolName, `{"browser_id":"browser-ambiguous","target_id":"tab-cube"}`)
}

func (s *ambiguousCubeConversationSession) emitPageToolWhenReady(ctx context.Context) {
	select {
	case <-s.pageToolsReady:
	case <-s.done:
		return
	case <-ctx.Done():
		return
	}
	s.mu.Lock()
	if s.phase != ambiguousCubeConversationWaitingForPageTools {
		s.mu.Unlock()
		return
	}
	s.phase = ambiguousCubeConversationAwaitingPageResult
	s.mu.Unlock()
	s.emitAssistantToolCall("call-cube-state", "get_cube_state", `{}`)
}

func (s *ambiguousCubeConversationSession) emitAssistantText(text string) {
	s.write(
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)},
		messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)
}

func (s *ambiguousCubeConversationSession) write(messagesToWrite ...messages.StreamMessage) bool {
	for _, message := range messagesToWrite {
		if !s.recv.Write(context.Background(), message) {
			return false
		}
	}
	return true
}

func (s *ambiguousCubeConversationSession) assistantCallsSnapshot() []messages.ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.ToolCall(nil), s.assistantCalls...)
}

func (s *ambiguousCubeConversationSession) toolResultsSnapshot() []messages.ToolCallEndValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.ToolCallEndValue(nil), s.toolResults...)
}

type ambiguousCubeConversationInferencer struct {
	mu          sync.Mutex
	session     *ambiguousCubeConversationSession
	connectionN int
}

func (i *ambiguousCubeConversationInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
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

func (i *ambiguousCubeConversationInferencer) connections() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectionN
}

const webmcpSelectionCallID = "call-select-tab"

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
