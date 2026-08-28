package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// probeScenarioV2Result is intentionally compatible with the legacy result
// vocabulary while retaining the stable v2 scenario identity and typed
// browser execution evidence. The richer acceptance evidence is finalized by
// the later v2 evidence story; this result is the command's execution verdict.
type probeScenarioV2Result struct {
	ID                 string                             `json:"id"`
	Name               string                             `json:"name"`
	SchemaVersion      string                             `json:"schema_version"`
	Pass               bool                               `json:"pass"`
	Stuck              bool                               `json:"stuck,omitempty"`
	StuckReason        string                             `json:"stuck_reason,omitempty"`
	StepCount          int                                `json:"step_count"`
	Steps              []probe.ScenarioV2Step             `json:"steps"`
	Expectations       []probe.ScenarioV2Expectation      `json:"expectations"`
	ExpectationResults []probeScenarioV2ExpectationResult `json:"expectation_results"`
	Ticks              probe.LogicalTime                  `json:"ticks"`
	Frames             int                                `json:"frames"`
	TerminalReason     string                             `json:"terminal_reason,omitempty"`
	TerminalProvenance string                             `json:"terminal_provenance,omitempty"`
	OutputState        string                             `json:"output_state,omitempty"`
	Error              string                             `json:"error,omitempty"`
	InputDropCount     uint64                             `json:"input_drop_count"`
	OutputDropCount    uint64                             `json:"output_drop_count"`
}

type probeScenarioV2ExpectationResult struct {
	Index    int                             `json:"index"`
	Type     probe.ScenarioV2ExpectationType `json:"type"`
	Passed   bool                            `json:"passed"`
	Expected string                          `json:"expected,omitempty"`
	Actual   string                          `json:"actual,omitempty"`
	Error    string                          `json:"error,omitempty"`
}

type probeScenarioV2Selection struct {
	Scenario  probe.ScenarioV2
	Selection string
	Err       error
}

type probeScenarioV2Invocation struct {
	PublicID webmcp.InvocationID
	ToolRef  webmcp.ToolRef
	Name     string
	Input    json.RawMessage
	Result   webmcp.InvokeResult
	Err      error
}

type probeScenarioV2Executor struct {
	scenario probe.ScenarioV2

	clock   *testkit.FakeClock
	ids     *testkit.DeterministicIDSource
	runtime *testkit.BrowserScriptRuntime
	adapter *testkit.BrowserScriptAdapter
	broker  *webmcp.StatefulBroker

	recorder    *testkit.Recorder
	eventOutput bytes.Buffer

	discovered []webmcp.BrowserCandidate
	selected   webmcp.PageContext
	catalog    webmcp.ToolCatalogSnapshot
	hasCatalog bool

	invocations     []probeScenarioV2Invocation
	provider        *probe.ObservationSnapshot
	providerCapture gatewaytesting.SessionCapture
	providerPath    string
	providerSteps   []probe.Step
	closed          bool
}

func loadProbeScenarioV2Selections(selections []string) ([]probeScenarioV2Selection, error) {
	if len(selections) == 0 {
		return nil, fmt.Errorf("no probe scenarios selected; pass scenario paths as arguments or repeat --scenario")
	}
	result := make([]probeScenarioV2Selection, 0, len(selections))
	seen := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		isV2, err := probeScenarioFileIsV2(selection)
		if err != nil {
			return nil, err
		}
		if !isV2 {
			return nil, fmt.Errorf("probe.scenario.v2 execution cannot mix selection %q with a legacy or registered scenario", selection)
		}
		scenario, loadErr := loadProbeScenarioV2File(selection)
		if loadErr != nil {
			// Keep a failed result line for a selected document whose envelope was
			// recognized but whose fixtures or typed values are invalid.
			result = append(result, probeScenarioV2Selection{Selection: selection, Err: fmt.Errorf("load probe scenario %q: %w", selection, loadErr)})
			continue
		}
		key := scenario.ID + "\x00" + scenario.Name
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, probeScenarioV2Selection{Selection: selection, Scenario: scenario})
	}
	return result, nil
}

func probeScenarioFileIsV2(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("load probe scenario %q: %w", path, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("load probe scenario %q: path is a directory", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read probe scenario %q: %w", path, err)
	}
	return hasScenarioV2Envelope(data), nil
}

func (c *ProbeRunCommand) runScenarioV2(cmd interface {
	Context() context.Context
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}, selections []string) error {
	entries, err := loadProbeScenarioV2Selections(selections)
	if err != nil {
		return err
	}
	resultsOut, summaryOut, closeOutputs, err := c.openProbeOutputs(cmd)
	if err != nil {
		return err
	}
	defer closeOutputs()

	summary := probe.RunSummary{Total: len(entries)}
	for _, entry := range entries {
		result := executeProbeScenarioV2(cmd.Context(), entry)
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return fmt.Errorf("encode result for scenario %q: %w", result.Name, encodeErr)
		}
		if _, writeErr := fmt.Fprintf(resultsOut, "%s\n", encoded); writeErr != nil {
			return fmt.Errorf("write result for scenario %q: %w", result.Name, writeErr)
		}
		if result.Pass {
			summary.Passed++
		} else {
			summary.Failed++
		}
		if result.Stuck {
			summary.Stuck++
		}
	}
	summary.Status = probe.StatusFail
	if summary.Failed == 0 && summary.Total > 0 {
		summary.Status = probe.StatusPass
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode v2 probe summary: %w", err)
	}
	if _, err := fmt.Fprintf(summaryOut, "%s\n", encoded); err != nil {
		return fmt.Errorf("write v2 probe summary: %w", err)
	}
	if !c.JSONOut {
		fmt.Fprintf(cmd.ErrOrStderr(), "probe: %d/%d scenarios passed (%s)\n", summary.Passed, summary.Total, summary.Status)
	}
	if summary.Failed > 0 {
		return fmt.Errorf("%d of %d probe scenarios failed", summary.Failed, summary.Total)
	}
	return nil
}

func (c *ProbeRunCommand) openProbeOutputs(cmd interface {
	OutOrStdout() io.Writer
	ErrOrStderr() io.Writer
}) (io.Writer, io.Writer, func(), error) {
	resultsOut := cmd.OutOrStdout()
	var resultFile *os.File
	if c.OutPath != "" {
		file, err := os.Create(c.OutPath)
		if err != nil {
			return nil, nil, func() {}, fmt.Errorf("open --out %q: %w", c.OutPath, err)
		}
		resultFile = file
		resultsOut = file
	}
	summaryOut := cmd.ErrOrStderr()
	var summaryFile *os.File
	if c.SummaryPath != "" {
		file, err := os.Create(c.SummaryPath)
		if err != nil {
			if resultFile != nil {
				_ = resultFile.Close()
			}
			return nil, nil, func() {}, fmt.Errorf("open --summary %q: %w", c.SummaryPath, err)
		}
		summaryFile = file
		summaryOut = file
	}
	closeOutputs := func() {
		if summaryFile != nil {
			_ = summaryFile.Close()
		}
		if resultFile != nil {
			_ = resultFile.Close()
		}
	}
	return resultsOut, summaryOut, closeOutputs, nil
}

func executeProbeScenarioV2(parent context.Context, entry probeScenarioV2Selection) (result probeScenarioV2Result) {
	result = probeScenarioV2Result{
		ID:            entry.Scenario.ID,
		Name:          entry.Scenario.Name,
		SchemaVersion: entry.Scenario.SchemaVersion,
		StepCount:     len(entry.Scenario.Steps),
		Steps:         entry.Scenario.Steps,
		Expectations:  entry.Scenario.Expectations,
		Pass:          false,
	}
	if result.Name == "" {
		result.Name = result.ID
	}
	if entry.Err != nil {
		result.ID = entry.Selection
		result.Name = entry.Selection
		result.SchemaVersion = probe.ScenarioV2Version
		result.Error = entry.Err.Error()
		return result
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, probeScenarioDeadline)
	defer cancel()
	executor, err := newProbeScenarioV2Executor(entry.Scenario)
	if err == nil {
		err = executor.execute(ctx)
	}
	if err != nil {
		result.Error = err.Error()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Stuck = true
			result.StuckReason = "v2 scenario execution exceeded the deadguard deadline"
		}
		if executor != nil {
			executor.cleanup()
			result = executor.populateResult(result)
		}
		return result
	}
	result = executor.populateResult(result)
	return result
}

func newProbeScenarioV2Executor(scenario probe.ScenarioV2) (*probeScenarioV2Executor, error) {
	executor := &probeScenarioV2Executor{scenario: scenario}
	executor.clock = testkit.NewFakeClock(0)
	executor.ids = testkit.NewDeterministicIDSource("probe")
	if scenario.BrowserFixture != "" {
		script, err := testkit.LoadBrowserScriptFile(scenario.BrowserFixturePath)
		if err != nil {
			return executor, fmt.Errorf("load browser fixture %q: %w", scenario.BrowserFixture, err)
		}
		state, err := testkit.NewFixtureStateOracle(map[string]any{})
		if err != nil {
			return executor, fmt.Errorf("create fixture page-state oracle: %w", err)
		}
		runtime, err := testkit.NewBrowserScriptRuntime(script,
			testkit.WithFixtureClock(executor.clock),
			testkit.WithFixtureIDSource(executor.ids),
			testkit.WithStateOracle(state),
		)
		if err != nil {
			return executor, fmt.Errorf("create browser fixture runtime: %w", err)
		}
		adapter, err := testkit.NewBrowserScriptAdapter(script, runtime)
		if err != nil {
			return executor, fmt.Errorf("create browser fixture adapter: %w", err)
		}
		executor.runtime = runtime
		executor.adapter = adapter
		executor.broker = webmcp.NewBroker(webmcp.BrokerOptions{
			Runtime:        adapter,
			Discoverer:     adapter,
			IDs:            executor.ids,
			Clock:          executor.clock,
			Timers:         executor.clock,
			Ownership:      webmcp.TargetOwnershipHarnessOwned,
			ToolRefFactory: webmcp.StableToolRef,
		})
	}
	if scenario.ProviderFixture != "" {
		validationErrs := gatewaytesting.ValidateSessionCaptureFile(scenario.ProviderFixturePath)
		if len(validationErrs) > 0 {
			return executor, fmt.Errorf("invalid provider fixture %q: %s", scenario.ProviderFixture, joinSessionFixtureErrors(validationErrs))
		}
		capture, err := gatewaytesting.LoadSessionCapture(scenario.ProviderFixturePath)
		if err != nil {
			return executor, fmt.Errorf("load provider fixture %q: %w", scenario.ProviderFixture, err)
		}
		executor.providerCapture = capture
		executor.providerPath = scenario.ProviderFixturePath
	}
	var err error
	executor.recorder, err = testkit.NewRecorder(&executor.eventOutput,
		testkit.WithClock(executor.clock),
		testkit.WithIDSource(executor.ids),
	)
	if err != nil {
		return executor, fmt.Errorf("create browser evidence recorder: %w", err)
	}
	return executor, nil
}

func (e *probeScenarioV2Executor) execute(ctx context.Context) error {
	if e == nil {
		return errors.New("v2 executor is nil")
	}
	for index, step := range e.scenario.Steps {
		if err := e.dispatchStep(ctx, step); err != nil {
			return fmt.Errorf("step %d (%s): %w", index, step.Type, err)
		}
	}
	if err := e.resolveInvocations(ctx); err != nil {
		return fmt.Errorf("resolve browser invocations: %w", err)
	}
	if e.providerPath != "" {
		providerScenario := scenarioV2ProviderScenario(e.scenario, e.providerSteps)
		observation, err := observationFromSessionCapture(ctx, providerScenario, e.providerCapture, e.providerPath, false)
		if err != nil {
			return fmt.Errorf("replay provider fixture %q: %w", e.scenario.ProviderFixture, err)
		}
		e.provider = &observation
	} else if len(e.providerSteps) > 0 {
		return errors.New("provider session steps require provider_fixture")
	}
	if e.broker != nil {
		if err := e.broker.Close(); err != nil {
			return fmt.Errorf("close browser broker: %w", err)
		}
		e.closed = true
	}
	if e.runtime != nil {
		if err := e.runtime.Complete(); err != nil {
			return fmt.Errorf("complete browser fixture: %w", err)
		}
	}
	return nil
}

func (e *probeScenarioV2Executor) cleanup() {
	if e == nil {
		return
	}
	if e.broker != nil {
		_ = e.broker.Close()
		e.closed = true
		return
	}
	if e.adapter != nil {
		_ = e.adapter.Disconnect(context.Background())
		e.closed = true
	}
}

func (e *probeScenarioV2Executor) dispatchStep(ctx context.Context, step probe.ScenarioV2Step) error {
	switch step.Type {
	case probe.ScenarioV2StepBrowserConnect, probe.ScenarioV2StepBrowserDiscover:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		candidates, err := e.broker.Discover(ctx, webmcp.DiscoverOptions{BrowserID: webmcp.BrowserID(step.BrowserID), ExplicitOnly: true})
		if err != nil {
			return err
		}
		e.discovered = candidates
		return nil
	case probe.ScenarioV2StepBrowserSelect:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		browserID := webmcp.BrowserID(step.BrowserID)
		if browserID == "" && len(e.discovered) == 1 {
			browserID = e.discovered[0].ID
		}
		page, err := e.broker.SelectWithOptions(ctx, webmcp.TargetSelector{BrowserID: browserID, TargetID: webmcp.TargetID(step.TargetID)}, webmcp.SelectOptions{Activate: step.Activate})
		if err != nil {
			return err
		}
		e.selected = page
		return nil
	case probe.ScenarioV2StepBrowserActivate:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		browserID := webmcp.BrowserID(step.BrowserID)
		if browserID == "" {
			browserID = e.selected.Key.BrowserID
		}
		targetID := webmcp.TargetID(step.TargetID)
		if targetID == "" {
			targetID = e.selected.Key.TargetID
		}
		page, err := e.broker.SelectWithOptions(ctx, webmcp.TargetSelector{BrowserID: browserID, TargetID: targetID}, webmcp.SelectOptions{Activate: true})
		if err != nil {
			return err
		}
		e.selected = page
		return nil
	case probe.ScenarioV2StepWebMCPWaitReady:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		page, err := e.broker.Selected(ctx)
		if err != nil {
			return err
		}
		e.selected = page
		return nil
	case probe.ScenarioV2StepWebMCPListTools:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		catalog, err := e.broker.ListTools(ctx, webmcp.ListToolsOptions{
			Refresh:        step.Refresh,
			NameContains:   step.NameContains,
			IncludeSchemas: step.IncludeSchemas,
			FrameID:        webmcp.FrameID(step.FrameID),
		})
		if err != nil {
			return err
		}
		e.catalog = catalog
		e.hasCatalog = true
		return nil
	case probe.ScenarioV2StepWebMCPInvoke:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		input := json.RawMessage(step.InputJSON)
		result, err := e.broker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: webmcp.ToolRef(step.ToolRef), Input: input, Reason: step.Reason})
		invocation := probeScenarioV2Invocation{PublicID: result.InvocationID, ToolRef: webmcp.ToolRef(step.ToolRef), Input: append(json.RawMessage(nil), input...), Result: result, Err: err}
		if descriptor, ok := e.toolForRef(step.ToolRef); ok {
			invocation.Name = descriptor.Name
		}
		e.invocations = append(e.invocations, invocation)
		if invocation.Err != nil {
			if e.scenarioExpects(probe.ScenarioV2ExpectationStaleToolRejected) && isStaleToolError(invocation.Err) {
				return nil
			}
			return invocation.Err
		}
		return nil
	case probe.ScenarioV2StepWebMCPCancel:
		if e.broker == nil {
			return errors.New("browser fixture is not configured")
		}
		return e.broker.Cancel(ctx, webmcp.CancelRequest{InvocationID: webmcp.InvocationID(step.InvocationID), Reason: step.Reason})
	case probe.ScenarioV2StepBrowserNavigateFixture:
		if e.adapter == nil {
			return errors.New("browser fixture is not configured")
		}
		targetURL := step.URL
		if step.FixturePath != "" {
			script, err := testkit.LoadBrowserScriptFile(step.FixturePath)
			if err != nil {
				return fmt.Errorf("load navigation fixture: %w", err)
			}
			if len(script.Endpoint.Targets) == 0 {
				return errors.New("navigation fixture has no target")
			}
			targetURL = script.Endpoint.Targets[0].URL
		}
		return e.adapter.Navigate(ctx, targetURL)
	case probe.ScenarioV2StepBrowserDisconnect, probe.ScenarioV2StepCloseTab:
		if e.adapter == nil {
			return errors.New("browser fixture is not configured")
		}
		// Defer physical teardown until terminal invocation evidence has been
		// drained. The broker keeps its bounded terminal cache available until
		// Close, which lets a preceding invoke and this lifecycle step coexist.
		e.closed = true
		return nil
	case probe.ScenarioV2StepSendText:
		e.providerSteps = append(e.providerSteps, probe.Step{Type: probe.StepSendText, Kind: probe.StepSendText, Text: step.Text})
		return nil
	case probe.ScenarioV2StepSendAudio:
		e.providerSteps = append(e.providerSteps, probe.Step{Type: probe.StepSendAudio, Kind: probe.StepSendAudio, CorpusID: step.CorpusID, Corpus: probe.AudioCorpusReference{ID: step.CorpusID, CorpusID: step.CorpusID}, Text: step.Text})
		return nil
	case probe.ScenarioV2StepInterrupt:
		// Interrupt timing is authored for the provider replay. The capture is
		// the source of the actual cancel event; retaining the step keeps the
		// provider projection aligned without introducing a live provider call.
		return nil
	case probe.ScenarioV2StepSleepFake:
		e.clock.Advance(time.Duration(step.DurationMS) * time.Millisecond)
		return nil
	case probe.ScenarioV2StepClose:
		e.closed = true
		return nil
	case probe.ScenarioV2StepOpenTab, probe.ScenarioV2StepSwitchBrowser:
		return fmt.Errorf("unsupported probe.scenario.v2 step %q for the single browser fixture executor", step.Type)
	default:
		return fmt.Errorf("unsupported probe.scenario.v2 step %q", step.Type)
	}
}

func (e *probeScenarioV2Executor) resolveInvocations(ctx context.Context) error {
	if e == nil || e.broker == nil {
		return nil
	}
	for index := range e.invocations {
		invocation := &e.invocations[index]
		if invocation.Err != nil || invocation.PublicID == "" {
			continue
		}
		result, err := e.broker.WaitInvocation(ctx, invocation.PublicID)
		if err != nil {
			invocation.Err = err
			continue
		}
		invocation.Result = result
	}
	for _, invocation := range e.invocations {
		if invocation.Err != nil {
			return invocation.Err
		}
	}
	return nil
}

func (e *probeScenarioV2Executor) toolForRef(ref string) (webmcp.ToolDescriptor, bool) {
	if !e.hasCatalog {
		return webmcp.ToolDescriptor{}, false
	}
	for _, tool := range e.catalog.Tools {
		if tool.Ref == webmcp.ToolRef(ref) {
			return tool, true
		}
	}
	return webmcp.ToolDescriptor{}, false
}

func (e *probeScenarioV2Executor) scenarioExpects(kind probe.ScenarioV2ExpectationType) bool {
	for _, expectation := range e.scenario.Expectations {
		if expectation.Type == kind {
			return true
		}
	}
	return false
}

func isStaleToolError(err error) bool {
	if errors.Is(err, webmcp.ErrStaleToolRef) {
		return true
	}
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorStaleToolRef
}

func (e *probeScenarioV2Executor) populateResult(result probeScenarioV2Result) probeScenarioV2Result {
	if e == nil {
		return result
	}
	result.StepCount = len(e.scenario.Steps)
	result.Steps = e.scenario.Steps
	result.Expectations = e.scenario.Expectations
	result.Ticks = probe.LogicalTime(e.clock.MonotonicMillis())
	result.Frames = len(e.invocations)
	if e.provider != nil {
		result.Ticks = e.provider.ObservedTick
		result.Frames = e.provider.FrameCount
		result.TerminalReason = e.provider.TerminalReason
		result.TerminalProvenance = e.provider.TerminalProvenance
		result.OutputState = e.provider.OutputState
		result.InputDropCount = e.provider.InputDrops
		result.OutputDropCount = e.provider.OutputDrops
	}
	result.ExpectationResults = e.evaluateExpectations()
	result.Pass = result.Error == ""
	for _, outcome := range result.ExpectationResults {
		if !outcome.Passed {
			result.Pass = false
			break
		}
	}
	return result
}

func (e *probeScenarioV2Executor) evaluateExpectations() []probeScenarioV2ExpectationResult {
	results := make([]probeScenarioV2ExpectationResult, 0, len(e.scenario.Expectations))
	for index, expectation := range e.scenario.Expectations {
		passed, expected, actual, err := e.evaluateExpectation(expectation)
		outcome := probeScenarioV2ExpectationResult{Index: index, Type: expectation.Type, Passed: passed, Expected: expected, Actual: actual}
		if err != nil {
			outcome.Error = err.Error()
		}
		results = append(results, outcome)
	}
	return results
}

func (e *probeScenarioV2Executor) evaluateExpectation(expectation probe.ScenarioV2Expectation) (bool, string, string, error) {
	count := func(value int64) (bool, string, string, error) {
		actual := int64(value)
		return actual == expectation.Equals, fmt.Sprintf("%d", expectation.Equals), fmt.Sprintf("%d", actual), nil
	}
	switch expectation.Type {
	case probe.ScenarioV2ExpectationBrowserCountEquals:
		return count(int64(len(e.discovered)))
	case probe.ScenarioV2ExpectationEligibleTabCountEquals:
		eligible := 0
		if e.broker != nil {
			for _, candidate := range e.discovered {
				targets, err := e.broker.ListTargets(context.Background(), webmcp.BrowserSelector{BrowserID: candidate.ID})
				if err != nil {
					return false, fmt.Sprintf("%d", expectation.Equals), "<error>", err
				}
				for _, target := range targets {
					if target.Eligible {
						eligible++
					}
				}
			}
		}
		return count(int64(eligible))
	case probe.ScenarioV2ExpectationSelectedTabEquals:
		return e.selected.Key.TargetID == webmcp.TargetID(expectation.TargetID), expectation.TargetID, string(e.selected.Key.TargetID), nil
	case probe.ScenarioV2ExpectationSelectedOriginEquals:
		return e.selected.Origin == expectation.Origin, expectation.Origin, e.selected.Origin, nil
	case probe.ScenarioV2ExpectationCatalogGenerationEquals:
		generation := e.selected.Generation
		if e.hasCatalog {
			generation = e.catalog.Generation
		}
		return int64(generation) == expectation.Generation, fmt.Sprintf("%d", expectation.Generation), fmt.Sprintf("%d", generation), nil
	case probe.ScenarioV2ExpectationToolCatalogContains, probe.ScenarioV2ExpectationToolCatalogNotContains:
		if !e.hasCatalog && e.broker != nil {
			catalog, err := e.broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
			if err != nil {
				return false, expectation.Name, "<error>", err
			}
			e.catalog = catalog
			e.hasCatalog = true
		}
		found := false
		for _, tool := range e.catalog.Tools {
			if tool.Name == expectation.Name {
				found = true
				break
			}
		}
		passed := found
		expected := "catalog contains " + expectation.Name
		if expectation.Type == probe.ScenarioV2ExpectationToolCatalogNotContains {
			passed = !found
			expected = "catalog does not contain " + expectation.Name
		}
		return passed, expected, fmt.Sprintf("present=%t", found), nil
	case probe.ScenarioV2ExpectationToolSchemaEquals:
		for _, tool := range e.catalog.Tools {
			if tool.Name == expectation.Name {
				passed := semanticJSONEqual(tool.InputSchema, expectation.Schema)
				return passed, string(expectation.Schema), string(tool.InputSchema), nil
			}
		}
		return false, string(expectation.Schema), "<missing>", nil
	case probe.ScenarioV2ExpectationToolInvocationCount:
		var actual int64
		for _, invocation := range e.invocations {
			if invocation.Name == expectation.Name {
				actual++
			}
		}
		return actual == expectation.Equals, fmt.Sprintf("%d", expectation.Equals), fmt.Sprintf("%d", actual), nil
	case probe.ScenarioV2ExpectationToolInputJSONEquals:
		for index := len(e.invocations) - 1; index >= 0; index-- {
			invocation := e.invocations[index]
			if invocation.Name == expectation.Name {
				return semanticJSONEqual(invocation.Input, json.RawMessage(expectation.InputJSON)), expectation.InputJSON, string(invocation.Input), nil
			}
		}
		return false, expectation.InputJSON, "<missing>", nil
	case probe.ScenarioV2ExpectationToolResultJSONPathEquals:
		for index := len(e.invocations) - 1; index >= 0; index-- {
			invocation := e.invocations[index]
			if invocation.Name != expectation.Name {
				continue
			}
			value, err := jsonPathValue(invocation.Result.Output, expectation.Path)
			if err != nil {
				return false, string(expectation.Value), "<missing>", err
			}
			return semanticJSONEqual(value, expectation.Value), string(expectation.Value), string(value), nil
		}
		return false, string(expectation.Value), "<missing>", nil
	case probe.ScenarioV2ExpectationToolStatusEquals:
		for index := len(e.invocations) - 1; index >= 0; index-- {
			if e.invocations[index].Name == expectation.Name {
				actual := string(e.invocations[index].Result.State)
				return strings.EqualFold(actual, expectation.Status), expectation.Status, actual, nil
			}
		}
		return false, expectation.Status, "<missing>", nil
	case probe.ScenarioV2ExpectationNoPendingInvocations:
		pending := 0
		if e.runtime != nil {
			pending += len(e.runtime.PendingInvocationIDs())
		}
		if e.broker != nil {
			pending += len(e.broker.PendingInvocations())
		}
		return pending == 0, "0", fmt.Sprintf("%d", pending), nil
	case probe.ScenarioV2ExpectationPageStateEquals:
		state := json.RawMessage(`null`)
		if e.runtime != nil && len(e.runtime.PageState()) > 0 {
			state = e.runtime.PageState()
		}
		actual, err := jsonPathValue(state, expectation.Path)
		if err != nil {
			return false, string(expectation.Value), "<missing>", err
		}
		return semanticJSONEqual(actual, expectation.Value), string(expectation.Value), string(actual), nil
	case probe.ScenarioV2ExpectationTranscriptContains:
		actual := ""
		if e.provider != nil {
			actual = e.provider.Transcript
		}
		return strings.Contains(actual, expectation.Text), expectation.Text, actual, nil
	case probe.ScenarioV2ExpectationResponseCanceled:
		for _, invocation := range e.invocations {
			if invocation.Result.State == webmcp.InvocationCanceled || strings.EqualFold(string(invocation.Result.State), "canceled") {
				return true, "canceled", string(invocation.Result.State), nil
			}
		}
		if e.provider != nil && e.provider.HasResponseCancel {
			return true, "canceled", "provider_response_cancel", nil
		}
		return false, "canceled", "<none>", nil
	case probe.ScenarioV2ExpectationStaleToolRejected:
		for _, invocation := range e.invocations {
			if isStaleToolError(invocation.Err) {
				return true, "stale_tool_ref", "stale_tool_ref", nil
			}
		}
		return false, "stale_tool_ref", "<none>", nil
	case probe.ScenarioV2ExpectationBrowserConnectionClosed:
		return e.closed, "closed", fmt.Sprintf("%t", e.closed), nil
	case probe.ScenarioV2ExpectationApprovalRequested,
		probe.ScenarioV2ExpectationApprovalNotRequested,
		probe.ScenarioV2ExpectationAssistantAudioStarted,
		probe.ScenarioV2ExpectationAssistantAudioStopped:
		return false, "supported by provider evidence", "not observed", nil
	default:
		return false, string(expectation.Type), "unsupported", fmt.Errorf("unsupported probe.scenario.v2 expectation %q", expectation.Type)
	}
}

func scenarioV2ProviderScenario(scenario probe.ScenarioV2, steps []probe.Step) probe.Scenario {
	return probe.Scenario{ID: scenario.ID, Name: scenario.Name, Description: scenario.Description, Steps: steps}
}

func joinSessionFixtureErrors(errs []gatewaytesting.SessionFixtureValidationError) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

func semanticJSONEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	return semanticJSONValueEqual(leftValue, rightValue)
}

func semanticJSONValueEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, value := range leftValue {
			other, ok := rightValue[key]
			if !ok || !semanticJSONValueEqual(value, other) {
				return false
			}
		}
		return true
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !semanticJSONValueEqual(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case json.Number:
		rightValue, ok := right.(json.Number)
		return ok && leftValue.String() == rightValue.String()
	default:
		return left == right
	}
}

func jsonPathValue(raw json.RawMessage, path string) (json.RawMessage, error) {
	if path == "$" {
		return append(json.RawMessage(nil), raw...), nil
	}
	if !strings.HasPrefix(path, "$.") {
		return nil, fmt.Errorf("unsupported JSONPath %q", path)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	segments := strings.Split(path[2:], ".")
	for _, segment := range segments {
		if segment == "" {
			return nil, errors.New("JSONPath contains an empty segment")
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("JSONPath segment %q is not an object field", segment)
		}
		value, ok = object[segment]
		if !ok {
			return nil, fmt.Errorf("JSONPath field %q is absent", segment)
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
