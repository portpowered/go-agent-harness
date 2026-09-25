package cli

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
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionAmbiguousTabsPublishOnlySelectedPageTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cubeTool := ambiguousFixtureTool(ambiguousCubeStateTool, "Read the Cubecade state.", "cube-frame")
	fixture := newAmbiguousBrowserFixture(t, ctx, cubeTool)
	surface := fixture.surface
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
		runErr <- newTestSessionCommand(nil, nil, testSessionDeps{Inferencer: provider, Capabilities: borrowedTestCapabilities(fixture.capabilities)}).runSessionRequest(sessionCtx, io.Discard, io.Discard, serviceSession.Request{
			Provider: config.ProviderGrok, Model: "ambiguous-session", APIKey: "unused",
			LoadedConfig: fixture.cfg, BrowserToolsEnabled: true, WaitForClose: true,
		})
	}()
	defer stopAmbiguousSession(t, cancelSession, runErr, nil)

	initialDefinitions := readAmbiguousPageToolsSessionUpdate(t, ctx, runErr, providerSession)
	assertAmbiguousPageSurface(t, initialDefinitions, surface.base, nil, "initial provider surface")

	listed := listAmbiguousTabs(t, ctx, surface.executor, fixture.candidate.ID)
	if listed[fixture.cubeTarget.Title] == "" || listed[fixture.marginTarget.Title] == "" {
		t.Fatalf("listed ambiguous target identities = %#v, want Cubecade and Margin", listed)
	}
	assertRuntimeHasNoOperation(t, fixture.runtime, testkit.OperationAttach, testkit.OperationEnableWebMCP, testkit.OperationInvoke)

	selectEnvelope := executeAmbiguousPageToolsCall(t, ctx, surface.executor, webmcp.SelectTabToolName, `{"browser_id":"`+string(fixture.candidate.ID)+`","target_id":"`+listed[fixture.cubeTarget.Title]+`"}`)
	if !selectEnvelope.OK {
		t.Fatalf("exact Cubecade selection failed: %+v", selectEnvelope.Error)
	}
	selectedDefinitions := readAmbiguousPageToolsSessionUpdate(t, ctx, runErr, providerSession)
	assertAmbiguousPageSurface(t, selectedDefinitions, surface.base, []string{cubeTool.Name}, "selected provider surface")

	assertCubePageToolCompleted(t, executeAmbiguousPageToolsCall(t, ctx, surface.executor, cubeTool.Name, `{}`))
	if provider.connections() != 1 {
		t.Fatalf("provider connections = %d, want one session connection", provider.connections())
	}
	fixture.assertOnlySelectedCubeOperations(t, cubeTool.Name)
}

func TestSessionAmbiguousCubeConversationRequiresChoiceBeforePageWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	cubeTool := ambiguousFixtureTool(ambiguousCubeStateTool, "Read the Cubecade state.", "cube-frame")
	cubeMoveTool := ambiguousFixtureTool("queue_cube_moves", "Queue moves on the Cubecade.", "cube-frame")
	fixture := newAmbiguousBrowserFixture(t, ctx, cubeTool, cubeMoveTool)
	surface := fixture.surface

	providerSession := newAmbiguousCubeConversationSession()
	provider := &ambiguousCubeConversationInferencer{session: providerSession}
	sessionCtx, cancelSession := context.WithCancel(ctx)
	conversation := startTestLiveConversation(t, sessionCtx, testSessionDeps{Inferencer: provider, Capabilities: borrowedTestCapabilities(fixture.capabilities)}, serviceSession.Request{
		Provider: config.ProviderGrok, Model: "ambiguous-session", APIKey: "unused", ConfigDir: t.TempDir(),
		Prompt: "Inspect the cube on the connected browser.", PromptProvided: true, LoadedConfig: fixture.cfg,
		BrowserToolsEnabled: true, WaitForClose: true, SystemPrompt: "You are a careful cube assistant.",
	})
	runErr, runComplete := conversation.runErr, conversation.runComplete
	defer stopAmbiguousSession(t, cancelSession, runErr, runComplete)

	initialUpdate := readAmbiguousCubeConversationUpdate(t, ctx, runComplete, providerSession, func(update *messages.SessionUpdateValue) bool {
		return update.Instructions != ""
	})
	assertAmbiguousPageSurface(t, initialUpdate.Tools, surface.base, nil, "initial provider surface")
	assertAmbiguousChoiceInstructions(t, initialUpdate, cubeTool.Name, cubeMoveTool.Name, fixture.marginTool.Name)

	waitAmbiguousConversationSignal(t, ctx, runComplete, providerSession.questionSent, "provider choice question")
	assistantCalls := providerSession.assistantCallsSnapshot()
	if len(assistantCalls) != 1 || assistantCalls[0].Name != webmcp.ListTabsToolName {
		t.Fatalf("assistant calls before customer choice = %#v, want one list-tabs call", assistantCalls)
	}
	toolResults := providerSession.toolResultsSnapshot()
	if len(toolResults) == 0 || !strings.Contains(toolResults[0].Arguments, "tab-cube") || !strings.Contains(toolResults[0].Arguments, "tab-margin") {
		t.Fatalf("list-tabs result = %#v, want both exact tab identities", toolResults)
	}
	assertRuntimeHasNoOperation(t, fixture.runtime, testkit.OperationAttach, testkit.OperationEnableWebMCP, testkit.OperationInvoke)

	conversation.commitCustomerTurn(t, ctx)
	waitAmbiguousConversationSignal(t, ctx, runComplete, providerSession.selectionCallSent, "exact tab selection call")
	assistantCalls = providerSession.assistantCallsSnapshot()
	if len(assistantCalls) != 2 || assistantCalls[1].Name != webmcp.SelectTabToolName {
		t.Fatalf("assistant calls after customer choice = %#v, want list-tabs then select-tab", assistantCalls)
	}
	fixture.assertExactCubeSelection(t, assistantCalls[1].Arguments)

	selectedUpdate := readAmbiguousCubeConversationUpdate(t, ctx, runComplete, providerSession, func(update *messages.SessionUpdateValue) bool {
		return containsAmbiguousDefinition(update.Tools, cubeTool.Name)
	})
	assertAmbiguousPageSurface(t, selectedUpdate.Tools, surface.base, []string{cubeTool.Name, cubeMoveTool.Name}, "selected provider surface")
	waitAmbiguousConversationSignal(t, ctx, runComplete, providerSession.pageCallSent, "selected page tool call")
	assistantCalls = providerSession.assistantCallsSnapshot()
	if len(assistantCalls) != 3 || assistantCalls[2].Name != cubeTool.Name {
		t.Fatalf("assistant calls after selection = %#v, want list-tabs, select-tab, get-cube-state", assistantCalls)
	}
	if containsAmbiguousCall(assistantCalls, fixture.marginTool.Name) {
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
	assertAmbiguousConversationOutput(t, conversation.output.String())
	if provider.connections() != 1 {
		t.Fatalf("provider connections = %d, want one session connection", provider.connections())
	}
	fixture.assertOnlySelectedCubeOperations(t, cubeTool.Name)
}

// ambiguousBrowserFixture is a scripted browser with two eligible tabs,
// Cubecade and Margin, composed into session capabilities with no page
// selected yet.
type ambiguousBrowserFixture struct {
	candidate                webmcp.BrowserCandidate
	cubeTarget, marginTarget webmcp.Target
	marginTool               webmcp.ToolDescriptor
	runtime                  *testkit.ScriptedBrowserRuntime
	cfg                      *config.Config
	capabilities             SessionToolCapabilities
	surface                  resolvedSessionToolSurface
}

func newAmbiguousBrowserFixture(t *testing.T, ctx context.Context, cubeTools ...webmcp.ToolDescriptor) *ambiguousBrowserFixture {
	t.Helper()
	candidate := webmcp.BrowserCandidate{ID: "browser-ambiguous", Source: webmcp.DiscoverySourceExplicit, Product: "scripted", Protocol: "1.3", HTTPURL: testCDPURL, Loopback: true, Explicit: true}
	f := &ambiguousBrowserFixture{
		candidate:    candidate,
		cubeTarget:   ambiguousFixtureTarget(candidate.ID, "tab-cube", "Cubecade", "cube"),
		marginTarget: ambiguousFixtureTarget(candidate.ID, "tab-margin", "Margin", "margin"),
		marginTool:   ambiguousFixtureTool("get_margin_state", "Read the Margin state.", "margin-frame"),
	}
	f.runtime = testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(f.cubeTarget, testkit.WithInitialCatalog(cubeTools...), testkit.WithAutoResponse(json.RawMessage(`{"page":"cube"}`))),
		testkit.NewTargetConfig(f.marginTarget, testkit.WithInitialCatalog(f.marginTool), testkit.WithAutoResponse(json.RawMessage(`{"page":"margin"}`))),
	))
	discoveryService := &ambiguousSessionDiscovery{
		candidate: discovery.BrowserCandidate{ID: string(candidate.ID), Source: discovery.SourceExplicitCDPHTTP, Product: candidate.Product, Protocol: candidate.Protocol, Loopback: true},
		targets:   []discovery.Target{ambiguousSessionLaneTarget(f.cubeTarget, len(cubeTools)), ambiguousSessionLaneTarget(f.marginTarget, 1)},
	}
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = candidate.HTTPURL
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	f.cfg = browserCapabilityConfig(t, true)
	f.cfg.Browser = browser
	f.cfg.Model = config.ModelConfig{Provider: config.ProviderGrok, Grok: &config.GrokConfig{Model: "ambiguous-session", APIKey: "unused"}}
	productionFactory := NewProductionWebMCPDoctorFactory(WithWebMCPProductionRuntime(f.runtime), WithWebMCPProductionDiscovery(discoveryService))
	capabilities, err := NewSessionToolCapabilitiesFactory(nil, func(browser config.BrowserConfig) (webmcp.Broker, error) {
		return newSessionBrowserBrokerWithDoctorFactory(browser, productionFactory)
	})(f.cfg)
	if err != nil {
		t.Fatalf("construct session capabilities: %v", err)
	}
	f.capabilities = capabilities
	t.Cleanup(func() {
		closeForTest(t, capabilities.Close)
		closeForTest(t, f.runtime.Close)
	})
	f.surface = resolveSessionToolSurface(ctx, capabilities)
	if f.surface.browserState != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("initial browser state = %q, want connected_unselected", f.surface.browserState)
	}
	assertAmbiguousPageSurface(t, f.surface.definitions, f.surface.base, nil, "initial CLI surface")
	return f
}

func ambiguousFixtureTarget(browserID webmcp.BrowserID, id webmcp.TargetID, title, host string) webmcp.Target {
	return webmcp.Target{
		BrowserID: browserID, ID: id, Type: "page", Title: title,
		URL: "https://" + host + ".example.test/", Origin: "https://" + host + ".example.test", Generation: 1,
		WebMCPDomainSupported: true, PageToolsReady: true, PageToolsKnown: true, Eligible: true,
	}
}

func ambiguousFixtureTool(name, description string, frameID webmcp.FrameID) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{Name: name, Description: description, FrameID: frameID, InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}
}

// stopAmbiguousSession cancels the session loop and requires it to stop
// cleanly; runComplete reports that a final assertion already consumed runErr.
func stopAmbiguousSession(t *testing.T, cancelSession context.CancelFunc, runErr <-chan error, runComplete <-chan struct{}) {
	t.Helper()
	cancelSession()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("session loop shutdown: %v", err)
		}
	case <-runComplete:
	case <-time.After(time.Second):
		t.Error("session loop did not stop after cancellation")
	}
}

// listAmbiguousTabs lists both eligible tabs and returns target IDs by title.
func listAmbiguousTabs(t *testing.T, ctx context.Context, executor messages.ToolExecutor, browserID webmcp.BrowserID) map[string]string {
	t.Helper()
	listEnvelope := executeAmbiguousPageToolsCall(t, ctx, executor, webmcp.ListTabsToolName, `{"include_zero_tool_pages":true}`)
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
		if !target.Eligible || target.BrowserID != string(browserID) {
			t.Fatalf("listed ambiguous target = %+v, want eligible target on %q", target, browserID)
		}
		listed[target.Title] = target.TargetID
	}
	return listed
}

func assertCubePageToolCompleted(t *testing.T, pageEnvelope webmcp.ToolResultEnvelope) {
	t.Helper()
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
}

func assertAmbiguousChoiceInstructions(t *testing.T, update *messages.SessionUpdateValue, unselectedTools ...string) {
	t.Helper()
	for _, required := range []string{
		"browser endpoint is connected",
		"no page is selected",
		webmcp.ListTabsToolName,
		"ask the customer which page to use",
		"exact browser_id and target_id",
		"do not invoke page tools",
	} {
		if !strings.Contains(update.Instructions, required) {
			t.Fatalf("initial provider instructions missing %q: %s", required, update.Instructions)
		}
	}
	for _, name := range unselectedTools {
		if containsAmbiguousDefinition(update.Tools, name) {
			t.Fatalf("initial provider surface advertised an unselected page tool %q: %#v", name, update.Tools)
		}
	}
}

func (f *ambiguousBrowserFixture) assertExactCubeSelection(t *testing.T, arguments string) {
	t.Helper()
	var selectionArgs struct {
		BrowserID string `json:"browser_id"`
		TargetID  string `json:"target_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &selectionArgs); err != nil {
		t.Fatalf("decode exact selection arguments: %v", err)
	}
	if selectionArgs.BrowserID != string(f.candidate.ID) || selectionArgs.TargetID != string(f.cubeTarget.ID) {
		t.Fatalf("selection arguments = %+v, want browser %q and target %q", selectionArgs, f.candidate.ID, f.cubeTarget.ID)
	}
}

func assertAmbiguousConversationOutput(t *testing.T, outputText string) {
	t.Helper()
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
}

// assertOnlySelectedCubeOperations requires exactly one attach, enable, and
// page-tool invoke, all on the selected Cubecade tab and none on Margin.
func (f *ambiguousBrowserFixture) assertOnlySelectedCubeOperations(t *testing.T, toolName string) {
	t.Helper()
	var attaches, enables, invokes []testkit.Operation
	for _, operation := range f.runtime.Operations() {
		switch operation.Kind {
		case testkit.OperationAttach:
			attaches = append(attaches, operation)
		case testkit.OperationEnableWebMCP:
			enables = append(enables, operation)
		case testkit.OperationInvoke:
			invokes = append(invokes, operation)
		}
	}
	if len(attaches) != 1 || attaches[0].TargetID != f.cubeTarget.ID {
		t.Fatalf("attach operations = %#v, want exactly selected Cubecade target", attaches)
	}
	if len(enables) != 1 || enables[0].TargetID != f.cubeTarget.ID {
		t.Fatalf("WebMCP enable operations = %#v, want exactly selected Cubecade target", enables)
	}
	if len(invokes) != 1 || invokes[0].TargetID != f.cubeTarget.ID || invokes[0].ToolName != toolName {
		t.Fatalf("invoke operations = %#v, want exactly one selected Cubecade call", invokes)
	}
	for _, operation := range append(append(attaches, enables...), invokes...) {
		if operation.TargetID == f.marginTarget.ID {
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
	s.mu.Lock()
	emits := s.advanceLocked(message)
	s.mu.Unlock()
	s.emit(ctx, emits)
	return true
}

// ambiguousCubeConversationEmits names the scripted provider outputs one
// client message triggers; they are emitted after the phase lock is released.
type ambiguousCubeConversationEmits struct {
	list, question, selection, pageCall, final bool
}

func (s *ambiguousCubeConversationSession) advanceLocked(message messages.StreamMessage) ambiguousCubeConversationEmits {
	var emits ambiguousCubeConversationEmits
	switch message.Type {
	case messages.StreamTypeSessionUpdate:
		s.recordSessionUpdateLocked(message.Value)
	case messages.StreamTypeTextDelta:
		if s.phase == ambiguousCubeConversationInitial {
			s.phase = ambiguousCubeConversationAwaitingListResult
			emits.list = true
		}
	case messages.StreamTypeToolCallEnd:
		s.recordToolResultLocked(message.Value)
	case messages.StreamTypeResponseCreate:
		switch {
		case s.phase == ambiguousCubeConversationAwaitingChoice && s.lastToolResult == webmcp.ListTabsToolName:
			emits.question = true
		case s.phase == ambiguousCubeConversationWaitingForPageTools && s.lastToolResult == webmcp.SelectTabToolName:
			emits.pageCall = true
		case s.phase == ambiguousCubeConversationAwaitingPageResult && s.lastToolResult == ambiguousCubeStateTool:
			s.phase = ambiguousCubeConversationComplete
			emits.final = true
		}
	case messages.StreamTypeMessageEnd:
		if s.phase == ambiguousCubeConversationAwaitingChoice {
			s.phase = ambiguousCubeConversationAwaitingSelectionResult
			emits.selection = true
		}
	case messages.StreamTypeSessionClose:
		s.closeOnce.Do(func() { close(s.done) })
	}
	return emits
}

func (s *ambiguousCubeConversationSession) recordSessionUpdateLocked(raw any) {
	value, ok := raw.(*messages.SessionUpdateValue)
	if !ok || value == nil {
		return
	}
	update := *value
	update.Tools = append([]messages.ToolDefinition(nil), value.Tools...)
	select {
	case s.updates <- &update:
	default:
	}
	if containsAmbiguousDefinition(update.Tools, ambiguousCubeStateTool) {
		s.pageToolsReadyOnce.Do(func() { close(s.pageToolsReady) })
	}
}

func (s *ambiguousCubeConversationSession) recordToolResultLocked(raw any) {
	value, ok := raw.(*messages.ToolCallEndValue)
	if !ok || value == nil {
		return
	}
	s.lastToolResult = value.Name
	s.toolResults = append(s.toolResults, *value)
	switch {
	case value.Name == webmcp.ListTabsToolName && s.phase == ambiguousCubeConversationAwaitingListResult:
		s.phase = ambiguousCubeConversationAwaitingChoice
	case value.Name == webmcp.SelectTabToolName && s.phase == ambiguousCubeConversationAwaitingSelectionResult:
		s.phase = ambiguousCubeConversationWaitingForPageTools
	}
}

func (s *ambiguousCubeConversationSession) emit(ctx context.Context, emits ambiguousCubeConversationEmits) {
	if emits.list {
		s.emitAssistantToolCall("call-list-tabs", webmcp.ListTabsToolName, `{"include_zero_tool_pages":true}`)
	}
	if emits.question {
		s.emitChoiceQuestion()
	}
	if emits.selection {
		s.emitSelectionTurn()
	}
	if emits.pageCall {
		go s.emitPageToolWhenReady(ctx)
	}
	if emits.final {
		s.emitAssistantText("Cubecade is ready for inspection.")
		s.write(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("ambiguous-session", "complete")})
	}
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
	if name == ambiguousCubeStateTool {
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
