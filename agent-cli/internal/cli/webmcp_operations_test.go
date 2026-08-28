package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
			args: []string{"cancel", "inv-23", "--json"},
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
			if result.stderr != "" {
				t.Fatalf("stderr = %q", result.stderr)
			}
		})
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
