package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func TestProbeScenarioV2BrowserExecutorDefaultsToHermetic(t *testing.T) {
	command := NewProbeRunCommand()
	if command.BrowserExecutorMode != ProbeScenarioV2BrowserExecutorHermetic {
		t.Fatalf("default browser executor = %q, want hermetic", command.BrowserExecutorMode)
	}

	factoryCalled := false
	executor, err := newProbeScenarioV2Executor(probe.ScenarioV2{}, WithProbeScenarioV2BrowserExecutorFactory(func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		factoryCalled = true
		return WebMCPDoctorRuntime{}, nil
	}))
	if err != nil {
		t.Fatalf("construct default executor: %v", err)
	}
	if executor.mode != ProbeScenarioV2BrowserExecutorHermetic {
		t.Fatalf("executor mode = %q, want hermetic", executor.mode)
	}
	if factoryCalled {
		t.Fatal("default executor consulted the real browser factory")
	}
}

func TestProbeRunCommandBrowserExecutorFlagIsExplicitAndTyped(t *testing.T) {
	command := NewProbeRunCommand()
	generated := command.Generate()
	if err := generated.Flags().Set("browser-executor", "real"); err != nil {
		t.Fatalf("set real browser executor: %v", err)
	}
	if command.BrowserExecutorMode != ProbeScenarioV2BrowserExecutorReal {
		t.Fatalf("selected browser executor = %q, want real", command.BrowserExecutorMode)
	}
	if err := generated.Flags().Set("browser-executor", "network"); err == nil {
		t.Fatal("invalid browser executor mode was accepted")
	}
}

func TestProbeScenarioV2RealExecutorUsesSharedBrowserContract(t *testing.T) {
	factory, factoryCalls, closeCalls := probeScenarioV2TestFactory(t, json.RawMessage(`{"value":"real"}`))
	scenario := probe.ScenarioV2{
		SchemaVersion: probe.ScenarioV2Version,
		ID:            "real-browser-contract",
		Name:          "real browser contract",
		// Real mode must not try to load a fixture or silently fall back to it.
		BrowserFixture: "missing-browser.fixture.json",
		Steps: []probe.ScenarioV2Step{
			{Type: probe.ScenarioV2StepBrowserConnect, BrowserID: "fixture-browser"},
			{Type: probe.ScenarioV2StepBrowserDiscover, BrowserID: "fixture-browser"},
			{Type: probe.ScenarioV2StepBrowserSelect, BrowserID: "fixture-browser", TargetID: "tab-1"},
			{Type: probe.ScenarioV2StepWebMCPWaitReady},
			{Type: probe.ScenarioV2StepWebMCPListTools, IncludeSchemas: true, HasIncludeSchemas: true},
			{Type: probe.ScenarioV2StepClose},
		},
		Expectations: []probe.ScenarioV2Expectation{
			{Type: probe.ScenarioV2ExpectationBrowserCountEquals, Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationSelectedTabEquals, TargetID: "tab-1"},
			{Type: probe.ScenarioV2ExpectationToolCatalogContains, Name: "read_state"},
			{Type: probe.ScenarioV2ExpectationPageStateEquals, Path: "$.value", Value: json.RawMessage(`"real"`)},
			{Type: probe.ScenarioV2ExpectationBrowserConnectionClosed},
		},
	}
	result := executeProbeScenarioV2(context.Background(), probeScenarioV2Selection{Scenario: scenario}, t.TempDir()+"/evidence",
		WithProbeScenarioV2BrowserExecutorMode(ProbeScenarioV2BrowserExecutorReal),
		WithProbeScenarioV2BrowserExecutorFactory(factory),
	)
	if !result.Pass {
		t.Fatalf("real browser result failed: %+v", result)
	}
	if result.BrowserExecutor != ProbeScenarioV2BrowserExecutorReal {
		t.Fatalf("reported browser executor = %q, want real", result.BrowserExecutor)
	}
	if result.Evidence == nil || !result.ObjectiveEvidence.Verified {
		t.Fatalf("real browser evidence = %+v, objective = %+v; want verified v2 bundle", result.Evidence, result.ObjectiveEvidence)
	}
	if *factoryCalls != 1 || *closeCalls != 1 {
		t.Fatalf("factory/close calls = %d/%d, want 1/1", *factoryCalls, *closeCalls)
	}
}

func TestProbeScenarioV2RealUnavailableIsClassifiedAndDoesNotLeakCause(t *testing.T) {
	closeCalls := 0
	secret := "ws://user:password@browser.example/devtools?token=secret"
	factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		return WebMCPDoctorRuntime{Close: func() error {
			closeCalls++
			return nil
		}}, webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, secret, nil)
	}
	scenario := probe.ScenarioV2{
		SchemaVersion: probe.ScenarioV2Version,
		ID:            "real-browser-unavailable",
		Steps:         []probe.ScenarioV2Step{{Type: probe.ScenarioV2StepBrowserConnect}},
	}
	result := executeProbeScenarioV2(context.Background(), probeScenarioV2Selection{Scenario: scenario}, "",
		WithProbeScenarioV2BrowserExecutorMode(ProbeScenarioV2BrowserExecutorReal),
		WithProbeScenarioV2BrowserExecutorFactory(factory),
	)
	if result.Pass || result.BrowserExecutor != ProbeScenarioV2BrowserExecutorReal {
		t.Fatalf("unexpected unavailable result: %+v", result)
	}
	if result.ErrorCode != string(webmcp.ErrorEndpointUnreachable) {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, webmcp.ErrorEndpointUnreachable)
	}
	if strings.Contains(result.Error, secret) || strings.Contains(result.Error, "password") {
		t.Fatalf("real prerequisite error leaked cause: %q", result.Error)
	}
	if !strings.Contains(result.Error, "verify the configured browser endpoint") {
		t.Fatalf("real prerequisite error is not actionable: %q", result.Error)
	}
	if closeCalls != 1 {
		t.Fatalf("factory runtime close calls = %d, want 1", closeCalls)
	}

	_, err := newProbeScenarioV2Executor(scenario,
		WithProbeScenarioV2BrowserExecutorMode(ProbeScenarioV2BrowserExecutorReal),
		WithProbeScenarioV2BrowserExecutorFactory(factory),
	)
	var classified *ProbeScenarioV2BrowserExecutorError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorEndpointUnreachable {
		t.Fatalf("typed prerequisite error = %v, want endpoint_unreachable classification", err)
	}
	if !errors.Is(err, ErrProbeScenarioV2RealAdapterUnavailable) {
		t.Fatalf("typed prerequisite error does not unwrap availability sentinel: %v", err)
	}
	if closeCalls != 2 {
		t.Fatalf("second factory runtime close calls = %d, want 2", closeCalls)
	}
}

func probeScenarioV2TestFactory(t *testing.T, pageState json.RawMessage) (WebMCPDoctorFactory, *int, *int) {
	t.Helper()
	script := testkit.BrowserScript{
		Version: testkit.BrowserScriptVersion,
		Endpoint: testkit.BrowserEndpoint{
			Version: testkit.EndpointVersionInfo{
				Browser:              "Chrome/Injected",
				ProtocolVersion:      "1.3",
				WebSocketDebuggerURL: "ws://injected/browser",
			},
			Targets: []testkit.BrowserTarget{{
				ID:                   "tab-1",
				Type:                 "page",
				Title:                "Injected browser",
				URL:                  "https://injected.test/",
				WebSocketDebuggerURL: "ws://injected/page/tab-1",
			}},
		},
		Operations: []testkit.BrowserScriptOperation{
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableLifecycle}, Result: json.RawMessage(`{}`)},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Result: json.RawMessage(`{}`), Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolsAdded,
				Tools: []testkit.ToolDescriptor{{
					Name:        "read_state",
					Description: "Read injected state",
					FrameID:     "frame-1",
					InputSchema: json.RawMessage(`{"type":"object"}`),
				}},
			}}},
		},
	}
	runtime, err := testkit.NewBrowserScriptRuntime(script)
	if err != nil {
		t.Fatalf("construct injected browser runtime: %v", err)
	}
	adapter, err := testkit.NewBrowserScriptAdapter(script, runtime)
	if err != nil {
		t.Fatalf("construct injected browser adapter: %v", err)
	}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        adapter,
		Discoverer:     adapter,
		IDs:            testkit.NewDeterministicIDs(),
		Clock:          testkit.NewFakeClock(0),
		Timers:         testkit.NewFakeClock(0),
		Ownership:      webmcp.TargetOwnershipHarnessOwned,
		ToolRefFactory: webmcp.StableToolRef,
	})
	factoryCalls := 0
	closeCalls := 0
	factory := func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		factoryCalls++
		return WebMCPDoctorRuntime{
			Broker: broker,
			PageState: func(context.Context) (json.RawMessage, error) {
				return pageState, nil
			},
			Close: func() error {
				closeCalls++
				return runtime.Complete()
			},
		}, nil
	}
	return factory, &factoryCalls, &closeCalls
}
