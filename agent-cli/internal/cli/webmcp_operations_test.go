package cli

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/spf13/cobra"
)

func TestWebMCPDirectCommandTreeIsFrozen(t *testing.T) {
	command := NewWebMCPCommand(flags.NewGlobalFlags()).Generate()
	got := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		got = append(got, child.Name())
	}
	sort.Strings(got)
	want := []string{"activate", "browsers", "cancel", "context", "doctor", "invoke", "select", "tabs", "tools", "watch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WebMCP command names = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"launch", "browser"} {
		for _, name := range got {
			if name == forbidden {
				t.Fatalf("unexpected forbidden command %q", forbidden)
			}
		}
	}
}

func TestWebMCPWatchHelpDocumentsCrossProcessObservationBoundary(t *testing.T) {
	command := NewWebMCPCommand(flags.NewGlobalFlags()).Generate()
	watch, _, err := command.Find([]string{"watch"})
	if err != nil {
		t.Fatalf("find webmcp watch command: %v", err)
	}
	tools, _, err := command.Find([]string{"tools"})
	if err != nil {
		t.Fatalf("find webmcp tools command: %v", err)
	}

	for _, test := range []struct {
		name string
		text string
	}{
		{name: "watch", text: watch.Long},
		{name: "tools --watch", text: tools.Long},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, want := range []string{
				"toolsAdded/toolsRemoved -> catalog_changed",
				"toolInvoked             -> invocation_created",
				"toolResponded           -> invocation_terminal",
				"selected and session_closed are watcher-local lifecycle events",
				"broker admission, approval, and cancellation-request history remains",
				"process-local; no cross-process visibility",
				"failed session_closed event",
			} {
				if !strings.Contains(test.text, want) {
					t.Errorf("help text does not contain %q:\n%s", want, test.text)
				}
			}
		})
	}
}

func TestWebMCPDirectInvokeAndCancelHelpDocumentHandoff(t *testing.T) {
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "invoke", want: []string{"stderr", "invocation_id", "dispatched", "Stdout", "SIGINT"}},
		{name: "cancel", want: []string{"Two-process flow", "receipt", "exact", "falls back", "stdout"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations := NewWebMCPOperationsCommand(flags.NewGlobalFlags())
			root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
			operations.AddCommands(root)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs([]string{test.name, "--help"})
			if err := root.Execute(); err != nil {
				t.Fatalf("execute %s help: %v", test.name, err)
			}
			description := stdout.String() + stderr.String()
			for _, want := range test.want {
				if !strings.Contains(description, want) {
					t.Fatalf("help omitted %q:\n%s", want, description)
				}
			}
		})
	}
}

func TestWebMCPDirectSelectionPersistsRedactedOpaqueIDs(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "select", "--browser", string(candidate.ID), "--tab", string(target.ID), "--json")
	if result.err != nil {
		t.Fatalf("select: %v\nstdout=%s", result.err, result.stdout)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if !envelope.OK {
		t.Fatalf("select envelope = %+v", envelope)
	}
	var data WebMCPDirectContext
	decodeDirectData(t, envelope.Data, &data)
	if data.BrowserID != string(candidate.ID) || data.TargetID != string(target.ID) || data.ToolCount != 1 {
		t.Fatalf("select data = %+v", data)
	}
	if strings.Contains(result.stdout, "secret") || strings.Contains(result.stdout, "#fragment") {
		t.Fatalf("select output exposed URL material: %s", result.stdout)
	}
	selection, err := store.Load()
	if err != nil {
		t.Fatalf("load selection: %v", err)
	}
	if selection.Version != WebMCPSelectionVersion || selection.EndpointID != string(candidate.ID) || selection.BrowserID != string(candidate.ID) || selection.TargetID != string(target.ID) || selection.Origin != string(targetOrigin(target)) {
		t.Fatalf("persisted selection = %+v", selection)
	}
	if len(broker.selectCalls) != 1 || broker.selectCalls[0] != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) {
		t.Fatalf("select calls = %+v", broker.selectCalls)
	}
	if len(broker.activateCalls) != 0 {
		t.Fatalf("select unexpectedly activated target: %+v", broker.activateCalls)
	}
	if broker.closeCalls != 1 {
		t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
	}
}

func TestWebMCPDirectSeparateCommandsRejectStaleSelectionWithoutFallback(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	if err := store.Save(WebMCPSelection{
		Version:    WebMCPSelectionVersion,
		EndpointID: "browser-a",
		BrowserID:  "browser-a",
		TargetID:   "missing-tab",
		Origin:     "https://fixture.test",
		SelectedAt: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	page, _, candidate, _ := directFixture()
	otherTarget := webmcp.Target{BrowserID: candidate.ID, ID: "other-tab", Type: "page", Title: "Fallback must not be used", URL: "https://fixture.test/other", Origin: "https://fixture.test", Eligible: true}
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{otherTarget},
		selected:   page,
	}
	result := executeDirectCommand(t, configDir, store, directFactory(broker), "context", "--json")
	if result.err == nil {
		t.Fatal("context unexpectedly succeeded with stale selection")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("stale context envelope = %+v", envelope)
	}
	if len(broker.selectCalls) != 0 {
		t.Fatalf("stale selection fell back to another target: %+v", broker.selectCalls)
	}
}

func TestWebMCPDirectDefaultSelectionDoesNotChooseAConvenientTab(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	page, target, candidate, _ := directFixture()
	broker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	result := executeDirectCommand(t, configDir, NewFileWebMCPSelectionStore(configDir), directFactory(broker), "context", "--browser", "browser-a", "--json")
	if result.err == nil {
		t.Fatal("context unexpectedly auto-selected a tab with auto_select=off")
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("missing selection envelope = %+v", envelope)
	}
	if len(broker.selectCalls) != 0 {
		t.Fatalf("context selected a tab without an explicit selector: %+v", broker.selectCalls)
	}
}

func TestWebMCPDirectNoEligibleTabUsesC0DetailsInHumanAndJSONModes(t *testing.T) {
	browserID := randomizedWebMCPTestID(t, "browser-")
	targetID := randomizedWebMCPTestID(t, "target-")
	candidate := webmcp.BrowserCandidate{
		ID:       webmcp.BrowserID(browserID),
		Source:   webmcp.DiscoverySourceExplicit,
		Product:  "Chrome/Test",
		Protocol: "1.3",
		Loopback: true,
	}
	ineligible := webmcp.Target{
		BrowserID:         candidate.ID,
		ID:                webmcp.TargetID(targetID),
		Type:              "page",
		Title:             "Blank page",
		URL:               "about:blank",
		EligibilityReason: "internal_url",
	}

	for _, testCase := range []struct {
		name string
		json bool
	}{
		{name: "json", json: true},
		{name: "human"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			broker := &directCommandBroker{
				candidates: []webmcp.BrowserCandidate{candidate},
				targets:    []webmcp.Target{ineligible},
			}
			args := []string{"select", "--browser", browserID}
			if testCase.json {
				args = append(args, "--json")
			}
			result := executeDirectCommand(t, writeDirectConfig(t, ""), nil, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("select unexpectedly succeeded for an ineligible page")
			}
			if len(broker.selectCalls) != 0 {
				t.Fatalf("ineligible page was selected: %+v", broker.selectCalls)
			}

			if testCase.json {
				envelope := decodeDirectEnvelope(t, result.stdout)
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorNoEligibleTab) {
					t.Fatalf("no-eligible envelope = %+v", envelope)
				}
				if envelope.Error.Details["browser_id"] != browserID || envelope.Error.Details["candidate_count"] != float64(1) {
					t.Fatalf("no-eligible details = %#v", envelope.Error.Details)
				}
				filters, ok := envelope.Error.Details["filters"].(map[string]any)
				if !ok || filters["eligible_only"] != true || filters["include_zero_tool_pages"] != true {
					t.Fatalf("effective filters = %#v", envelope.Error.Details["filters"])
				}
			} else if !strings.Contains(result.stdout, "Error: no_eligible_tab") {
				t.Fatalf("human no-eligible output = %q", result.stdout)
			}
			if strings.Contains(result.stdout, "about:blank") || strings.Contains(result.stdout, targetID) {
				t.Fatalf("no-eligible output exposed page data: %q", result.stdout)
			}
			if broker.closeCalls != 1 {
				t.Fatalf("broker close calls = %d, want one", broker.closeCalls)
			}
		})
	}
}

func randomizedWebMCPTestID(t *testing.T, prefix string) string {
	t.Helper()
	value := make([]byte, 6)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatalf("randomize WebMCP test ID: %v", err)
	}
	return prefix + hex.EncodeToString(value)
}

func randomizedWebMCPInstanceID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatalf("randomize WebMCP instance ID: %v", err)
	}
	return "incarnation-" + hex.EncodeToString(value)
}

func TestWebMCPDirectOperationsUseBrokerIDsRefsAndInvocations(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	newBroker := func() *directCommandBroker {
		return &directCommandBroker{
			candidates: []webmcp.BrowserCandidate{candidate},
			targets:    []webmcp.Target{target},
			selected:   page,
			catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
			invokeResult: webmcp.InvokeResult{
				InvocationID: "inv-23",
				State:        webmcp.InvocationCompleted,
				Output:       json.RawMessage(`{"ok":true}`),
			},
		}
	}

	tests := []struct {
		name  string
		args  []string
		check func(*testing.T, directCommandResult, *directCommandBroker)
	}{
		{
			name: "browsers",
			args: []string{"browsers", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectBrowsersData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Browsers) != 1 || data.Browsers[0].ID != "browser-a" || strings.Contains(result.stdout, "secret") {
					t.Fatalf("browsers result = %+v output=%s", data, result.stdout)
				}
			},
		},
		{
			name: "tabs",
			args: []string{"tabs", "--browser", "browser-a", "--eligible", "--json"},
			check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectTabsData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Tabs) != 1 || data.Tabs[0].TargetID != "tab-a" || data.Tabs[0].Origin != "https://fixture.test" {
					t.Fatalf("tabs result = %+v", data)
				}
			},
		},
		{
			name: "activate",
			args: []string{"activate", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				requireDirectSuccess(t, result)
				if len(broker.activateCalls) != 1 || broker.activateCalls[0].TargetID != "tab-a" {
					t.Fatalf("activate calls = %+v", broker.activateCalls)
				}
			},
		},
		{
			name: "context",
			args: []string{"context", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectContext
				decodeDirectData(t, envelope.Data, &data)
				if data.Generation != 7 || data.CatalogGeneration != 7 || data.ToolCount != 1 || data.URL != "https://fixture.test/page" {
					t.Fatalf("context result = %+v", data)
				}
			},
		},
		{
			name: "tools",
			args: []string{"tools", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, _ *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectToolsData
				decodeDirectData(t, envelope.Data, &data)
				if len(data.Tools) != 1 || data.Tools[0].Ref != string(tool.Ref) || data.Tools[0].Generation != 7 {
					t.Fatalf("tools result = %+v", data)
				}
			},
		},
		{
			name: "invoke",
			args: []string{"invoke", "--browser", "browser-a", "--tab", "tab-a", "--tool-ref", string(tool.Ref), "--input-json", `{"value":1}`, "--reason", "test reason", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectInvocation
				decodeDirectData(t, envelope.Data, &data)
				if data.InvocationID != "inv-23" || data.ToolRef != string(tool.Ref) || data.Status != string(webmcp.InvocationCompleted) {
					t.Fatalf("invoke result = %+v", data)
				}
				if broker.invokeRequest.ToolRef != tool.Ref || string(broker.invokeRequest.Input) != `{"value":1}` || broker.invokeRequest.Reason != "test reason" {
					t.Fatalf("invoke request = %+v", broker.invokeRequest)
				}
			},
		},
		{
			name: "cancel",
			args: []string{"cancel", "inv-23", "--browser", "browser-a", "--tab", "tab-a", "--json"},
			check: func(t *testing.T, result directCommandResult, broker *directCommandBroker) {
				envelope := requireDirectSuccess(t, result)
				var data WebMCPDirectCancelData
				decodeDirectData(t, envelope.Data, &data)
				if data.InvocationID != "inv-23" || broker.cancelRequest.InvocationID != "inv-23" {
					t.Fatalf("cancel result/request = %+v/%+v", data, broker.cancelRequest)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newBroker()
			result := executeDirectCommand(t, configDir, store, directFactory(broker), test.args...)
			test.check(t, result, broker)
			if test.name == "invoke" {
				var receipt WebMCPDirectInvocationReceipt
				decoder := json.NewDecoder(strings.NewReader(result.stderr))
				if err := decoder.Decode(&receipt); err != nil {
					t.Fatalf("decode dispatch receipt: %v; stderr=%q", err, result.stderr)
				}
				if receipt.Version != webmcpDirectInvocationReceiptVersion || receipt.InvocationID != "inv-23" || receipt.ToolRef != string(tool.Ref) || receipt.State != string(webmcp.InvocationDispatched) {
					t.Fatalf("dispatch receipt = %+v", receipt)
				}
				var extra any
				if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
					t.Fatalf("dispatch stderr contains more than one receipt: err=%v extra=%#v", err, extra)
				}
			} else if result.stderr != "" {
				t.Fatalf("stderr = %q", result.stderr)
			}
		})
	}
}

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
			BrowserInvocationID: "browser-invocation-9",
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
	if len(result.stderr) > webmcpDirectInvocationReceiptMaxBytes {
		t.Fatalf("dispatch receipt is %d bytes, want <= %d: %q", len(result.stderr), webmcpDirectInvocationReceiptMaxBytes, result.stderr)
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
	if receipt.Version != webmcpDirectInvocationReceiptVersion || receipt.InvocationID != "browser-invocation-9" || receipt.ToolRef != string(tool.Ref) || receipt.State != string(webmcp.InvocationDispatched) {
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
	if data.InvocationID != "browser-invocation-9" {
		t.Fatalf("final invocation ID = %q, want browser protocol ID", data.InvocationID)
	}
}

func TestWebMCPDirectHumanCancellationReportsIDAndUnknownSideEffect(t *testing.T) {
	var output bytes.Buffer
	err := writeWebMCPDirectHuman(&output, "invoke", nil, webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), map[string]any{
		"invocation_id":       "browser-invocation-9",
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

func TestWebMCPDirectInterruptBeforeDispatchDoesNotFabricateInvocationID(t *testing.T) {
	result := webmcp.ResultErrorFor(directInvocationCanceledBeforeDispatch("webmcp.tool-ref.v1:fixture-ref"), webmcp.ErrorInvocationFailed, nil)
	if result.Code != string(webmcp.ErrorInvocationCanceled) || result.Retryable {
		t.Fatalf("pre-dispatch cancellation result = %+v", result)
	}
	if _, ok := result.Details["invocation_id"]; ok {
		t.Fatalf("pre-dispatch cancellation fabricated an invocation ID: %#v", result.Details)
	}
	if result.Details["cancel_source"] != "interrupt" || result.Details["phase"] != "before_dispatch" {
		t.Fatalf("pre-dispatch cancellation details = %#v", result.Details)
	}
}

func TestWebMCPDirectInterruptCleanupContextIsIndependent(t *testing.T) {
	status := boundedInterruptCancellationStatus(func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	})
	if status != "requested" {
		t.Fatalf("interrupt cleanup status = %q, want requested", status)
	}
}

func TestWebMCPDirectInvokeSIGINTChildProcess(t *testing.T) {
	if os.Getenv("WEBMCP_DIRECT_SIGINT_CHILD") == "1" {
		runWebMCPDirectInvokeSIGINTChild(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestWebMCPDirectInvokeSIGINTChildProcess$", "-test.v=false")
	command.Env = append(os.Environ(), "WEBMCP_DIRECT_SIGINT_CHILD=1")
	stdout := &childProcessOutputBuffer{}
	stderr := newChildProcessStderrBuffer()
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start SIGINT child: %v", err)
	}
	childAlive := true
	defer func() {
		if childAlive {
			_ = command.Process.Kill()
		}
	}()

	var firstValue string
	select {
	case firstValue = <-stderr.firstLine:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT child did not emit a dispatch receipt")
	}
	var receipt WebMCPDirectInvocationReceipt
	if err := json.Unmarshal([]byte(firstValue), &receipt); err != nil {
		t.Fatalf("decode child dispatch receipt: %v; stderr=%q", err, firstValue)
	}
	if receipt.InvocationID != "browser-child-1" || receipt.State != string(webmcp.InvocationDispatched) {
		t.Fatalf("child dispatch receipt = %+v", receipt)
	}

	signalAt := time.Now()
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT to child: %v", err)
	}
	exitErr := command.Wait()
	childAlive = false
	if exitErr == nil || command.ProcessState.ExitCode() == 0 {
		t.Fatalf("SIGINT child exited successfully: err=%v exit=%d", exitErr, command.ProcessState.ExitCode())
	}
	if elapsed := time.Since(signalAt); elapsed > webmcpDirectInterruptReconciliationTimeout+time.Second {
		t.Fatalf("SIGINT child completion took %s, want <= %s", elapsed, webmcpDirectInterruptReconciliationTimeout+time.Second)
	}

	stderrValue := stderr.String()
	stdoutValue := stdout.String()
	envelope := decodeDirectEnvelope(t, stdoutValue)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvocationCanceled) || envelope.Error.Retryable {
		t.Fatalf("SIGINT child envelope = %+v", envelope)
	}
	if envelope.Error.Details["invocation_id"] != "browser-child-1" || envelope.Error.Details["cancel_source"] != "interrupt" || envelope.Error.Details["side_effect_unknown"] != true {
		t.Fatalf("SIGINT child cancellation details = %#v", envelope.Error.Details)
	}
	if strings.Contains(stdoutValue, "input_secret") || strings.Contains(stdoutValue, "page_output") || strings.Contains(stdoutValue, "credential") {
		t.Fatalf("SIGINT child output leaked sensitive data: %q", stdoutValue)
	}
	if strings.TrimSpace(strings.TrimPrefix(stderrValue, firstValue)) != "" {
		t.Fatalf("SIGINT child wrote unexpected stderr after receipt: %q", stderrValue)
	}
}

type childProcessOutputBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *childProcessOutputBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}

func (b *childProcessOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type childProcessStderrBuffer struct {
	childProcessOutputBuffer
	firstLine chan string
	notified  bool
}

func newChildProcessStderrBuffer() *childProcessStderrBuffer {
	return &childProcessStderrBuffer{firstLine: make(chan string, 1)}
}

func (b *childProcessStderrBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	written, err := b.data.Write(value)
	if !b.notified {
		if newline := bytes.IndexByte(b.data.Bytes(), '\n'); newline >= 0 {
			b.notified = true
			line := append([]byte(nil), b.data.Bytes()[:newline+1]...)
			b.mu.Unlock()
			b.firstLine <- string(line)
			return written, err
		}
	}
	b.mu.Unlock()
	return written, err
}

func runWebMCPDirectInvokeSIGINTChild(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, candidate, tool := directFixture()
	broker := &sigintChildBroker{directCommandBroker: &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{candidate},
		targets:    []webmcp.Target{target},
		selected:   page,
		catalog:    webmcp.ToolCatalogSnapshot{Context: page, Generation: page.Generation, Tools: []webmcp.ToolDescriptor{tool}},
		invokeResult: webmcp.InvokeResult{
			InvocationID:        "broker-child-1",
			BrowserInvocationID: "browser-child-1",
			State:               webmcp.InvocationDispatched,
		},
	}}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, directFactory(broker))
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	root.SetArgs([]string{"invoke", "--browser", string(candidate.ID), "--tab", string(target.ID), "--tool-ref", string(tool.Ref), "--input-json", `{"input_secret":"do-not-echo"}`, "--json"})
	if err := root.Execute(); err == nil {
		os.Exit(43)
	}
	if broker.cancelRequest.InvocationID != "broker-child-1" {
		os.Exit(44)
	}
	if broker.cancelContextErr != nil {
		os.Exit(45)
	}
	os.Exit(42)
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

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "cancel", "--invocation", "browser-invocation-9", "--json")
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectCancelData
	decodeDirectData(t, envelope.Data, &data)
	if data.InvocationID != "browser-invocation-9" || data.Status != "cancel_requested" {
		t.Fatalf("cancel data = %+v", data)
	}
	if got := broker.directCancelRequest; got.Target != (webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}) || got.InvocationID != "browser-invocation-9" {
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

	result := executeDirectCommand(t, configDir, nil, directFactory(broker), "cancel", "--browser", "browser-a", "--invocation", "browser-invocation-9", "--json")
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

	result := executeDirectCommand(t, configDir, store, directFactory(broker), "cancel", "--invocation", "browser-invocation-9", "--json")
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
	if endedData.Status != webmcpDirectWatchStatusEnded || len(endedData.Events) != 0 {
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
	if canceledData.Status != webmcpDirectWatchStatusCanceled {
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
	if data.Status != webmcpDirectWatchStatusFailed || len(data.Events) != 1 || data.Events[0].Type != string(webmcp.BrokerEventSessionClosed) || data.Events[0].Reason != webmcp.BrokerWatchBufferFullReason {
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
	if data.Status != webmcpDirectWatchStatusEnded || len(data.Events) != 2 || data.Events[0].Type != string(webmcp.BrokerEventSelected) || data.Events[1].Type != string(webmcp.BrokerEventCatalogChanged) {
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

func TestWebMCPDirectRetainedSelectionDistinguishesFreshReplacementFromEndpointLoss(t *testing.T) {
	configDir := writeDirectConfig(t, "")
	store := NewFileWebMCPSelectionStore(configDir)
	page, target, oldCandidate, _ := directFixture()
	oldCandidate.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	selected := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{oldCandidate},
		targets:    []webmcp.Target{target},
		selected:   page,
	}
	seed := executeDirectCommand(t, configDir, store, directFactory(selected), "select", "--browser", string(oldCandidate.ID), "--tab", string(target.ID), "--json")
	if seed.err != nil {
		t.Fatalf("seed persisted selection: %v\nstdout=%s", seed.err, seed.stdout)
	}
	oldRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load old selection: %v", err)
	}
	if oldRecord.BrowserInstanceID != oldCandidate.BrowserInstanceID || oldRecord.Generation != page.Generation {
		t.Fatalf("persisted identity = %+v, want instance=%q generation=%d", oldRecord, oldCandidate.BrowserInstanceID, page.Generation)
	}

	replacement := oldCandidate
	replacement.ID = webmcp.BrowserID(randomizedWebMCPTestID(t, "browser-"))
	replacement.BrowserInstanceID = randomizedWebMCPInstanceID(t)
	replacementTarget := target
	replacementTarget.BrowserID = replacement.ID
	replacementBroker := &directCommandBroker{
		candidates: []webmcp.BrowserCandidate{replacement},
		targets:    []webmcp.Target{replacementTarget},
	}
	replaced := executeDirectCommand(t, configDir, store, directFactory(replacementBroker), "context", "--json")
	if replaced.err == nil {
		t.Fatal("context unexpectedly selected a reachable fresh browser replacement")
	}
	replacedEnvelope := decodeDirectEnvelope(t, replaced.stdout)
	if replacedEnvelope.OK || replacedEnvelope.Error == nil || replacedEnvelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("replacement envelope = %+v", replacedEnvelope)
	}
	if details := replacedEnvelope.Error.Details; details["browser_id"] != oldRecord.BrowserID || details["target_id"] != oldRecord.TargetID || details["selected_generation"] != float64(oldRecord.Generation) || details["reason"] != "browser_instance_changed" {
		t.Fatalf("replacement details = %#v", details)
	}
	if len(replacementBroker.selectCalls) != 0 || len(replacementBroker.activateCalls) != 0 {
		t.Fatalf("replacement received selection work: select=%+v activate=%+v", replacementBroker.selectCalls, replacementBroker.activateCalls)
	}
	if oldAfterReplacement, loadErr := store.Load(); loadErr != nil || oldAfterReplacement != oldRecord {
		t.Fatalf("replacement changed persisted selection: before=%+v after=%+v err=%v", oldRecord, oldAfterReplacement, loadErr)
	}

	lostBroker := &directCommandBroker{
		discoverErr: webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "browser endpoint could not be reached", map[string]any{
			"phase": "discovery",
		}),
	}
	lost := executeDirectCommand(t, configDir, store, directFactory(lostBroker), "context", "--json")
	if lost.err == nil {
		t.Fatal("context unexpectedly succeeded after endpoint loss")
	}
	lostEnvelope := decodeDirectEnvelope(t, lost.stdout)
	if lostEnvelope.OK || lostEnvelope.Error == nil || lostEnvelope.Error.Code != string(webmcp.ErrorBrowserDisconnected) {
		t.Fatalf("lost endpoint envelope = %+v", lostEnvelope)
	}
	if details := lostEnvelope.Error.Details; details["browser_id"] != oldRecord.BrowserID || details["target_id"] != oldRecord.TargetID || details["phase"] != "discovery" || details["reconnect_required"] != true {
		t.Fatalf("lost endpoint details = %#v", details)
	}
}

func TestWebMCPDirectMalformedInputReturnsSelectedSchema(t *testing.T) {
	schema := `{"type":"object","properties":{"profile":{"type":"object","properties":{"count":{"type":"integer","minimum":1},"mode":{"enum":["fast","safe"]}},"required":["count"],"additionalProperties":false},"tags":{"type":"array","items":{"type":"string"}}},"required":["profile","tags"],"additionalProperties":false}`
	const toolRef = webmcp.ToolRef("webmcp.tool-ref.v1:AAAAAAAAAAAAAAAAAAAAAA")
	wantGolden, err := os.ReadFile(filepath.Join("testdata", "webmcp-invoke-invalid-input.golden.json"))
	if err != nil {
		t.Fatalf("read malformed-input golden: %v", err)
	}

	for _, testCase := range []struct {
		name       string
		positional bool
	}{
		{name: "exact ref"},
		{name: "unique positional name", positional: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := writeDirectConfig(t, "")
			store := NewFileWebMCPSelectionStore(configDir)
			_, target, candidate, tool := directFixture()
			tool.InputSchema = json.RawMessage(schema)
			runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
				testkit.NewTargetConfig(target, testkit.WithInitialCatalog(tool)),
			))
			broker := webmcp.NewBroker(webmcp.BrokerOptions{
				Runtime:    runtime,
				Discoverer: directDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
				ToolRefFactory: func(webmcp.ToolDescriptor) (webmcp.ToolRef, error) {
					return toolRef, nil
				},
			})

			args := []string{"invoke", "--browser", string(candidate.ID), "--tab", string(target.ID)}
			if testCase.positional {
				args = append(args, "read_state")
			} else {
				args = append(args, "--tool-ref", string(toolRef))
			}
			args = append(args, "--input-json", `{"profile":{"mode":"fast","secret":"do-not-echo"}`, "--json")

			result := executeDirectCommand(t, configDir, store, directFactory(broker), args...)
			if result.err == nil {
				t.Fatal("malformed invocation unexpectedly succeeded")
			}
			if got := strings.TrimSpace(result.stdout); got != strings.TrimSpace(string(wantGolden)) {
				t.Fatalf("malformed-input envelope = %s, want golden %s", got, strings.TrimSpace(string(wantGolden)))
			}
			if strings.Contains(result.stdout, "do-not-echo") {
				t.Fatalf("malformed input leaked into result: %s", result.stdout)
			}
			envelope := decodeDirectEnvelope(t, result.stdout)
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorInvalidToolInput) || !envelope.Error.Retryable {
				t.Fatalf("malformed-input envelope = %+v", envelope)
			}
			operations := runtime.Operations()
			if hasTestkitOperation(operations, testkit.OperationInvoke) {
				t.Fatalf("malformed input was dispatched: %+v", operations)
			}
		})
	}
}

type directCommandResult struct {
	stdout string
	stderr string
	err    error
}

func executeDirectCommand(t *testing.T, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	return executeDirectCommandContext(t, context.Background(), configDir, store, factory, args...)
}

func executeDirectCommandContext(t *testing.T, ctx context.Context, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, factory)
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return directCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func decodeDirectEnvelope(t *testing.T, output string) webmcp.ToolResultEnvelope {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var envelope webmcp.ToolResultEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode direct envelope: %v; output=%q", err, output)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("direct output contains more than one result: err=%v extra=%#v output=%q", err, extra, output)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("invalid direct envelope: %v; output=%q", err, output)
	}
	return envelope
}

func requireDirectSuccess(t *testing.T, result directCommandResult) webmcp.ToolResultEnvelope {
	t.Helper()
	if result.err != nil {
		t.Fatalf("direct command: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if !envelope.OK {
		t.Fatalf("direct command failed: %+v", envelope.Error)
	}
	return envelope
}

func decodeDirectData(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode direct data: %v; data=%s", err, raw)
	}
}

func writeDirectConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	contents := "browser:\n  connection:\n    cdp_url: http://127.0.0.1:9222\n"
	contents += extra
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(contents), 0o600); err != nil {
		t.Fatalf("write direct config: %v", err)
	}
	return dir
}

func directFixture() (webmcp.PageContext, webmcp.Target, webmcp.BrowserCandidate, webmcp.ToolDescriptor) {
	candidate := webmcp.BrowserCandidate{
		ID:           "browser-a",
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      "Chrome/Test",
		Protocol:     "1.3",
		HTTPURL:      "http://127.0.0.1:9222/json/version?token=secret",
		BrowserWSURL: "ws://127.0.0.1/devtools/browser/secret",
		Loopback:     true,
	}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-a",
		Type:      "page",
		Title:     "Fixture page",
		URL:       "https://fixture.test/page?password=secret#fragment",
		Origin:    "https://fixture.test",
		Eligible:  true,
	}
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: candidate.ID, TargetID: target.ID},
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: 7,
		Connected:  true,
		Ready:      true,
	}
	tool := webmcp.ToolDescriptor{
		Ref:         "webmcp.tool-ref.v1:fixture-ref",
		Name:        "read_state",
		Description: "Read fixture state",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"}},"additionalProperties":false}`),
		FrameID:     "frame-1",
		Origin:      target.Origin,
		Generation:  7,
	}
	return page, target, candidate, tool
}

func targetOrigin(target webmcp.Target) string {
	return target.Origin
}

func directFactory(broker webmcp.Broker) WebMCPDoctorFactory {
	return func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		return WebMCPDoctorRuntime{Broker: broker}, nil
	}
}

type directDiscoverer struct {
	candidates []webmcp.BrowserCandidate
}

func (d directDiscoverer) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return append([]webmcp.BrowserCandidate(nil), d.candidates...), nil
}

type directCommandBroker struct {
	candidates []webmcp.BrowserCandidate
	targets    []webmcp.Target
	selected   webmcp.PageContext
	catalog    webmcp.ToolCatalogSnapshot

	discoverErr error
	listErr     error
	selectErr   error
	activateErr error
	toolsErr    error
	invokeErr   error
	cancelErr   error

	invokeResult webmcp.InvokeResult
	watch        <-chan webmcp.BrokerEvent

	selectCalls   []webmcp.TargetSelector
	activateCalls []webmcp.TargetSelector
	invokeRequest webmcp.InvokeRequest
	cancelRequest webmcp.CancelRequest
	closeCalls    int
}

type selectionOrderingWatchBroker struct {
	*directCommandBroker
	stream     chan webmcp.BrokerEvent
	subscribed bool
}

func (b *selectionOrderingWatchBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	b.subscribed = true
	return b.stream
}

func (b *selectionOrderingWatchBroker) Select(ctx context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	return b.SelectWithOptions(ctx, selector, webmcp.SelectOptions{})
}

func (b *selectionOrderingWatchBroker) SelectWithOptions(_ context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	if !b.subscribed {
		return webmcp.PageContext{}, errors.New("watch subscription must precede selection")
	}
	page, err := b.directCommandBroker.SelectWithOptions(context.Background(), selector, options)
	if err != nil {
		return webmcp.PageContext{}, err
	}
	b.stream <- webmcp.BrokerEvent{Version: webmcp.BrowserEventsVersion, Type: webmcp.BrokerEventSelected, Sequence: 1, BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: page.Generation}
	b.stream <- webmcp.BrokerEvent{Version: webmcp.BrowserEventsVersion, Type: webmcp.BrokerEventCatalogChanged, Sequence: 2, BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: page.Generation, Reason: "tools_added"}
	close(b.stream)
	return page, nil
}

type directCancelCommandBroker struct {
	*directCommandBroker

	directCancelRequest webmcp.DirectCancelRequest
	directCancelErr     error
}

type sigintChildBroker struct {
	*directCommandBroker
	cancelContextErr error
}

func (b *sigintChildBroker) WaitInvocation(ctx context.Context, _ webmcp.InvocationID) (webmcp.InvokeResult, error) {
	<-ctx.Done()
	return webmcp.InvokeResult{}, ctx.Err()
}

func (b *sigintChildBroker) Cancel(ctx context.Context, request webmcp.CancelRequest) error {
	b.cancelContextErr = ctx.Err()
	return b.directCommandBroker.Cancel(ctx, request)
}

func (b *directCancelCommandBroker) CancelDirect(_ context.Context, request webmcp.DirectCancelRequest) error {
	b.directCancelRequest = request
	return b.directCancelErr
}

func (b *directCommandBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if b.discoverErr != nil {
		return nil, b.discoverErr
	}
	return append([]webmcp.BrowserCandidate(nil), b.candidates...), nil
}

func (b *directCommandBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return append([]webmcp.Target(nil), b.targets...), nil
}

func (b *directCommandBroker) Select(_ context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	return b.selectWithOptions(selector, false)
}

func (b *directCommandBroker) SelectWithOptions(_ context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	return b.selectWithOptions(selector, options.Activate)
}

func (b *directCommandBroker) selectWithOptions(selector webmcp.TargetSelector, activate bool) (webmcp.PageContext, error) {
	if b.selectErr != nil {
		return webmcp.PageContext{}, b.selectErr
	}
	b.selectCalls = append(b.selectCalls, selector)
	if activate {
		b.activateCalls = append(b.activateCalls, selector)
	}
	return b.selected, nil
}

func (b *directCommandBroker) Activate(_ context.Context, selector webmcp.TargetSelector) error {
	if b.activateErr != nil {
		return b.activateErr
	}
	b.activateCalls = append(b.activateCalls, selector)
	return nil
}

func (b *directCommandBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *directCommandBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	if b.toolsErr != nil {
		return webmcp.ToolCatalogSnapshot{}, b.toolsErr
	}
	return b.catalog, nil
}

func (b *directCommandBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	b.invokeRequest = request
	if b.invokeErr != nil {
		return webmcp.InvokeResult{}, b.invokeErr
	}
	return b.invokeResult, nil
}

func (b *directCommandBroker) Cancel(_ context.Context, request webmcp.CancelRequest) error {
	b.cancelRequest = request
	return b.cancelErr
}

func (b *directCommandBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	if b.watch != nil {
		return b.watch
	}
	return closedEventChannel()
}

func (b *directCommandBroker) Close() error {
	b.closeCalls++
	return nil
}

func closedEventChannel() <-chan webmcp.BrokerEvent {
	channel := make(chan webmcp.BrokerEvent)
	close(channel)
	return channel
}
