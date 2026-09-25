package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
)

// Fixture values shared by the direct invoke and cancel command tests.
const (
	testBrowserInvocationID  = "browser-invocation-9"
	testDirectInvocationID   = "inv-23"
	testCanceledStatus       = "canceled"
	testPendingStatus        = "pending"
	testCDPURL               = "http://127.0.0.1:9222"
	go2rtcFixtureWSPath      = "/api/ws"
	v9WebRTCDeviceScenarioID = "s2s-v9-webrtc-device-roundtrip"
	testConfirmedOutcome     = "confirmed_canceled"
	testTerminalPhase        = "terminal"
)

func TestWebMCPDirectInvokeReceiptUsesBrowserIDAndOnlyHandoffFields(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
		invokeResult: webmcp.InvokeResult{
			InvocationID:        "broker-invocation-1",
			BrowserInvocationID: testBrowserInvocationID,
			State:               webmcp.InvocationCompleted,
			Output:              json.RawMessage(`{"page_output":"do-not-put-in-receipt"}`),
		},
	}

	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker),
		"invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref),
		"--input-json", `{"input_secret":"do-not-put-in-receipt"}`, "--json")
	if result.err != nil {
		t.Fatalf("invoke: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	if len(result.stderr) > operations.ReceiptMaxBytes {
		t.Fatalf("dispatch receipt is %d bytes, want <= %d: %q", len(result.stderr), operations.ReceiptMaxBytes, result.stderr)
	}
	decoder := json.NewDecoder(strings.NewReader(result.stderr))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		t.Fatalf("decode dispatch receipt: %v; stderr=%q", err, result.stderr)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("dispatch receipt has more than one JSON value: err=%v extra=%#v", err, extra)
	}
	wantFields := map[string]struct{}{"version": {}, "invocation_id": {}, "tool_ref": {}, "state": {}}
	if len(fields) != len(wantFields) {
		t.Fatalf("dispatch receipt fields = %#v, want exactly %#v", fields, wantFields)
	}
	for field := range wantFields {
		if _, ok := fields[field]; !ok {
			t.Fatalf("dispatch receipt omitted %q: %#v", field, fields)
		}
	}
	var receipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(result.stderr), &receipt); err != nil {
		t.Fatalf("decode typed dispatch receipt: %v", err)
	}
	if receipt.Version != operations.ReceiptVersion || receipt.InvocationID != testBrowserInvocationID || receipt.ToolRef != string(tool.Ref) || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("dispatch receipt = %+v", receipt)
	}
	for _, secret := range []string{"broker-invocation-1", "input_secret", "do-not-put-in-receipt", "page_output", "127.0.0.1", "password", "fragment"} {
		if strings.Contains(result.stderr, secret) {
			t.Fatalf("dispatch receipt exposed %q: %q", secret, result.stderr)
		}
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectInvocation
	decodeDirectData(t, envelope.Data, &data)
	if data.InvocationID != testBrowserInvocationID {
		t.Fatalf("final invocation ID = %q, want browser protocol ID", data.InvocationID)
	}
}

func TestWebMCPDirectHumanCancellationReportsIDAndUnknownSideEffect(t *testing.T) {
	var output bytes.Buffer
	err := writeWebMCPDirectHuman(&output, "invoke", nil, webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), map[string]any{
		"invocation_id":       testBrowserInvocationID,
		"cancel_source":       "interrupt",
		"side_effect_unknown": true,
	}), webmcp.ErrorInvocationFailed)
	if err != nil {
		t.Fatalf("human cancellation output: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"Error: invocation_canceled",
		"invocation_id=browser-invocation-9",
		"cancel_source=interrupt",
		"side_effect_unknown=true",
		"rollback and retry safety are unknown",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("human cancellation output omitted %q: %q", want, got)
		}
	}
}

func TestWebMCPDirectCancelRehydratesExactSelectionWithoutLocalRegistry(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	if err := store.Save(WebMCPSelection{
		Version:          WebMCPSelectionVersion,
		EndpointID:       string(candidate.ID),
		BrowserID:        string(candidate.ID),
		TargetID:         string(target.ID),
		Origin:           target.Origin,
		ContinuityMarker: target.ContinuityMarker,
		Generation:       page.Generation,
	}); err != nil {
		t.Fatalf("seed persisted selection: %v", err)
	}
	base := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	broker := &directCancelCommandBroker{directCommandBroker: base}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "cancel", "--invocation", testBrowserInvocationID, "--json")
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectCancelData
	decodeDirectData(t, envelope.Data, &data)
	if data.InvocationID != testBrowserInvocationID || data.Status != testCanceledStatus || data.Phase != testTerminalPhase || data.Outcome != testConfirmedOutcome {
		t.Fatalf("cancel data = %+v", data)
	}
	if got := broker.directCancelRequest; got.Target != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) || got.InvocationID != testBrowserInvocationID {
		t.Fatalf("direct cancel request = %+v", got)
	}
	if base.cancelRequest.InvocationID != "" {
		t.Fatalf("fresh direct cancel consulted local broker registry: %+v", base.cancelRequest)
	}
	if len(base.selectCalls) != 1 || base.selectCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("exact selection calls = %+v", base.selectCalls)
	}
}

func TestWebMCPDirectCancelRejectsConvenientFallbackTarget(t *testing.T) {
	configDir := writeDirectConfig(t, "  selection:\n    auto_select: single\n")
	page, target, candidate, _ := directFixture()
	base := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	broker := &directCancelCommandBroker{directCommandBroker: base}

	result := executeDirectCommand(t, configDir, nil, directFactory(broker), "cancel", "--browser", "browser-a", "--invocation", testBrowserInvocationID, "--json")
	if result.err == nil {
		t.Fatal("cancel unexpectedly selected a convenient fallback target")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("fallback cancellation envelope = %+v", envelope)
	}
	if len(base.selectCalls) != 0 || broker.directCancelRequest.InvocationID != "" {
		t.Fatalf("fallback cancellation touched target/cancel path: selections=%+v request=%+v", base.selectCalls, broker.directCancelRequest)
	}
}

func TestWebMCPDirectCancelClassifiesBrowserRejection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	if err := store.Save(WebMCPSelection{
		Version:    WebMCPSelectionVersion,
		EndpointID: string(candidate.ID),
		BrowserID:  string(candidate.ID),
		TargetID:   string(target.ID),
		Origin:     target.Origin,
	}); err != nil {
		t.Fatalf("seed persisted selection: %v", err)
	}
	base := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	broker := &directCancelCommandBroker{
		directCommandBroker: base,
		directCancelErr:     errors.New("browser response leaked credential=secret"),
	}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "cancel", "--invocation", testBrowserInvocationID, "--json")
	if result.err == nil {
		t.Fatal("browser rejection unexpectedly succeeded")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationFailed) {
		t.Fatalf("browser rejection envelope = %+v", envelope)
	}
	if strings.Contains(result.stdout, "credential=secret") || strings.Contains(result.stderr, "credential=secret") {
		t.Fatalf("browser rejection leaked raw error: stdout=%q stderr=%q", result.stdout, result.stderr)
	}
}

func TestWebMCPDirectHumanOutputIsStableAndRedacted(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
	}
	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker), "browsers")
	if result.err != nil {
		t.Fatalf("browsers: %v", result.err)
	}
	want := "Browsers:\n  browser-a  Chrome/Test  source=explicit scope=loopback endpoint=http://127.0.0.1:9222/json/version\n"
	if result.stdout != want {
		t.Fatalf("human output = %q, want %q", result.stdout, want)
	}
	if strings.Contains(result.stdout, "secret") || strings.Contains(result.stdout, "token=") {
		t.Fatalf("human output exposed endpoint secret: %q", result.stdout)
	}
}

func TestWebMCPDirectWatchReportsTerminationAndCancellation(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	closedBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		watch:      closedEventChannel(),
	}
	ended := executeDirectCommand(t, configDir, store, directFactory(closedBroker), "watch", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	envelope := requireDirectSuccess(t, ended)
	var endedData WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &endedData)
	if endedData.Status != operations.WatchStatusEnded || len(endedData.Events) != 0 {
		t.Fatalf("terminated watch = %+v", endedData)
	}

	blockedBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		watch:      make(chan webmcp.BrokerEvent),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := executeDirectCommandContext(t, ctx, configDir, store, directFactory(blockedBroker), "watch", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if canceled.err != nil {
		t.Fatalf("canceled watch: %v", canceled.err)
	}
	envelope = decodeDirectEnvelope(t, canceled.stdout)
	var canceledData WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &canceledData)
	if canceledData.Status != operations.WatchStatusCanceled {
		t.Fatalf("canceled watch = %+v", canceledData)
	}
}

func TestWebMCPDirectDefaultRuntimeReturnsClassifiedDiscoveryError(t *testing.T) {
	configDir := t.TempDir()
	store := NewFileWebMCPSelectionStore(configDir)
	result := executeDirectCommand(t, configDir, store, nil, "browsers", "--json")
	if result.err == nil {
		t.Fatal("default operation unexpectedly succeeded without a browser endpoint")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorEndpointNotFound) {
		t.Fatalf("default envelope = %+v, want endpoint_not_found", envelope)
	}
	if strings.Contains(result.stdout, "Lane B") || strings.Contains(result.stdout, "Lane D") {
		t.Fatalf("default operation output exposed internal implementation names: %s", result.stdout)
	}
}

func TestWebMCPDirectWatchReportsBoundedFailure(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	stream := make(chan webmcp.BrokerEvent, 1)
	stream <- webmcp.BrokerEvent{
		Version:   webmcp.BrowserEventsVersion,
		Type:      webmcp.BrokerEventSessionClosed,
		Sequence:  2,
		BrowserID: candidate.ID,
		TargetID:  target.ID,
		Reason:    webmcp.BrokerWatchBufferFullReason,
	}
	close(stream)
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		watch:      stream,
	}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "watch", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if result.err != nil {
		t.Fatalf("bounded watch failure: %v", result.err)
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &data)
	if data.Status != operations.WatchStatusFailed || len(data.Events) != 1 || data.Events[0].Type != string(webmcp.BrokerEventSessionClosed) || data.Events[0].Reason != webmcp.BrokerWatchBufferFullReason {
		t.Fatalf("bounded watch result = %+v, want explicit failed status", data)
	}
}

func TestWebMCPDirectToolsWatchSubscribesBeforeSelection(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	stream := make(chan webmcp.BrokerEvent, 2)
	broker := &selectionOrderingWatchBroker{
		directCommandBroker: &directCommandBroker{
			candidates: []webmcp.BrowserCandidate{candidate},
			targets:    []webmcp.Target{target},
			selected:   page,
		},
		stream: stream,
	}

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "tools", "--browser", string(candidate.ID), "--tab", string(target.ID), "--watch", "--json")
	if result.err != nil {
		t.Fatalf("tools --watch: %v\nstdout=%s", result.err, result.stdout)
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectWatchData
	decodeDirectData(t, envelope.Data, &data)
	if data.Status != operations.WatchStatusEnded || len(data.Events) != 2 || data.Events[0].Type != string(webmcp.BrokerEventSelected) || data.Events[1].Type != string(webmcp.BrokerEventCatalogChanged) {
		t.Fatalf("tools --watch result = %+v, want selection and initial catalog events", data)
	}
}

func TestWebMCPDirectPreservesExternallyOwnedTarget(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, tool := directFixture()
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool)),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
		Ownership:  webmcp.TargetOwnershipExternal,
	})
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "select", "--browser", "browser-a", "--tab", "tab-a", "--json")
	if result.err != nil {
		t.Fatalf("select through real broker: %v\nstdout=%s", result.err, result.stdout)
	}
	ops := runtime.Operations()
	if !hasTestkitOperation(ops, testkit.OperationDetach) {
		t.Fatalf("external target was not detached: %+v", ops)
	}
	if hasTestkitOperation(ops, testkit.OperationCloseTarget) {
		t.Fatalf("external target was closed: %+v", ops)
	}
}

func TestWebMCPDirectActivateClassifiesLiveOperationFailure(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	_, target, candidate, _ := directFixture()
	runtime := testkit.NewScriptedBrowserRuntime(testkit.BrowserConfig{
		Candidate:     candidate,
		ActivateError: errors.New("foreground activation rejected by headless Chrome"),
		Targets: []testkit.TargetConfig{
			testkit.NewTargetConfig(target),
		},
	})
	browser := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})

	result := executeDirectCommand(t, configDir, nil, directFactory(browser), "activate", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if result.err == nil {
		t.Fatal("activate unexpectedly succeeded")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorTargetAttachFailed) {
		t.Fatalf("live activation failure envelope = %+v, error = %v", envelope, result.err)
	}
	if envelope.Error.Details["browser_id"] != string(candidate.ID) || envelope.Error.Details["target_id"] != string(target.ID) || envelope.Error.Details["phase"] != "activate" {
		t.Fatalf("live activation failure details = %#v, want exact activation identity", envelope.Error.Details)
	}
	if _, exists := envelope.Error.Details["reconnect_required"]; exists {
		t.Fatalf("live activation failure requested reconnect: %#v", envelope.Error.Details)
	}
	for _, operation := range runtime.Operations() {
		if operation.Kind == testkit.OperationAttach || operation.Kind == testkit.OperationEnableWebMCP || operation.Kind == testkit.OperationEnableAcknowledged {
			t.Fatalf("activation-only command initialized WebMCP: %#v", runtime.Operations())
		}
	}
}

func TestWebMCPDirectClassifiesBrokerFailures(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	_, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}},
		catalog:    webmcp.ToolCatalogSnapshot{Context: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID}}, Tools: []webmcp.ToolDescriptor{tool}},
		invokeErr:  webmcp.NewClassifiedError(webmcp.ErrorStaleToolRef, "tool ref is stale", map[string]any{"tool_ref": string(tool.Ref)}),
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref), "--input-json", `{}`, "--json")
	if result.err == nil {
		t.Fatal("stale invocation unexpectedly succeeded")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleToolRef) {
		t.Fatalf("stale invocation envelope = %+v", envelope)
	}
}

func TestWebMCPDirectClassifiesPersistedBrowserLossAsDisconnected(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, _ := directFixture()
	selected := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	if result := executeDirectCommand(t, configDir, store, directFactory(selected), "select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json"); result.err != nil {
		t.Fatalf("seed persisted selection: %v\nstdout=%s", result.err, result.stdout)
	}

	lost := &directCommandBroker{
		discoverErr: webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "browser endpoint could not be reached", map[string]any{
			"phase": "discovery",
		}),
	}
	result := executeDirectCommand(t, configDir, store, directFactory(lost), "context", "--json")
	if result.err == nil {
		t.Fatal("context unexpectedly succeeded after the persisted browser disappeared")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("disconnected context envelope = %+v", envelope)
	}
	if envelope.Error.Details["browser_id"] != string(candidate.ID) || envelope.Error.Details["target_id"] != string(target.ID) || envelope.Error.Details["phase"] != "discovery" || envelope.Error.Details["reconnect_required"] != true {
		t.Fatalf("disconnected context details = %#v", envelope.Error.Details)
	}
}

// directOperationCase is one direct command and the result it must produce
// from the shared fixture broker.
type directOperationCase struct {
	name  string
	args  []string
	check func(*testing.T, directCommandResult, *directCommandBroker)
}

func directOperationCases(tool webmcp.ToolDescriptor) []directOperationCase {
	return append(directDiscoveryOperationCases(), directTargetOperationCases(tool)...)
}

func directDiscoveryOperationCases() []directOperationCase {
	return []directOperationCase{
		{name: "browsers", args: []string{"browsers", "--json"}, check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
			var data WebMCPDirectBrowsersData
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if len(data.Browsers) != 1 || data.Browsers[0].ID != "browser-a" || strings.Contains(result.stdout, "secret") {
				t.Fatalf("browsers result = %+v output=%s", data, result.stdout)
			}
		}},
		{name: "tabs", args: []string{"tabs", "--browser", "browser-a", "--eligible", "--json"}, check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
			var data WebMCPDirectTabsData
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if len(data.Tabs) != 1 || data.Tabs[0].TargetID != "tab-a" || data.Tabs[0].Origin != "https://fixture.test" {
				t.Fatalf("tabs result = %+v", data)
			}
		}},
	}
}

func directTargetOperationCases(tool webmcp.ToolDescriptor) []directOperationCase {
	return []directOperationCase{
		{name: "activate", args: []string{"activate", "--browser", "browser-a", "--tab", "tab-a", "--json"}, check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
			requireDirectSuccess(t, result)
			if len(broker.activateCalls) != 1 || broker.activateCalls[0].TargetID != "tab-a" {
				t.Fatalf("activate calls = %+v", broker.activateCalls)
			}
		}},
		{name: "context", args: []string{"context", "--browser", "browser-a", "--tab", "tab-a", "--json"}, check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
			var data WebMCPDirectContext
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if data.Generation != 7 || data.CatalogGeneration != 7 || data.ToolCount != 1 || data.URL != "https://fixture.test/page" {
				t.Fatalf("context result = %+v", data)
			}
		}},
		{name: "tools", args: []string{"tools", "--browser", "browser-a", "--tab", "tab-a", "--json"}, check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
			var data WebMCPDirectToolsData
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if len(data.Tools) != 1 || data.Tools[0].Ref != string(tool.Ref) || data.Tools[0].Generation != 7 {
				t.Fatalf("tools result = %+v", data)
			}
		}},
		{name: "invoke", args: []string{"invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref), "--input-json", `{"value":1}`, "--reason", "test reason", "--json"}, check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
			var data WebMCPDirectInvocation
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if data.InvocationID != testDirectInvocationID || data.ToolRef != string(tool.Ref) || data.Status != string(webmcp.InvocationCompleted) {
				t.Fatalf("invoke result = %+v", data)
			}
			if broker.invokeRequest.ToolRef != tool.Ref || string(broker.invokeRequest.Input) != `{"value":1}` || broker.invokeRequest.Reason != "test reason" {
				t.Fatalf("invoke request = %+v", broker.invokeRequest)
			}
			requireSingleDispatchReceipt(t, result.stderr, testDirectInvocationID, tool.Ref)
		}},
		{name: "cancel", args: []string{"cancel", testDirectInvocationID, "--browser", "browser-a", "--tab", "tab-a", "--json"}, check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
			var data WebMCPDirectCancelData
			decodeDirectData(t, requireDirectSuccess(t, result).Data, &data)
			if data.InvocationID != testDirectInvocationID || broker.cancelRequest.InvocationID != testDirectInvocationID {
				t.Fatalf("cancel result/request = %+v/%+v", data, broker.cancelRequest)
			}
		}},
	}
}

// requireSingleDispatchReceipt checks that stderr holds exactly one bounded
// dispatch receipt naming the invocation and tool.
func requireSingleDispatchReceipt(t *testing.T, stderr, invocationID string, toolRef webmcp.ToolRef) {
	t.Helper()
	var receipt WebMCPDirectInvocationReceipt
	decoder := json.NewDecoder(strings.NewReader(stderr))
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode dispatch receipt: %v; stderr=%q", err, stderr)
	}
	if receipt.Version != operations.ReceiptVersion || receipt.InvocationID != invocationID || receipt.ToolRef != string(toolRef) || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("dispatch receipt = %+v", receipt)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("dispatch stderr contains more than one receipt: err=%v extra=%#v", err, extra)
	}
}

func TestWebMCPDirectOperationsUseBrokerIDsRefsAndInvocations(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	for _, test := range directOperationCases(tool) {
		t.Run(test.name, func(t *testing.T) {
			broker := &directCommandBroker{
				candidates:   []webmcp.BrowserCandidate{candidate},
				targets:      []webmcp.Target{target},
				selected:     page,
				catalog:      webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
				invokeResult: webmcp.InvokeResult{InvocationID: testDirectInvocationID, State: webmcp.InvocationCompleted, Output: json.RawMessage(`{"ok":true}`)},
			}
			result := executeDirectCommand(t, configDir, store, directFactory(broker), test.args...)
			test.check(t, result, broker)
			if test.name != "invoke" && result.stderr != "" {
				t.Fatalf("stderr = %q", result.stderr)
			}
		})
	}
}
