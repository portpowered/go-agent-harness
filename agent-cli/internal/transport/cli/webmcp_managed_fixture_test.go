package cli

// Managed-browser lifecycle fixtures shared by CLI session tests. The
// composition-level managed tests live in internal/webmcp/production.

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/spf13/cobra"
)

// testDevToolsVersionPath is the DevTools HTTP version endpoint served by the
// CLI's browser fixtures.
const testDevToolsVersionPath = "/json/version"

// Fixture values compared by the WebMCP doctor command tests.
const (
	testDoctorTabID    = "tab-a"
	testDoctorLoopback = "loopback"
)

func newManagedCompositionTestManager(configDir string, control *managedCompositionTestControl, starts *atomic.Int32) *chrome.ManagedBrowserManager {
	return chrome.NewManagedBrowserManager(chrome.ManagedBrowserManagerOptions{
		ConfigDir: configDir,
		LaunchOptions: chrome.ManagedBrowserLaunchOptions{
			ConfigDir:        configDir,
			DisplayAvailable: func() bool { return true },
			Acquirer: chrome.ManagedChromeExecutableAcquirerFunc(func(context.Context) (chrome.ChromeExecutable, error) {
				return chrome.ChromeExecutable{Path: "/qualified/test-chrome", Major: 152, Source: chrome.ExecutableSourceStock}, nil
			}),
			HTTPClient: &http.Client{Transport: managedCompositionVersionTransport{}},
			ProcessStarter: func(string, []string) (chrome.ManagedBrowserProcess, error) {
				starts.Add(1)
				return control.newProcess(7002), nil
			},
			StartupTimeout:  500 * time.Millisecond,
			PollInterval:    time.Millisecond,
			ShutdownTimeout: 50 * time.Millisecond,
		},
		ProcessInspector: chrome.ManagedBrowserProcessInspectorFunc(func(_ context.Context, state chrome.ManagedBrowserState) (chrome.ManagedBrowserProcessInfo, error) {
			return chrome.ManagedBrowserProcessInfo{PID: state.PID, Identity: "composition-incarnation", ProfileDir: state.ProfileDir}, nil
		}),
		ProcessReattacher: func(context.Context, chrome.ManagedBrowserState) (chrome.ManagedBrowserProcess, error) {
			return control.newProcess(7002), nil
		},
		LockTimeout: 2 * time.Second,
		LockPoll:    time.Millisecond,
	})
}

type managedCompositionVersionTransport struct{}

func (managedCompositionVersionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Path != testDevToolsVersionPath {
		return nil, errors.New("unexpected readiness request")
	}
	body := fmt.Sprintf(`{"Browser":"Google Chrome 152.0.1.2","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/browser/composition"}`, request.URL.Port())
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

type managedCompositionTestControl struct {
	done      chan struct{}
	initOnce  sync.Once
	exitOnce  sync.Once
	terminate atomic.Int32
	kill      atomic.Int32
}

func (c *managedCompositionTestControl) newProcess(pid int) *managedCompositionTestProcess {
	c.initOnce.Do(func() { c.done = make(chan struct{}) })
	return &managedCompositionTestProcess{control: c, pid: pid}
}

func (c *managedCompositionTestControl) exit() {
	c.initOnce.Do(func() { c.done = make(chan struct{}) })
	c.exitOnce.Do(func() { close(c.done) })
}

type managedCompositionTestProcess struct {
	control *managedCompositionTestControl
	pid     int
}

func (p *managedCompositionTestProcess) Wait() error {
	if p == nil || p.control == nil {
		return errors.New("test process unavailable")
	}
	<-p.control.done
	return nil
}

func (p *managedCompositionTestProcess) Terminate() error {
	p.control.terminate.Add(1)
	p.control.exit()
	return nil
}

func (p *managedCompositionTestProcess) Kill() error {
	p.control.kill.Add(1)
	p.control.exit()
	return nil
}

func (p *managedCompositionTestProcess) PID() int { return p.pid }

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

func countTestkitOperations(operations []testkit.Operation, kind testkit.OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind {
			count++
		}
	}
	return count
}

type directCommandResult struct {
	stdout string
	stderr string
	err    error
}

func executeDirectCommand(t *testing.T, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	return executeDirectCommandContext(t, context.Background(), configDir, store, factory, args...)
}

func executeDirectCommandThroughAgentRoot(t *testing.T, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, factory)
	operations.SelectionStore = store
	webmcpCommand := &WebMCPCommand{OperationsCommand: operations}
	root := &cobra.Command{Use: "agent"}
	root.AddCommand(NewPath("webmcp", webmcpCommand.Generate()).CreateCommand())
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"webmcp"}, args...))
	err := root.ExecuteContext(context.Background())
	return directCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func executeDirectCommandContext(t *testing.T, ctx context.Context, configDir string, store WebMCPSelectionStore, factory WebMCPDoctorFactory, args ...string) directCommandResult {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	operations := NewWebMCPOperationsCommand(globalFlags, factory)
	operations.SelectionStore = store
	root := &cobra.Command{Use: "webmcp", SilenceErrors: true, SilenceUsage: true}
	operations.AddCommands(root) //nolint:contextcheck // Commands take the context passed to ExecuteContext below.
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

	selectCalls     []webmcp.TargetSelector
	activateCalls   []webmcp.TargetSelector
	listTargetCalls int
	invokeRequest   webmcp.InvokeRequest
	cancelRequest   webmcp.CancelRequest
	closeCalls      int
}

func (b *directCommandBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if b.discoverErr != nil {
		return nil, b.discoverErr
	}
	return append([]webmcp.BrowserCandidate(nil), b.candidates...), nil
}

func (b *directCommandBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	b.listTargetCalls++
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

func (b *selectionOrderingWatchBroker) SelectWithOptions(ctx context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	if !b.subscribed {
		return webmcp.PageContext{}, errors.New("watch subscription must precede selection")
	}
	page, err := b.directCommandBroker.SelectWithOptions(ctx, selector, options)
	if err != nil {
		return webmcp.PageContext{}, err
	}
	b.stream <- webmcp.BrokerEvent{Version: webmcp.BrowserEventsVersion, Type: webmcp.BrokerEventSelected, Sequence: 1, BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: page.Generation}
	b.stream <- webmcp.BrokerEvent{Version: webmcp.BrowserEventsVersion, Type: webmcp.BrokerEventCatalogChanged, Sequence: 2, BrowserID: selector.BrowserID, TargetID: selector.TargetID, Generation: page.Generation, Reason: "tools_added"}
	close(b.stream)
	return page, nil
}
