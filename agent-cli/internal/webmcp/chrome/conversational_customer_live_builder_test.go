package chrome

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	looptranscript "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	browserconversation "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
	browserconversationWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation/wire"
)

func TestBuildConversationalCustomerResultPreservesRecoveryOrderAndRawInput(t *testing.T) {
	scenario := newConversationalCustomerScenario("https://fixture.test/", "https://fixture.test/settings")
	recordingDir := t.TempDir()
	providerCalls := []struct {
		name      string
		arguments string
	}{
		{name: webmcp.ListToolsToolName, arguments: `{}`},
		{name: webmcp.InvokeToolName, arguments: `{"tool_ref":"home-label-1","input_json":"{\"value\":\"live alpha\"}"}`},
		{name: webmcp.ListToolsToolName, arguments: `{}`},
		{name: webmcp.InvokeToolName, arguments: `{"tool_ref":"home-theme-1","input_json":"{\"value\":\"live dark\"}"}`},
		{name: webmcp.ListToolsToolName, arguments: `{}`},
		{name: webmcp.InvokeToolName, arguments: `{"tool_ref":"settings-priority-1","input_json":"{\"value\":\"high\"}"}`},
		{name: webmcp.ListToolsToolName, arguments: `{}`},
		{name: webmcp.InvokeToolName, arguments: `{"tool_ref":"settings-priority-2","input_json":"{\"value\":\"high\"}"}`},
		{name: webmcp.ListToolsToolName, arguments: `{}`},
		{name: webmcp.InvokeToolName, arguments: `{"tool_ref":"home-label-2","input_json":"{\"value\":\"live corrected\"}"}`},
		{name: webmcp.InvokeToolName, arguments: `{"tool_ref":"home-pending-2","input_json":"{\"value\":\"hold\"}"}`},
	}
	transcript := make([][]byte, 0, len(providerCalls))
	for index, call := range providerCalls {
		transcript = append(transcript, conversationalCustomerProviderRecord(t, index, call.name, call.arguments))
	}
	writeConversationalCustomerRecords(t, filepath.Join(recordingDir, "agent.transcript.jsonl"), transcript)
	writeConversationalCustomerSessionLog(t, filepath.Join(recordingDir, "session-log.jsonl"))

	labelInitial := browserEventTool("home-label-1", "webmcp_customer_set_label", 1)
	themeInitial := browserEventTool("home-theme-1", "webmcp_customer_set_theme", 1)
	priorityStale := browserEventTool("settings-priority-1", "webmcp_customer_set_priority", 1)
	priorityFresh := browserEventTool("settings-priority-2", "webmcp_customer_set_priority", 2)
	labelCorrection := browserEventTool("home-label-2", "webmcp_customer_set_label", 3)
	pendingTool := browserEventTool("home-pending-2", "webmcp_customer_pending", 3)
	events := []webmcp.BrowserEvent{
		{Type: webmcp.EventToolsAdded, Generation: 1, Tools: []webmcp.ToolDescriptor{labelInitial, themeInitial, priorityStale, browserEventTool("home-pending-1", "webmcp_customer_pending", 1)}},
		browserEventInvocation(labelInitial, "browser-label", `{"value":"live alpha"}`),
		browserEventTerminal("browser-label", "Completed", json.RawMessage(`{"ok":true}`), 1),
		browserEventInvocation(themeInitial, "browser-theme", `{"value":"live dark"}`),
		browserEventTerminal("browser-theme", "Completed", json.RawMessage(`{"ok":true}`), 1),
		{Type: webmcp.EventPageNavigated, Generation: 2, PreviousGeneration: 1},
		{Type: webmcp.EventToolsAdded, Generation: 2, Tools: []webmcp.ToolDescriptor{priorityFresh}},
		browserEventInvocation(priorityFresh, "browser-priority", `{"value":"high"}`),
		browserEventTerminal("browser-priority", "Completed", json.RawMessage(`{"ok":true}`), 2),
		{Type: webmcp.EventPageNavigated, Generation: 3, PreviousGeneration: 2},
		{Type: webmcp.EventToolsAdded, Generation: 3, Tools: []webmcp.ToolDescriptor{labelCorrection, pendingTool}},
		browserEventInvocation(labelCorrection, "browser-correction", `{"value":"live corrected"}`),
		browserEventTerminal("browser-correction", "Completed", json.RawMessage(`{"ok":true}`), 3),
		browserEventInvocation(pendingTool, "browser-pending", `{"value":"hold"}`),
		browserEventTerminal("browser-pending", "Canceled", nil, 3),
	}
	navigations := []conversationalCustomerNavigationObservation{
		{StepID: "stale_recovery", Event: webmcp.BrowserEvent{Type: webmcp.EventPageNavigated, Generation: 2, PreviousGeneration: 1}},
		{StepID: "correction", Event: webmcp.BrowserEvent{Type: webmcp.EventPageNavigated, Generation: 3, PreviousGeneration: 2}},
	}
	oracles := conversationalCustomerBuilderOracles()
	result, err := buildConversationalCustomerResult(
		scenario,
		events,
		filepath.Join(recordingDir, "session-log.jsonl"),
		navigations,
		oracles,
		"browser-a",
		"target-a",
		conversationalCustomerProbe{PageID: conversationalCustomerHomePage, BrowserID: "browser-a", TargetID: "target-a", Alive: true, Responsive: true, AllowsMutation: true, ReadSucceeded: true, MutationSucceeded: true},
		events[len(events)-2],
		conversationalCustomerCancelResult{InvocationID: "browser-pending", Status: "cancel_requested"},
	)
	if err != nil {
		t.Fatalf("build result: %v", err)
	}
	if len(result.Turns) != 12 {
		t.Fatalf("turns = %d, want six customer/assistant turns", len(result.Turns))
	}
	for index := 1; index < len(result.BrokerCalls); index++ {
		if result.BrokerCalls[index-1].Sequence >= result.BrokerCalls[index].Sequence {
			t.Fatalf("broker call sequence is not strictly ordered at %d: %#v", index, result.BrokerCalls)
		}
	}
	if len(result.Recovery) != 1 || !result.Recovery[0].Passed || !result.Recovery[0].StaleRejected || !result.Recovery[0].ToolsRelisted {
		t.Fatalf("recovery = %+v, want ordered stale reject/re-list/retry evidence", result.Recovery)
	}
	if len(result.Corrections) != 1 || !result.Corrections[0].Passed {
		t.Fatalf("corrections = %+v, want grounded correction evidence", result.Corrections)
	}
	if result.InputJSONValidity.TotalAttempts != 12 || result.InputJSONValidity.ValidObjectStrings != 12 {
		t.Fatalf("input_json validity = %+v, want all twelve invoke observations valid", result.InputJSONValidity)
	}
	if fmt.Sprint(result.Lifecycle.ExternalBrowserID) != "browser-a" || fmt.Sprint(result.Lifecycle.ExternalTargetID) != "target-a" {
		t.Fatalf("lifecycle target identity = %+v, want probe identity", result.Lifecycle)
	}
}

func conversationalCustomerProviderRecord(t *testing.T, index int, name, arguments string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":      "response.function_call_arguments.done",
		"call_id":   "provider-call-" + string(rune('a'+index)),
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		t.Fatalf("encode provider event: %v", err)
	}
	record, err := looptranscript.Encode(looptranscript.NewRecord(uint64(index+1), testTranscriptTime(), looptranscript.PeerAgent, looptranscript.DirectionIn, looptranscript.StreamWebSocket, payload))
	if err != nil {
		t.Fatalf("encode provider record: %v", err)
	}
	return record
}

func writeConversationalCustomerSessionLog(t *testing.T, path string) {
	t.Helper()
	logs := []conversationalCustomerSessionLogEntry{
		conversationalCustomerSessionLog("Set the customer label to live alpha.", "Label is live alpha."),
		conversationalCustomerSessionLog("Now set the customer theme to live dark.", "Theme is live dark."),
		conversationalCustomerSessionLog("Set the customer priority to high.", "Priority is high."),
		conversationalCustomerSessionLog("Actually change the customer label to live corrected.", "Label is live corrected."),
		conversationalCustomerSessionLog("Hold this customer request while I decide.", "The request is pending."),
		conversationalCustomerSessionLog("Stop and cancel that request.", "The request was canceled."),
	}
	records := make([][]byte, 0, len(logs))
	for _, entry := range logs {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("encode session log: %v", err)
		}
		records = append(records, append(encoded, '\n'))
	}
	writeConversationalCustomerRecords(t, path, records)
}

// writeConversationalCustomerRecords writes pre-encoded records to a fresh
// file so encoding failures never leave a partially written open handle.
func writeConversationalCustomerRecords(t *testing.T, path string, records [][]byte) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Join(records, nil), 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}

func conversationalCustomerBuilderOracles() []conversationalCustomerOracleObservation {
	return []conversationalCustomerOracleObservation{
		{StepID: "initial_action", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: "unset", Theme: "default", Priority: "normal", VisibleText: "unset/default"}},
		{StepID: "initial_action", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: "default", Priority: "normal", VisibleText: conversationalCustomerLabel + "/default"}},
		{StepID: "second_action", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: "default", Priority: "normal", VisibleText: conversationalCustomerLabel + "/default"}},
		{StepID: "second_action", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: "normal", VisibleText: conversationalCustomerLabel + "/" + conversationalCustomerTheme}},
		{StepID: "stale_recovery", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerSettingsPage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: "normal", VisibleText: "normal"}},
		{StepID: "stale_recovery", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerSettingsPage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: conversationalCustomerPriority, VisibleText: conversationalCustomerPriority}},
		{StepID: "correction", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: conversationalCustomerPriority, VisibleText: conversationalCustomerLabel + "/" + conversationalCustomerTheme}},
		{StepID: "correction", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerCorrected, Theme: conversationalCustomerTheme, Priority: conversationalCustomerPriority, VisibleText: conversationalCustomerCorrected + "/" + conversationalCustomerTheme}},
		{Phase: browserconversation.BrowserConversationOraclePostSession, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerCorrected, Theme: conversationalCustomerTheme, Priority: "normal", VisibleText: conversationalCustomerCorrected + "/" + conversationalCustomerTheme}},
	}
}

func conversationalCustomerSessionLog(input, response string) conversationalCustomerSessionLogEntry {
	var entry conversationalCustomerSessionLogEntry
	entry.Input.Text = input
	entry.Response.Text = response
	entry.Response.Complete = true
	return entry
}

func browserEventTool(ref webmcp.ToolRef, name string, generation uint64) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{Ref: ref, Name: name, Generation: generation, BrowserID: "browser-a", TargetID: "target-a", FrameID: "frame-a"}
}

func browserEventInvocation(tool webmcp.ToolDescriptor, invocation webmcp.InvocationID, input string) webmcp.BrowserEvent {
	return webmcp.BrowserEvent{Type: webmcp.EventToolInvoked, Generation: tool.Generation, ToolName: tool.Name, InvocationID: invocation, Input: json.RawMessage(input)}
}

func browserEventTerminal(invocation webmcp.InvocationID, status string, output json.RawMessage, generation uint64) webmcp.BrowserEvent {
	return webmcp.BrowserEvent{Type: webmcp.EventToolResponded, Generation: generation, InvocationID: invocation, Status: status, Output: output}
}

func testTranscriptTime() (timestamp time.Time) {
	return time.Unix(0, 0).UTC()
}

func buildConversationalCustomerResult(
	scenario browserconversation.BrowserConversationScenario,
	events []webmcp.BrowserEvent,
	logPath string,
	navigations []conversationalCustomerNavigationObservation,
	oracles []conversationalCustomerOracleObservation,
	browserID string,
	targetID webmcp.TargetID,
	probe conversationalCustomerProbe,
	pending webmcp.BrowserEvent,
	cancel conversationalCustomerCancelResult,
) (browserconversation.BrowserConversationResult, error) {
	providerCalls, err := readConversationalCustomerProviderCalls(filepath.Join(filepath.Dir(logPath), "agent.transcript.jsonl"))
	if err != nil {
		return browserconversation.BrowserConversationResult{}, fmt.Errorf("read agent transcript: %w", err)
	}
	logs, err := readConversationalCustomerSessionLog(logPath)
	if err != nil {
		return browserconversation.BrowserConversationResult{}, fmt.Errorf("read session log: %w", err)
	}
	turns := conversationalCustomerTurns(scenario, logs)
	builder := newConversationalCustomerCallBuilder(scenario, events, navigations)
	for _, providerCall := range providerCalls {
		builder.addProviderCall(providerCall)
	}
	builder.addPendingFallback(pending)
	builder.addExternalCancel(cancel)
	for _, step := range scenario.Steps {
		if step.Navigation != nil {
			builder.appendNavigation(step.ID)
		}
	}
	calls := builder.calls
	assignConversationalCustomerTurnSequences(turns, calls)

	lifecycle := browserconversation.BrowserConversationLifecycleEvidence{Outcome: browserconversation.BrowserConversationLifecycleCanceled, SessionStarted: true, SessionTerminated: true, Detached: true, DetachCount: 1, DetachRequired: true, ExternalBrowserID: webmcp.BrowserID(browserID), ExternalTargetID: targetID, ExternalTabAlive: probe.Alive, ExternalTabResponsive: probe.Responsive, ExternalTabAllowsMutation: probe.AllowsMutation, ExternalTabRead: probe.ReadSucceeded, ExternalTabMutation: probe.MutationSucceeded}
	result := browserconversation.BrowserConversationResult{ScenarioID: scenario.ID, ScenarioName: scenario.Name, Finalized: true, Turns: turns, BrokerCalls: calls, Oracles: conversationalCustomerOracleSnapshots(oracles), Cancellation: browserconversation.BrowserConversationCancellationEvidence{Interrupted: true, Requested: true, InvocationID: pending.InvocationID, FinalState: webmcp.InvocationCanceled, Reason: "customer stop", InterruptedStepID: "interrupt", CancelStepID: "cancel", OverlappingAudioSent: true, ExplicitCancelAudioSent: true}, Lifecycle: lifecycle}
	result.Corrections = browserconversationWire.NewService().DeriveCorrections(scenario, result)
	result.Recovery = browserconversationWire.NewService().DeriveRecovery(scenario, result)
	result.InputJSONValidity = browserconversationWire.NewService().ComputeInputJSONValidity(result.BrokerCalls)
	return result, browserconversationWire.NewService().ValidateResult(result)
}

func conversationalCustomerTurns(scenario browserconversation.BrowserConversationScenario, logs []conversationalCustomerSessionLogEntry) []browserconversation.BrowserConversationTurn {
	turns := make([]browserconversation.BrowserConversationTurn, 0, len(logs)*2)
	logStepIDs := conversationalCustomerLogStepIDs(scenario, logs)
	for index, entry := range logs {
		stepID := logStepIDs[index]
		if strings.TrimSpace(entry.Input.Text) != "" {
			turns = append(turns, browserconversation.BrowserConversationTurn{StepID: stepID, Direction: browserconversation.BrowserConversationCustomerTurn, ExpectedText: expectedStepTextForStep(scenario, stepID), ObservedText: entry.Input.Text, Complete: entry.Response.Complete})
		}
		if strings.TrimSpace(entry.Response.Text) != "" {
			turns = append(turns, browserconversation.BrowserConversationTurn{StepID: stepID, Direction: browserconversation.BrowserConversationAssistantTurn, ObservedText: entry.Response.Text, Complete: entry.Response.Complete})
		}
	}
	return turns
}

func conversationalCustomerOracleSnapshots(oracles []conversationalCustomerOracleObservation) []browserconversation.BrowserConversationOracleSnapshot {
	var oracleSnapshots []browserconversation.BrowserConversationOracleSnapshot
	for index, observation := range oracles {
		oracleSnapshots = append(oracleSnapshots, browserconversation.BrowserConversationOracleSnapshot{Sequence: uint64(index + 1), StepID: observation.StepID, PageID: observation.Oracle.Page, Generation: 0, Phase: observation.Phase, State: conversationalCustomerOracleState(observation.Oracle)})
	}
	return oracleSnapshots
}

// conversationalCustomerCallBuilder joins provider tool calls with the
// independently observed browser events into ordered broker call evidence.
type conversationalCustomerCallBuilder struct {
	scenario             browserconversation.BrowserConversationScenario
	events               []webmcp.BrowserEvent
	toolNames            map[webmcp.ToolRef]string
	toolGenerations      map[webmcp.ToolRef]uint64
	toolRefsByGeneration map[uint64]map[webmcp.ToolRef]struct{}
	firstGeneration      uint64
	terminalByInvocation map[webmcp.InvocationID]webmcp.BrowserEvent
	matchedInvocation    map[webmcp.InvocationID]bool
	navigationByStep     map[string]webmcp.BrowserEvent
	navigationAdded      map[string]bool
	calls                []browserconversation.BrowserConversationBrokerCall
	lastStep             string
	labelCount           int
	themeCount           int
	priorityCount        int
}

func newConversationalCustomerCallBuilder(scenario browserconversation.BrowserConversationScenario, events []webmcp.BrowserEvent, navigations []conversationalCustomerNavigationObservation) *conversationalCustomerCallBuilder {
	builder := &conversationalCustomerCallBuilder{
		scenario:             scenario,
		events:               events,
		toolNames:            make(map[webmcp.ToolRef]string),
		toolGenerations:      make(map[webmcp.ToolRef]uint64),
		toolRefsByGeneration: make(map[uint64]map[webmcp.ToolRef]struct{}),
		terminalByInvocation: make(map[webmcp.InvocationID]webmcp.BrowserEvent),
		matchedInvocation:    make(map[webmcp.InvocationID]bool),
		navigationByStep:     make(map[string]webmcp.BrowserEvent),
		navigationAdded:      make(map[string]bool),
		lastStep:             "initial_action",
	}
	for _, event := range events {
		for _, tool := range event.Tools {
			builder.indexTool(tool)
		}
	}
	for _, event := range events {
		if event.Type == webmcp.EventToolResponded && event.InvocationID != "" {
			builder.terminalByInvocation[event.InvocationID] = event
		}
	}
	for _, navigation := range navigations {
		builder.navigationByStep[navigation.StepID] = navigation.Event
	}
	return builder
}

func (b *conversationalCustomerCallBuilder) indexTool(tool webmcp.ToolDescriptor) {
	b.toolNames[tool.Ref] = tool.Name
	b.toolGenerations[tool.Ref] = tool.Generation
	if tool.Generation == 0 {
		return
	}
	refs := b.toolRefsByGeneration[tool.Generation]
	if refs == nil {
		refs = make(map[webmcp.ToolRef]struct{})
		b.toolRefsByGeneration[tool.Generation] = refs
	}
	refs[tool.Ref] = struct{}{}
	if b.firstGeneration == 0 || tool.Generation < b.firstGeneration {
		b.firstGeneration = tool.Generation
	}
}

func (b *conversationalCustomerCallBuilder) appendCall(call browserconversation.BrowserConversationBrokerCall) {
	call.Sequence = uint64(len(b.calls) + 1)
	b.calls = append(b.calls, call)
}

func (b *conversationalCustomerCallBuilder) appendNavigation(stepID string) {
	if b.navigationAdded[stepID] {
		return
	}
	event, ok := b.navigationByStep[stepID]
	if !ok {
		return
	}
	b.navigationAdded[stepID] = true
	input := json.RawMessage(`{}`)
	for _, step := range b.scenario.Steps {
		if step.ID == stepID && step.Navigation != nil {
			if encoded, err := json.Marshal(step.Navigation); err == nil {
				input = encoded
			}
			break
		}
	}
	b.appendCall(browserconversation.BrowserConversationBrokerCall{
		StepID: stepID, Operation: browserconversation.BrowserConversationCustomerNavigate,
		InputJSON: string(input), Generation: event.Generation,
		PreviousGeneration: event.PreviousGeneration,
	})
}

func (b *conversationalCustomerCallBuilder) addProviderCall(providerCall conversationalCustomerProviderCall) {
	switch providerCall.Name {
	case webmcp.ListToolsToolName:
		b.addListTools(providerCall)
	case webmcp.CancelToolName:
		b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: "cancel", Operation: browserconversation.BrowserConversationCancel, InputJSON: providerCall.Arguments, State: webmcp.InvocationCanceled, Terminal: true})
	case webmcp.InvokeToolName:
		b.addInvoke(providerCall)
	}
}

func (b *conversationalCustomerCallBuilder) addListTools(providerCall conversationalCustomerProviderCall) {
	var stepID string
	switch {
	case b.labelCount == 0:
		stepID = "initial_action"
	case b.themeCount == 0:
		stepID = "second_action"
	case b.priorityCount >= 2:
		stepID = "correction"
	default:
		stepID = "stale_recovery"
	}
	b.appendNavigation(stepID)
	refs, generation := conversationalCustomerCurrentToolRefs(b.toolNames, b.toolRefsByGeneration, stepID, b.navigationByStep, b.firstGeneration)
	b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: stepID, Operation: browserconversation.BrowserConversationListTools, InputJSON: providerCall.Arguments, Generation: generation, ToolRefs: refs})
	b.lastStep = stepID
}

// invokeStep classifies an invoke by tool name and advances the per-tool
// counters that later list-tools calls use to infer their step.
func (b *conversationalCustomerCallBuilder) invokeStep(toolName string) string {
	switch toolName {
	case "webmcp_customer_set_label":
		b.labelCount++
		if b.labelCount == 1 {
			return "initial_action"
		}
		return "correction"
	case "webmcp_customer_set_theme":
		b.themeCount++
		return "second_action"
	case "webmcp_customer_set_priority":
		b.priorityCount++
		return "stale_recovery"
	case "webmcp_customer_pending":
		return "interrupt"
	}
	return b.lastStep
}

func (b *conversationalCustomerCallBuilder) addInvoke(providerCall conversationalCustomerProviderCall) {
	toolName := providerCall.ToolName
	if toolName == "" {
		toolName = b.toolNames[providerCall.ToolRef]
	}
	stepID := b.invokeStep(toolName)
	b.appendNavigation(stepID)
	b.lastStep = stepID
	generation := b.toolGenerations[providerCall.ToolRef]
	b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: stepID, Operation: browserconversation.BrowserConversationInvoke, ToolRef: providerCall.ToolRef, ToolName: toolName, InputJSON: providerCall.InputJSON, State: webmcp.InvocationDispatched, Terminal: false, Generation: generation})
	if b.addMatchedTerminal(providerCall, toolName, stepID) {
		return
	}
	if toolName != "webmcp_customer_set_priority" || generation == 0 {
		return
	}
	if navigationEvent, ok := b.navigationByStep["stale_recovery"]; ok && generation <= navigationEvent.PreviousGeneration {
		b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: stepID, Operation: browserconversation.BrowserConversationInvoke, ToolRef: providerCall.ToolRef, ToolName: toolName, InputJSON: providerCall.InputJSON, State: webmcp.InvocationError, Terminal: true, ErrorCode: string(webmcp.ErrorStaleToolRef), Generation: generation})
	}
}

// addMatchedTerminal claims the first unmatched browser invocation for the
// provider call and records its terminal response when one was observed.
func (b *conversationalCustomerCallBuilder) addMatchedTerminal(providerCall conversationalCustomerProviderCall, toolName, stepID string) bool {
	for _, event := range b.events {
		if !b.invocationMatches(event, providerCall, toolName) {
			continue
		}
		b.matchedInvocation[event.InvocationID] = true
		terminal, ok := b.terminalByInvocation[event.InvocationID]
		if !ok {
			continue
		}
		b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: stepID, Operation: browserconversation.BrowserConversationInvoke, ToolRef: providerCall.ToolRef, ToolName: toolName, InvocationID: event.InvocationID, InputJSON: providerCall.InputJSON, State: conversationalCustomerInvocationState(terminal), Terminal: true, Output: conversationalCustomerJSON(terminal.Output), ErrorCode: terminal.ErrorCode, Generation: terminal.Generation, PreviousGeneration: terminal.PreviousGeneration})
		return true
	}
	return false
}

func (b *conversationalCustomerCallBuilder) invocationMatches(event webmcp.BrowserEvent, providerCall conversationalCustomerProviderCall, toolName string) bool {
	if event.Type != webmcp.EventToolInvoked || event.InvocationID == "" || b.matchedInvocation[event.InvocationID] || event.ToolName != toolName {
		return false
	}
	providerName := b.toolNames[providerCall.ToolRef]
	if providerCall.ToolRef != "" && event.ToolName != "" && providerName != "" && event.ToolName != providerName {
		return false
	}
	providerGeneration := b.toolGenerations[providerCall.ToolRef]
	return providerGeneration == 0 || event.Generation == 0 || providerGeneration == event.Generation
}

func (b *conversationalCustomerCallBuilder) addPendingFallback(pending webmcp.BrowserEvent) {
	if pending.InvocationID == "" {
		return
	}
	for _, call := range b.calls {
		if call.InvocationID == pending.InvocationID {
			return
		}
	}
	if terminal, ok := b.terminalByInvocation[pending.InvocationID]; ok {
		b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: "interrupt", Operation: browserconversation.BrowserConversationInvoke, ToolName: pending.ToolName, InvocationID: pending.InvocationID, InputJSON: string(pending.Input), State: conversationalCustomerInvocationState(terminal), Terminal: true, ErrorCode: terminal.ErrorCode, Generation: terminal.Generation})
	}
}

func (b *conversationalCustomerCallBuilder) addExternalCancel(cancel conversationalCustomerCancelResult) {
	if cancel.InvocationID == "" {
		return
	}
	// Marshal cannot fail for a single string field; a nil input would still
	// be reported as invalid JSON by the evidence validator.
	cancelInput, err := json.Marshal(struct {
		InvocationID string `json:"invocation_id"`
	}{InvocationID: cancel.InvocationID})
	if err != nil {
		cancelInput = nil
	}
	b.appendCall(browserconversation.BrowserConversationBrokerCall{StepID: "cancel", Operation: browserconversation.BrowserConversationCancel, InvocationID: webmcp.InvocationID(cancel.InvocationID), InputJSON: string(cancelInput), State: webmcp.InvocationCanceled, Terminal: true})
}
