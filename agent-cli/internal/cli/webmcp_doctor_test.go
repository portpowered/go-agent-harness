package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/spf13/cobra"
)

func TestWebMCPDoctorReadyJSONUsesRealBrokerStateAndRedactsEndpoints(t *testing.T) {
	candidate := webmcp.BrowserCandidate{
		ID:           "browser-a",
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      "Chrome/Test",
		Protocol:     "1.3",
		HTTPURL:      "http://127.0.0.1:9222",
		BrowserWSURL: "ws://127.0.0.1/devtools/browser/secret-browser-token",
		Loopback:     true,
	}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-a",
		Type:      "page",
		Title:     "Fixture",
		URL:       "https://fixture.test/page?password=secret#fragment",
		Origin:    "https://fixture.test",
		Eligible:  true,
	}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(target, testkit.WithInitialCatalog(webmcp.ToolDescriptor{
			Name:        "read_state",
			FrameID:     "frame-1",
			InputSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
		})),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:    runtime,
		Discoverer: doctorDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}},
	})
	configDir := writeDoctorConfig(t, `
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: "http://127.0.0.1:9222/json/version?token=secret#fragment"
  selection:
    browser: browser-a
    tab: tab-a
`)

	factoryCalls := 0
	factory := func(browser config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		factoryCalls++
		if browser.Selection.Tab != "tab-a" {
			t.Fatalf("factory received selection %+v", browser.Selection)
		}
		return WebMCPDoctorRuntime{
			Broker: broker,
			VersionFunc: func(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
				return webmcp.BrowserVersion{Browser: "Chrome/Test", ProtocolVersion: "1.3", WebSocketDebuggerURL: candidate.BrowserWSURL}, nil
			},
		}, nil
	}

	root, stdout, stderr := executeDoctorCommand(t, configDir, factory, "--json")
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("doctor factory calls = %d, want one", factoryCalls)
	}
	if stderr.String() != "" {
		t.Fatalf("doctor stderr = %q, want empty", stderr.String())
	}
	report := decodeDoctorReport(t, stdout.String())
	if report.Status != doctorStatusReady || report.Error != nil {
		t.Fatalf("report status/error = %s/%+v, want ready/nil", report.Status, report.Error)
	}
	if report.WebMCP != "supported" || !report.Catalog.Ready || report.Catalog.ToolCount != 1 {
		t.Fatalf("WebMCP/catalog report = %+v/%+v", report.WebMCP, report.Catalog)
	}
	if report.PageTargets != 1 || report.EligiblePages != 1 || report.SelectedPage == nil || !report.SelectedPage.Selected {
		t.Fatalf("target report = %+v counts=%d/%d", report.SelectedPage, report.PageTargets, report.EligiblePages)
	}
	if report.Endpoint.Address != "http://127.0.0.1:9222/json/version" {
		t.Fatalf("redacted endpoint = %q", report.Endpoint.Address)
	}
	if report.Endpoint.Scope != "loopback" {
		t.Fatalf("endpoint scope = %q, want loopback", report.Endpoint.Scope)
	}
	encoded := stdout.String()
	for _, secret := range []string{"secret", "password=", "#fragment", "/devtools/browser/secret-browser-token"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("doctor JSON exposed %q: %s", secret, encoded)
		}
	}
	if check := doctorCheckByName(report, "cleanup"); check.Status != doctorCheckPass {
		t.Fatalf("cleanup check = %+v, want pass", check)
	}

	operations := runtime.Operations()
	if !hasTestkitOperation(operations, testkit.OperationOpen) || !hasTestkitOperation(operations, testkit.OperationEnableWebMCP) {
		t.Fatalf("real broker did not perform endpoint/enable checks: %+v", operations)
	}
	if hasTestkitOperation(operations, testkit.OperationActivate) {
		t.Fatalf("doctor unexpectedly activated the target: %+v", operations)
	}
}

func TestWebMCPDoctorHumanOutputIsDeterministic(t *testing.T) {
	candidate := webmcp.BrowserCandidate{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3", HTTPURL: "http://127.0.0.1:9222", Loopback: true}
	runtime := testkit.NewScriptedBrowserRuntime(testkit.NewBrowserConfig(candidate,
		testkit.NewTargetConfig(webmcp.Target{ID: "tab-a", Type: "page", Origin: "https://fixture.test", Eligible: true}),
	))
	broker := webmcp.NewBroker(webmcp.BrokerOptions{Runtime: runtime, Discoverer: doctorDiscoverer{candidates: []webmcp.BrowserCandidate{candidate}}})
	configDir := writeDoctorConfig(t, `
browser:
  connection:
    cdp_url: http://127.0.0.1:9222
  selection:
    browser: browser-a
    tab: tab-a
`)
	factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		return WebMCPDoctorRuntime{Broker: broker}, nil
	}
	root, stdout, stderr := executeDoctorCommand(t, configDir, factory)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if stderr.String() != "" {
		t.Fatalf("doctor stderr = %q", stderr.String())
	}
	human := stdout.String()
	for _, want := range []string{
		"WebMCP doctor: ready",
		"Endpoint source: explicit HTTP URL",
		"Endpoint:        http://127.0.0.1:9222",
		"Scope:           loopback",
		"Browser:         Chrome/Test id=browser-a",
		"Protocol:        1.3",
		"WebMCP domain:   supported",
		"Page targets:    1",
		"Eligible pages:  1",
		"Selected page:   browser-a/tab-a",
		"Warnings:",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("human output missing %q:\n%s", want, human)
		}
	}
}

func TestWebMCPDoctorInvalidConfigurationDoesNotConstructRuntime(t *testing.T) {
	configDir := writeDoctorConfig(t, `
browser:
  tools:
    backend: unsupported
`)
	factoryCalls := 0
	factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		factoryCalls++
		return WebMCPDoctorRuntime{}, nil
	}
	root, stdout, _ := executeDoctorCommand(t, configDir, factory, "--json")
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), doctorErrorInvalidConfiguration) {
		t.Fatalf("doctor error = %v, want invalid configuration", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want zero", factoryCalls)
	}
	report := decodeDoctorReport(t, stdout.String())
	if report.Status != doctorStatusInvalidConfiguration || report.Error == nil || report.Error.Code != doctorErrorInvalidConfiguration {
		t.Fatalf("invalid config report = %+v", report)
	}
}

func TestWebMCPDoctorDefaultRuntimeReportsClassifiedDiscoveryFailure(t *testing.T) {
	root, stdout, _ := executeDoctorCommand(t, t.TempDir(), nil, "--json")
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), string(webmcp.ErrorEndpointNotFound)) {
		t.Fatalf("doctor error = %v, want classified endpoint-not-found failure", err)
	}
	report := decodeDoctorReport(t, stdout.String())
	if report.Status != doctorStatusNotReady || report.Error == nil || report.Error.Code != string(webmcp.ErrorEndpointNotFound) {
		t.Fatalf("default runtime report = %+v, want not-ready endpoint_not_found", report)
	}
	if discovery := doctorCheckByName(report, "discovery"); discovery.Status != doctorCheckFail {
		t.Fatalf("discovery check = %+v, want discovery failure", discovery)
	}
	if strings.Contains(stdout.String(), "Lane B") || strings.Contains(stdout.String(), "Lane D") {
		t.Fatalf("default runtime output exposed internal implementation names: %s", stdout.String())
	}
}

func TestWebMCPDoctorClassifiesNoEndpointAndNoEligibleTarget(t *testing.T) {
	tests := []struct {
		name       string
		broker     webmcp.Broker
		configYAML string
		wantCode   string
	}{
		{
			name: "no endpoint",
			broker: &doctorStubBroker{
				discoverErr: webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "endpoint unavailable", map[string]any{"source": "fake"}),
			},
			wantCode: string(webmcp.ErrorEndpointNotFound),
		},
		{
			name: "no eligible target",
			broker: &doctorStubBroker{
				candidates: []webmcp.BrowserCandidate{{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3", Loopback: true}},
				targets:    []webmcp.Target{{BrowserID: "browser-a", ID: "tab-a", Type: "page", Eligible: false, EligibilityReason: "webmcp disabled"}},
			},
			wantCode: string(webmcp.ErrorNoEligibleTab),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir := writeDoctorConfig(t, tc.configYAML)
			factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
				return WebMCPDoctorRuntime{Broker: tc.broker}, nil
			}
			root, stdout, _ := executeDoctorCommand(t, configDir, factory, "--json")
			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Fatal("doctor unexpectedly succeeded")
			}
			report := decodeDoctorReport(t, stdout.String())
			if report.Error == nil || report.Error.Code != tc.wantCode {
				t.Fatalf("report error = %+v, want %s", report.Error, tc.wantCode)
			}
		})
	}
}

func TestWebMCPDoctorClassifiesAmbiguousAndDeniedSelection(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string
		targets    []webmcp.Target
		wantCode   string
	}{
		{
			name: "ambiguous",
			configYAML: `
browser:
  selection:
    browser: browser-a
    auto_select: single
`,
			targets: []webmcp.Target{
				{BrowserID: "browser-a", ID: "tab-a", Type: "page", Origin: "https://a.test", Eligible: true},
				{BrowserID: "browser-a", ID: "tab-b", Type: "page", Origin: "https://b.test", Eligible: true},
			},
			wantCode: string(webmcp.ErrorAmbiguousTab),
		},
		{
			name: "denied",
			configYAML: `
browser:
  selection:
    browser: browser-a
  policy:
    denied_origins: [https://blocked.test]
`,
			targets:  []webmcp.Target{{BrowserID: "browser-a", ID: "tab-a", Type: "page", Origin: "https://blocked.test", Eligible: true}},
			wantCode: string(webmcp.ErrorOriginDenied),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir := writeDoctorConfig(t, tc.configYAML)
			broker := &doctorStubBroker{
				candidates: []webmcp.BrowserCandidate{{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3", Loopback: true}},
				targets:    tc.targets,
			}
			factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
				return WebMCPDoctorRuntime{Broker: broker}, nil
			}
			root, stdout, _ := executeDoctorCommand(t, configDir, factory, "--json")
			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Fatal("doctor unexpectedly succeeded")
			}
			report := decodeDoctorReport(t, stdout.String())
			if report.Error == nil || report.Error.Code != tc.wantCode {
				t.Fatalf("report error = %+v, want %s", report.Error, tc.wantCode)
			}
		})
	}
}

func TestWebMCPDoctorReportsUnsupportedWebMCPDisconnectAndCleanup(t *testing.T) {
	tests := []struct {
		name     string
		broker   *doctorStubBroker
		wantCode string
	}{
		{
			name: "unsupported",
			broker: &doctorStubBroker{
				candidates: []webmcp.BrowserCandidate{{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3", Loopback: true}},
				targets:    []webmcp.Target{{BrowserID: "browser-a", ID: "tab-a", Type: "page", Origin: "https://fixture.test", Eligible: true}},
				selectErr:  webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "unsupported", map[string]any{"required_capability": "webmcp"}),
			},
			wantCode: string(webmcp.ErrorUnsupportedWebMCP),
		},
		{
			name: "disconnect",
			broker: &doctorStubBroker{
				discoverErr: webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, "disconnected", map[string]any{"phase": "discovery"}),
			},
			wantCode: string(webmcp.ErrorBrowserDisconnected),
		},
		{
			name: "cleanup",
			broker: &doctorStubBroker{
				candidates: []webmcp.BrowserCandidate{{ID: "browser-a", Product: "Chrome/Test", Protocol: "1.3", Loopback: true}},
				targets:    []webmcp.Target{{BrowserID: "browser-a", ID: "tab-a", Type: "page", Origin: "https://fixture.test", Eligible: true}},
				selected:   webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}, Generation: 1, Connected: true, Ready: true},
				catalog:    webmcp.ToolCatalogSnapshot{Context: webmcp.PageContext{Key: webmcp.PageKey{BrowserID: "browser-a", TargetID: "tab-a"}, Generation: 1, Connected: true, Ready: true}, Generation: 1},
				closeErr:   errors.New("cleanup failed"),
			},
			wantCode: doctorErrorCleanupFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configYAML := "\nbrowser:\n  selection:\n    browser: browser-a\n    tab: tab-a\n"
			configDir := writeDoctorConfig(t, configYAML)
			factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
				return WebMCPDoctorRuntime{Broker: tc.broker}, nil
			}
			root, stdout, _ := executeDoctorCommand(t, configDir, factory, "--json")
			err := root.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("doctor unexpectedly succeeded")
			}
			report := decodeDoctorReport(t, stdout.String())
			if report.Error == nil || report.Error.Code != tc.wantCode {
				t.Fatalf("report error = %+v, want %s", report.Error, tc.wantCode)
			}
			if tc.name == "cleanup" && tc.broker.closeCalls != 1 {
				t.Fatalf("cleanup calls = %d, want one", tc.broker.closeCalls)
			}
		})
	}
}

func executeDoctorCommand(t *testing.T, configDir string, factory WebMCPDoctorFactory, args ...string) (*cobra.Command, *strings.Builder, *strings.Builder) {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	command := NewWebMCPDoctorCommand(globalFlags, factory).Generate()
	var stdout, stderr strings.Builder
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	return command, &stdout, &stderr
}

func decodeDoctorReport(t *testing.T, output string) WebMCPDoctorReport {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var report WebMCPDoctorReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatalf("decode doctor JSON: %v; output=%q", err, output)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("doctor JSON contains more than one result: err=%v extra=%#v output=%q", err, extra, output)
	}
	return report
}

func doctorCheckByName(report WebMCPDoctorReport, name string) WebMCPDoctorCheck {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return WebMCPDoctorCheck{}
}

func writeDoctorConfig(t *testing.T, browserYAML string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(browserYAML), 0600); err != nil {
		t.Fatalf("write doctor config: %v", err)
	}
	return dir
}

func hasTestkitOperation(operations []testkit.Operation, want testkit.OperationKind) bool {
	for _, operation := range operations {
		if operation.Kind == want {
			return true
		}
	}
	return false
}

type doctorDiscoverer struct {
	candidates []webmcp.BrowserCandidate
}

func (d doctorDiscoverer) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return append([]webmcp.BrowserCandidate(nil), d.candidates...), nil
}

type doctorStubBroker struct {
	candidates  []webmcp.BrowserCandidate
	discoverErr error
	targets     []webmcp.Target
	selectErr   error
	selected    webmcp.PageContext
	catalog     webmcp.ToolCatalogSnapshot
	closeErr    error
	closeCalls  int
}

func (b *doctorStubBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if b.discoverErr != nil {
		return nil, b.discoverErr
	}
	return append([]webmcp.BrowserCandidate(nil), b.candidates...), nil
}

func (b *doctorStubBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return append([]webmcp.Target(nil), b.targets...), nil
}

func (b *doctorStubBroker) Select(context.Context, webmcp.TargetSelector) (webmcp.PageContext, error) {
	if b.selectErr != nil {
		return webmcp.PageContext{}, b.selectErr
	}
	return b.selected, nil
}

func (b *doctorStubBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return b.selected, nil
}

func (b *doctorStubBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	return b.catalog, nil
}

func (b *doctorStubBroker) Invoke(context.Context, webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	return webmcp.InvokeResult{}, nil
}

func (b *doctorStubBroker) Cancel(context.Context, webmcp.CancelRequest) error { return nil }

func (b *doctorStubBroker) Watch(context.Context) <-chan webmcp.BrokerEvent {
	channel := make(chan webmcp.BrokerEvent)
	close(channel)
	return channel
}

func (b *doctorStubBroker) Close() error {
	b.closeCalls++
	return b.closeErr
}
