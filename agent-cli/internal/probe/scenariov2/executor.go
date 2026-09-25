package scenariov2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

type invocation struct {
	PublicID webmcp.InvocationID
	ToolRef  webmcp.ToolRef
	Name     string
	Input    json.RawMessage
	Result   webmcp.InvokeResult
	Err      error
}

// statefulBroker is the shared browser execution contract. Both the hermetic
// testkit broker and the production StatefulBroker satisfy this extension of
// the public broker interface, so mode selection does not create a second
// step grammar or evidence projection.
type statefulBroker interface {
	webmcp.Broker
	SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	WaitInvocation(context.Context, webmcp.InvocationID) (webmcp.InvokeResult, error)
	PendingInvocations() []webmcp.Invocation
}

type executor struct {
	scenario      probe.ScenarioV2
	mode          BrowserExecutorMode
	replayService runtimeReplay.Service
	analyze       ProviderAnalyzer

	clock   *testkit.FakeClock
	ids     *testkit.DeterministicIDSource
	runtime *testkit.BrowserScriptRuntime
	adapter *testkit.BrowserScriptAdapter
	broker  statefulBroker

	browserClose     func() error
	browserCloseOnce sync.Once
	browserCloseErr  error
	browserNavigate  func(context.Context, string) error
	browserPageState func(context.Context) (json.RawMessage, error)
	pageState        json.RawMessage
	pageStateSet     bool

	recorder    *testkit.Recorder
	eventOutput bytes.Buffer

	discovered []webmcp.BrowserCandidate
	selected   webmcp.PageContext
	catalog    webmcp.ToolCatalogSnapshot
	hasCatalog bool

	invocations         []invocation
	provider            *probe.ObservationSnapshot
	providerReport      *runtimeReplay.CaptureProbeObservation
	providerPath        string
	providerSteps       []probe.Step
	closed              bool
	cleanupErr          error
	objectiveDivergence *objective.Divergence
}

func newExecutor(ctx context.Context, scenario probe.ScenarioV2, runner Runner, options ...BrowserExecutorOption) (*executor, error) {
	resolved, err := resolveBrowserExecutorOptions(options...)
	if err != nil {
		return &executor{scenario: scenario, mode: BrowserExecutorHermetic, replayService: runner.Replay, analyze: runner.Analyze}, err
	}
	e := &executor{scenario: scenario, mode: resolved.Mode, replayService: runner.Replay, analyze: runner.Analyze}
	e.clock = testkit.NewFakeClock(0)
	e.ids = testkit.NewDeterministicIDSource("probe")
	if err := e.admitProviderFixture(ctx); err != nil {
		return e, err
	}
	if resolved.Mode == BrowserExecutorReal {
		err = e.composeReal(resolved)
	} else if scenario.BrowserFixture != "" {
		err = e.composeHermetic()
	}
	if err != nil {
		return e, err
	}
	e.recorder, err = testkit.NewRecorder(&e.eventOutput,
		testkit.WithClock(e.clock),
		testkit.WithIDSource(e.ids),
		testkit.WithRedaction(testkit.RedactionPolicy{URLQuery: true, URLFragment: true}),
	)
	if err != nil {
		return e, fmt.Errorf("create browser evidence recorder: %w", err)
	}
	return e, nil
}

func (e *executor) admitProviderFixture(ctx context.Context) error {
	if e.scenario.ProviderFixture == "" {
		return nil
	}
	e.providerPath = e.scenario.ProviderFixturePath
	if e.replayService == nil {
		return errors.New("replay service is required for provider fixtures")
	}
	if _, err := e.replayService.InspectCapture(context.WithoutCancel(ctx), e.scenario.ProviderFixturePath); err != nil {
		return fmt.Errorf("invalid provider fixture %q: %w", e.scenario.ProviderFixture, err)
	}
	return nil
}

func (e *executor) composeReal(resolved BrowserExecutorOptions) error {
	if resolved.ConfigError != nil {
		return newBrowserExecutorError(resolved.Mode, "configuration", webmcp.ErrorBrowserProtocol, resolved.ConfigError)
	}
	if resolved.Factory == nil {
		return newBrowserExecutorError(resolved.Mode, phaseComposition, webmcp.ErrorUnsupportedWebMCP, ErrRealAdapterUnavailable)
	}
	runtime, err := resolved.Factory(resolved.Browser)
	if err != nil {
		return newBrowserExecutorError(resolved.Mode, "construction", webmcp.ErrorEndpointNotFound, errors.Join(err, closeRealRuntime(runtime)))
	}
	if runtime.Broker == nil {
		return newBrowserExecutorError(resolved.Mode, phaseComposition, webmcp.ErrorUnsupportedWebMCP, errors.Join(errors.New("real runtime has no stateful broker"), closeRealRuntime(runtime)))
	}
	broker, ok := runtime.Broker.(statefulBroker)
	if !ok {
		return newBrowserExecutorError(resolved.Mode, phaseComposition, webmcp.ErrorUnsupportedWebMCP, errors.Join(errors.New("real runtime broker does not implement the v2 stateful seam"), closeRealRuntime(runtime)))
	}
	e.broker = broker
	e.browserClose = func() error { return closeRealRuntime(runtime) }
	e.browserNavigate = runtime.Navigate
	e.browserPageState = runtime.PageState
	return nil
}

func closeRealRuntime(runtime RealRuntime) error {
	if runtime.Close == nil {
		return nil
	}
	return runtime.Close()
}

func (e *executor) composeHermetic() error {
	script, err := testkit.LoadBrowserScriptFile(e.scenario.BrowserFixturePath)
	if err != nil {
		return fmt.Errorf("load browser fixture %q: %w", e.scenario.BrowserFixture, err)
	}
	state, err := testkit.NewFixtureStateOracle(map[string]any{})
	if err != nil {
		return fmt.Errorf("create fixture page-state oracle: %w", err)
	}
	runtime, err := testkit.NewBrowserScriptRuntime(script,
		testkit.WithFixtureClock(e.clock),
		testkit.WithFixtureIDSource(e.ids),
		testkit.WithStateOracle(state),
	)
	if err != nil {
		return fmt.Errorf("create browser fixture runtime: %w", err)
	}
	adapter, err := testkit.NewBrowserScriptAdapter(script, runtime)
	if err != nil {
		return fmt.Errorf("create browser fixture adapter: %w", err)
	}
	e.runtime = runtime
	e.adapter = adapter
	e.broker = webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:        adapter,
		Discoverer:     adapter,
		IDs:            e.ids,
		Clock:          e.clock,
		Timers:         e.clock,
		Ownership:      webmcp.TargetOwnershipHarnessOwned,
		ToolRefFactory: webmcp.StableToolRef,
	})
	return nil
}
