package chrome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	looptranscript "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
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
	transcriptFile, err := os.OpenFile(filepath.Join(recordingDir, "agent.transcript.jsonl"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create provider transcript: %v", err)
	}
	for index, call := range providerCalls {
		payload, marshalErr := json.Marshal(map[string]any{
			"type":      "response.function_call_arguments.done",
			"call_id":   "provider-call-" + string(rune('a'+index)),
			"name":      call.name,
			"arguments": call.arguments,
		})
		if marshalErr != nil {
			_ = transcriptFile.Close()
			t.Fatalf("encode provider event: %v", marshalErr)
		}
		record, recordErr := looptranscript.Encode(looptranscript.NewRecord(uint64(index+1), testTranscriptTime(), looptranscript.PeerAgent, looptranscript.DirectionIn, looptranscript.StreamWebSocket, payload))
		if recordErr != nil {
			_ = transcriptFile.Close()
			t.Fatalf("encode provider record: %v", recordErr)
		}
		if _, writeErr := transcriptFile.Write(record); writeErr != nil {
			_ = transcriptFile.Close()
			t.Fatalf("write provider record: %v", writeErr)
		}
	}
	if err := transcriptFile.Close(); err != nil {
		t.Fatalf("close provider transcript: %v", err)
	}

	logs := []conversationalCustomerSessionLogEntry{
		conversationalCustomerSessionLog("Set the customer label to live alpha.", "Label is live alpha."),
		conversationalCustomerSessionLog("Now set the customer theme to live dark.", "Theme is live dark."),
		conversationalCustomerSessionLog("Set the customer priority to high.", "Priority is high."),
		conversationalCustomerSessionLog("Actually change the customer label to live corrected.", "Label is live corrected."),
		conversationalCustomerSessionLog("Hold this customer request while I decide.", "The request is pending."),
		conversationalCustomerSessionLog("Stop and cancel that request.", "The request was canceled."),
	}
	logFile, err := os.OpenFile(filepath.Join(recordingDir, "session-log.jsonl"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create session log: %v", err)
	}
	for _, entry := range logs {
		encoded, marshalErr := json.Marshal(entry)
		if marshalErr != nil {
			_ = logFile.Close()
			t.Fatalf("encode session log: %v", marshalErr)
		}
		if _, writeErr := logFile.Write(append(encoded, '\n')); writeErr != nil {
			_ = logFile.Close()
			t.Fatalf("write session log: %v", writeErr)
		}
	}
	if err := logFile.Close(); err != nil {
		t.Fatalf("close session log: %v", err)
	}

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
	oracles := []conversationalCustomerOracleObservation{
		{StepID: "initial_action", Phase: services.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: "unset", Theme: "default", Priority: "normal", VisibleText: "unset/default"}},
		{StepID: "initial_action", Phase: services.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: "default", Priority: "normal", VisibleText: conversationalCustomerLabel + "/default"}},
		{StepID: "second_action", Phase: services.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: "default", Priority: "normal", VisibleText: conversationalCustomerLabel + "/default"}},
		{StepID: "second_action", Phase: services.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: "normal", VisibleText: conversationalCustomerLabel + "/" + conversationalCustomerTheme}},
		{StepID: "stale_recovery", Phase: services.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerSettingsPage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: "normal", VisibleText: "normal"}},
		{StepID: "stale_recovery", Phase: services.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerSettingsPage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: conversationalCustomerPriority, VisibleText: conversationalCustomerPriority}},
		{StepID: "correction", Phase: services.BrowserConversationOracleBefore, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerLabel, Theme: conversationalCustomerTheme, Priority: conversationalCustomerPriority, VisibleText: conversationalCustomerLabel + "/" + conversationalCustomerTheme}},
		{StepID: "correction", Phase: services.BrowserConversationOracleAfter, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerCorrected, Theme: conversationalCustomerTheme, Priority: conversationalCustomerPriority, VisibleText: conversationalCustomerCorrected + "/" + conversationalCustomerTheme}},
		{Phase: services.BrowserConversationOraclePostSession, Oracle: conversationalCustomerOracle{Page: conversationalCustomerHomePage, Ready: true, Label: conversationalCustomerCorrected, Theme: conversationalCustomerTheme, Priority: "normal", VisibleText: conversationalCustomerCorrected + "/" + conversationalCustomerTheme}},
	}
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
	if result.Lifecycle.ExternalBrowserID != "browser-a" || result.Lifecycle.ExternalTargetID != "target-a" {
		t.Fatalf("lifecycle target identity = %+v, want probe identity", result.Lifecycle)
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
