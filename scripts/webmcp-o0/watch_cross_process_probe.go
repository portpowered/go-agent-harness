package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
)

const (
	crossProcessProbeTimeout  = 60 * time.Second
	crossProcessOracleTimeout = 5 * time.Second
	crossProcessTargetChecks  = 5
)

// crossProcessProbe carries the state shared by the ordered phases of the
// cross-process watch probe: the watcher and invoker are separate CLI
// processes while this probe keeps an independent CDP observer attached.
type crossProcessProbe struct {
	root                context.Context
	allocator           context.Context
	httpEndpoint        string
	absAgent            string
	fixture             *crossProcessFixture
	fixtureURL          string
	configDir           string
	rawTargetID         string
	targetID            string
	discoveredBrowserID string
	observer            *crossProcessAttachment
	fresh               *crossProcessAttachment
	eventLog            *crossProcessCDPEventLog
	watcher             *crossProcessCommand
	invoker             *crossProcessCommand
	checks              []crossProcessTargetCheck
	report              crossProcessProbeReport
}

func runWatchCrossProcessProbe(endpoint, agentBinary string) (report crossProcessProbeReport, err error) {
	if endpoint == "" {
		return report, errors.New("browser websocket endpoint is empty")
	}
	if agentBinary == "" {
		return report, errors.New("agent CLI binary is empty")
	}
	absAgent, err := filepath.Abs(agentBinary)
	if err != nil {
		return report, fmt.Errorf("resolve agent CLI binary: %w", err)
	}
	if _, err := os.Stat(absAgent); err != nil {
		return report, fmt.Errorf("agent CLI binary %s: %w", absAgent, err)
	}
	httpEndpoint, err := browserHTTPEndpoint(endpoint)
	if err != nil {
		return report, err
	}
	fixture, err := newCrossProcessFixture()
	if err != nil {
		return report, err
	}
	defer fixture.Close()
	fixtureURL := fixture.URL()
	if !isLoopbackWatchFixtureURL(fixtureURL) {
		return report, fmt.Errorf("cross-process fixture URL is not loopback: %s", fixtureURL)
	}

	rootContext, cancelRoot := context.WithTimeout(context.Background(), crossProcessProbeTimeout)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()

	probe := &crossProcessProbe{
		root:         rootContext,
		allocator:    allocatorContext,
		httpEndpoint: httpEndpoint,
		absAgent:     absAgent,
		fixture:      fixture,
		fixtureURL:   fixtureURL,
		checks:       make([]crossProcessTargetCheck, 0, crossProcessTargetChecks),
	}
	return probe.run()
}

func (p *crossProcessProbe) run() (report crossProcessProbeReport, err error) {
	defer func() {
		err = errors.Join(err, p.cleanup())
	}()
	phases := []func() error{
		p.attachInitialTarget,
		p.discoverPublicTarget,
		p.startWatcher,
		p.attachObserver,
		p.addDynamicTool,
		p.invokeFromSeparateProcess,
		p.removeDynamicTool,
		p.collectWatchTranscript,
		p.detachObserver,
		p.reattachFreshVerifier,
		p.finishReport,
	}
	for _, phase := range phases {
		if err := phase(); err != nil {
			return report, err
		}
	}
	return p.report, nil
}

// cleanup releases probe resources in the reverse order of acquisition.
func (p *crossProcessProbe) cleanup() error {
	var errs []error
	if err := p.fresh.cleanup(); err != nil {
		errs = append(errs, fmt.Errorf("detach final verifier during cleanup: %w", err))
	}
	p.invoker.stop()
	if err := p.observer.cleanup(); err != nil {
		errs = append(errs, fmt.Errorf("detach independent observer during cleanup: %w", err))
	}
	p.watcher.stop()
	if p.configDir != "" {
		if err := os.RemoveAll(p.configDir); err != nil {
			errs = append(errs, fmt.Errorf("remove temporary CLI config: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (p *crossProcessProbe) attachInitialTarget() error {
	initialContext, cancelInitial := chromedp.NewContext(p.allocator)
	p.observer = &crossProcessAttachment{ctx: initialContext, cancel: cancelInitial, attached: true}
	if err := chromedp.Run(initialContext, chromedp.Navigate(p.fixtureURL), chromedp.WaitReady("#ready")); err != nil {
		return fmt.Errorf("navigate cross-process fixture: %w", err)
	}
	rawTargetID, err := targetIDFromContext(initialContext)
	if err != nil {
		return err
	}
	p.rawTargetID = rawTargetID
	initialState, err := readCrossProcessPageState(initialContext)
	if err != nil {
		return err
	}
	if !initialState.Ready || initialState.Value != hermeticInitialValue || initialState.VisibleText != hermeticInitialValue {
		return fmt.Errorf("initial page state = %+v, want ready initial state", initialState)
	}
	initialOracleContext, cancelInitialOracle := context.WithTimeout(p.root, crossProcessOracleTimeout)
	initialOracle, err := waitForHTTPOracle(initialOracleContext, p.fixture.StateURL(), func(state crossProcessPageState) bool {
		return state.Ready && state.Value == hermeticInitialValue && state.VisibleText == hermeticInitialValue && len(state.Invocations) == 0
	})
	cancelInitialOracle()
	if err != nil {
		return err
	}
	p.report.InitialOracle = initialOracle
	return nil
}

// discoverPublicTarget asks the real CLI for the public target ID. Public
// target IDs are intentionally opaque and scoped to the normalized browser
// identity; the CDP /json/list ID is only for the probe's direct observer.
func (p *crossProcessProbe) discoverPublicTarget() error {
	configDir, err := os.MkdirTemp("", "webmcp-watch-cli-")
	if err != nil {
		return fmt.Errorf("create temporary CLI config: %w", err)
	}
	p.configDir = configDir
	if err := writeCrossProcessConfig(configDir, p.httpEndpoint); err != nil {
		return err
	}
	tabsCommand := directAgentCommand(p.absAgent, configDir, "webmcp", "tabs", "--cdp-url", p.httpEndpoint, "--eligible", "--json")
	tabLister, err := startCrossProcessCommand(p.root, tabsCommand)
	if err != nil {
		return err
	}
	if waitErr := tabLister.wait(p.root); waitErr != nil {
		return fmt.Errorf("discover public target ID: %w; stdout=%s stderr=%s", waitErr, trimWatchProcessOutput(tabLister.stdoutText()), trimWatchProcessOutput(tabLister.stderrText()))
	}
	var tabs crossProcessTabsData
	if err := parseCrossProcessEnvelope(tabLister.stdoutText(), &tabs); err != nil {
		return fmt.Errorf("parse public target discovery: %w; stderr=%s", err, trimWatchProcessOutput(tabLister.stderrText()))
	}
	publicTab, err := findCrossProcessTab(tabs, p.fixtureURL)
	if err != nil {
		return err
	}
	p.targetID = publicTab.TargetID
	p.discoveredBrowserID = publicTab.BrowserID
	return nil
}

func (p *crossProcessProbe) startWatcher() error {
	watchCommand := directAgentCommand(p.absAgent, p.configDir, "webmcp", "watch", "--cdp-url", p.httpEndpoint, "--tab", p.targetID, "--timeout", crossProcessWatchTimeout.String(), "--json")
	watcher, err := startCrossProcessCommand(p.root, watchCommand)
	if err != nil {
		return err
	}
	p.watcher = watcher
	if _, err := waitForCrossProcessTarget(p.root, p.httpEndpoint, p.rawTargetID, nil); err != nil {
		return fmt.Errorf("wait for exact target to remain present while watcher starts: %w; stderr=%s", err, trimWatchProcessOutput(watcher.stderrText()))
	}
	return sleepCrossProcess(p.root, crossProcessActionGap)
}

// attachObserver keeps the setup client attached as the independent
// observer. A target created by chromedp.NewContext is not launcher-owned,
// so detaching it before the watcher starts can race chromedp's target
// cleanup. Keeping this client alive also gives the probe a real second CDP
// client while both CLI processes attach and detach their own sessions.
func (p *crossProcessProbe) attachObserver() error {
	p.eventLog = newCrossProcessCDPEventLog()
	chromedp.ListenTarget(p.observer.ctx, p.eventLog.observe)
	if err := chromedp.Run(p.observer.ctx, chromedp.WaitReady("#ready")); err != nil {
		return fmt.Errorf("attach independent CDP observer: %w", err)
	}
	client := chromedp.FromContext(p.observer.ctx)
	if client == nil || client.Target == nil {
		return errors.New("independent CDP observer has no target client")
	}
	if err := webmcp.Enable().Do(cdp.WithExecutor(p.observer.ctx, client.Target)); err != nil {
		return fmt.Errorf("enable WebMCP on independent CDP observer: %w", err)
	}
	_, err := waitForCrossProcessCDPEvent(p.root, p.eventLog.events, "initial toolsAdded", func(event crossProcessCDPEvent) bool {
		return event.Type == "toolsAdded" && containsCrossProcessTool(event.ToolNames, crossProcessInitialToolName)
	})
	return err
}

func (p *crossProcessProbe) addDynamicTool() error {
	var addResult string
	if err := chromedp.Run(p.observer.ctx, chromedp.Evaluate(crossProcessAddToolExpression(), &addResult)); err != nil {
		return fmt.Errorf("add dynamic DOM-declared tool: %w", err)
	}
	if addResult != "added" {
		return fmt.Errorf("dynamic add result = %q, want added", addResult)
	}
	if _, err := waitForCrossProcessCDPEvent(p.root, p.eventLog.events, "dynamic toolsAdded", func(event crossProcessCDPEvent) bool {
		return event.Type == "toolsAdded" && containsCrossProcessTool(event.ToolNames, crossProcessDynamicToolName)
	}); err != nil {
		return err
	}
	return sleepCrossProcess(p.root, crossProcessActionGap)
}

func (p *crossProcessProbe) invokeFromSeparateProcess() error {
	invokeCommand := directAgentCommand(p.absAgent, p.configDir, "webmcp", "invoke", "--cdp-url", p.httpEndpoint, "--tab", p.targetID, crossProcessInitialToolName, "--input-json", `{"value":"cross-process"}`, "--timeout", "5s", "--json")
	invoker, err := startCrossProcessCommand(p.root, invokeCommand)
	if err != nil {
		return err
	}
	p.invoker = invoker
	if waitErr := invoker.wait(p.root); waitErr != nil {
		return fmt.Errorf("separate CLI invocation failed: %w; stdout=%s stderr=%s", waitErr, trimWatchProcessOutput(invoker.stdoutText()), trimWatchProcessOutput(invoker.stderrText()))
	}
	var invocation crossProcessInvocationData
	if err := parseCrossProcessEnvelope(invoker.stdoutText(), &invocation); err != nil {
		return fmt.Errorf("parse separate CLI invocation: %w; stderr=%s", err, trimWatchProcessOutput(invoker.stderrText()))
	}
	if invocation.Status != "completed" || invocation.InvocationID == "" || invocation.ToolRef == "" {
		return fmt.Errorf("separate CLI invocation result = %+v, want completed ID and ref", invocation)
	}
	p.report.InvokerResult = invocation
	p.report.InvocationID = invocation.InvocationID
	return p.verifyInvocationEffect()
}

func (p *crossProcessProbe) verifyInvocationEffect() error {
	oracleContext, cancelOracle := context.WithTimeout(p.root, crossProcessOracleTimeout)
	afterInvocationOracle, err := waitForHTTPOracle(oracleContext, p.fixture.StateURL(), func(state crossProcessPageState) bool {
		return stateMatchesInvocation(state, crossProcessInvocationValue)
	})
	cancelOracle()
	if err != nil {
		return err
	}
	afterInvocationCDP, err := readCrossProcessPageState(p.observer.ctx)
	if err != nil {
		return fmt.Errorf("read page oracle through independent observer after invocation: %w", err)
	}
	if !stateMatchesInvocation(afterInvocationCDP, crossProcessInvocationValue) {
		return fmt.Errorf("independent page state after invocation = %+v, want mutation", afterInvocationCDP)
	}
	p.report.AfterInvocationOracle = afterInvocationOracle
	p.report.AfterInvocationCDP = afterInvocationCDP
	if err := p.checkTarget("after_invoker_detach", p.observer.ctx); err != nil {
		return err
	}
	return sleepCrossProcess(p.root, crossProcessActionGap)
}

func (p *crossProcessProbe) checkTarget(phase string, pageContext context.Context) error {
	check, err := checkCrossProcessTarget(p.root, p.httpEndpoint, p.rawTargetID, phase, pageContext, p.fixtureURL)
	if err != nil {
		return err
	}
	p.checks = append(p.checks, check)
	return nil
}

func (p *crossProcessProbe) removeDynamicTool() error {
	var removeResult string
	if err := chromedp.Run(p.observer.ctx, chromedp.Evaluate(crossProcessRemoveToolExpression(), &removeResult)); err != nil {
		return fmt.Errorf("remove dynamic DOM-declared tool: %w", err)
	}
	if removeResult != "removed" {
		return fmt.Errorf("dynamic remove result = %q, want removed", removeResult)
	}
	if _, err := waitForCrossProcessCDPEvent(p.root, p.eventLog.events, "dynamic toolsRemoved", func(event crossProcessCDPEvent) bool {
		return event.Type == "toolsRemoved" && containsCrossProcessTool(event.ToolNames, crossProcessDynamicToolName)
	}); err != nil {
		return err
	}
	return sleepCrossProcess(p.root, crossProcessActionGap)
}

func (p *crossProcessProbe) collectWatchTranscript() error {
	if waitErr := p.watcher.wait(p.root); waitErr != nil {
		return fmt.Errorf("watcher CLI failed: %w; stdout=%s stderr=%s", waitErr, trimWatchProcessOutput(p.watcher.stdoutText()), trimWatchProcessOutput(p.watcher.stderrText()))
	}
	watch, browserID, err := parseAndValidateCrossProcessWatch(p.watcher.stdoutText(), p.targetID)
	if err != nil {
		return fmt.Errorf("validate ordered watcher transcript: %w; stderr=%s", err, trimWatchProcessOutput(p.watcher.stderrText()))
	}
	p.report.BrowserID = browserID
	p.report.WatchStatus = watch.Status
	p.report.WatchEvents = watch.Events
	return p.checkTarget("after_watcher_detach", p.observer.ctx)
}

func (p *crossProcessProbe) detachObserver() error {
	if err := p.observer.detach(); err != nil {
		return fmt.Errorf("detach independent observer: %w", err)
	}
	notAttached := false
	if _, err := waitForCrossProcessTarget(p.root, p.httpEndpoint, p.rawTargetID, &notAttached); err != nil {
		return fmt.Errorf("target after independent observer detach: %w", err)
	}
	return p.checkTarget("after_observer_detach", nil)
}

func (p *crossProcessProbe) reattachFreshVerifier() error {
	freshContext, cancelFresh := chromedp.NewContext(p.allocator, chromedp.WithTargetID(target.ID(p.rawTargetID)))
	p.fresh = &crossProcessAttachment{ctx: freshContext, cancel: cancelFresh, attached: true}
	if err := chromedp.Run(freshContext, chromedp.WaitReady("#ready")); err != nil {
		return fmt.Errorf("reattach target after CLI cleanup: %w", err)
	}
	freshState, err := readCrossProcessPageState(freshContext)
	if err != nil {
		return fmt.Errorf("read target after CLI cleanup: %w", err)
	}
	if !stateMatchesInvocation(freshState, crossProcessInvocationValue) {
		return fmt.Errorf("reattached page state = %+v, want preserved mutation", freshState)
	}
	if err := p.checkTarget("reattached_after_cli_cleanup", freshContext); err != nil {
		return err
	}
	if err := p.fresh.detach(); err != nil {
		return fmt.Errorf("detach final independent verifier: %w", err)
	}
	notAttached := false
	if _, err := waitForCrossProcessTarget(p.root, p.httpEndpoint, p.rawTargetID, &notAttached); err != nil {
		return fmt.Errorf("target after final verifier detach: %w", err)
	}
	return p.checkTarget("after_final_verifier_detach", nil)
}

func (p *crossProcessProbe) finishReport() error {
	finalOracle, err := waitForHTTPOracle(p.root, p.fixture.StateURL(), func(state crossProcessPageState) bool {
		return stateMatchesInvocation(state, crossProcessInvocationValue)
	})
	if err != nil {
		return err
	}
	cdpEvents := p.eventLog.snapshot()
	if p.report.BrowserID != p.discoveredBrowserID {
		return fmt.Errorf("watch browser ID = %q, want public target-list browser ID %q", p.report.BrowserID, p.discoveredBrowserID)
	}
	p.report.ObservedAt = time.Now().UTC().Format(time.RFC3339)
	p.report.Endpoint = p.httpEndpoint
	p.report.FixtureURL = p.fixtureURL
	p.report.TargetID = p.targetID
	p.report.RawTargetID = p.rawTargetID
	p.report.Watcher = p.watcher.report()
	p.report.Invoker = p.invoker.report()
	p.report.CDPEvents = cdpEvents
	p.report.FinalOracle = finalOracle
	p.report.TargetChecks = p.checks
	p.report.Cleanup = "watcher and invoker detached external target; independent CDP verifier reattached and detached it; browser remained launcher-owned"
	p.report.Verdict = verdictPass
	return nil
}
