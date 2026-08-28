package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
)

const (
	crossProcessInvocationValue = "cross-process"
	crossProcessWatchTimeout    = 8 * time.Second
	crossProcessActionGap       = 350 * time.Millisecond
)

type crossProcessWatchEvent struct {
	Version      string `json:"version"`
	Type         string `json:"type"`
	Sequence     uint64 `json:"sequence"`
	BrowserID    string `json:"browser_id,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	Generation   uint64 `json:"generation,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	ToolRef      string `json:"tool_ref,omitempty"`
	State        string `json:"state,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type crossProcessWatchData struct {
	Status string                   `json:"status"`
	Events []crossProcessWatchEvent `json:"events"`
}

type crossProcessInvocationData struct {
	InvocationID string          `json:"invocation_id"`
	ToolRef      string          `json:"tool_ref"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
}

type crossProcessTabData struct {
	BrowserID string `json:"browser_id"`
	TargetID  string `json:"target_id"`
	Type      string `json:"type"`
	Origin    string `json:"origin"`
	Eligible  bool   `json:"eligible"`
}

type crossProcessTabsData struct {
	Tabs []crossProcessTabData `json:"tabs"`
}

type crossProcessEnvelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error json.RawMessage `json:"error"`
}

type crossProcessProcessReport struct {
	Started  bool   `json:"started"`
	Finished bool   `json:"finished"`
	Exit     string `json:"exit,omitempty"`
}

type crossProcessTargetCheck struct {
	Phase      string                `json:"phase"`
	Present    bool                  `json:"present"`
	Attached   bool                  `json:"attached"`
	Responsive bool                  `json:"responsive"`
	TargetID   string                `json:"targetID"`
	Type       string                `json:"type,omitempty"`
	URL        string                `json:"url,omitempty"`
	State      crossProcessPageState `json:"state,omitempty"`
}

type crossProcessProbeReport struct {
	ObservedAt            string                     `json:"observedAt"`
	Endpoint              string                     `json:"endpoint"`
	FixtureURL            string                     `json:"fixtureURL"`
	TargetID              string                     `json:"targetID"`
	RawTargetID           string                     `json:"rawTargetID"`
	BrowserID             string                     `json:"browserID"`
	InvocationID          string                     `json:"invocationID"`
	Watcher               crossProcessProcessReport  `json:"watcher"`
	Invoker               crossProcessProcessReport  `json:"invoker"`
	WatchStatus           string                     `json:"watchStatus"`
	WatchEvents           []crossProcessWatchEvent   `json:"watchEvents"`
	InvokerResult         crossProcessInvocationData `json:"invokerResult"`
	CDPEvents             []crossProcessCDPEvent     `json:"cdpEvents"`
	InitialOracle         crossProcessPageState      `json:"initialOracle"`
	AfterInvocationOracle crossProcessPageState      `json:"afterInvocationOracle"`
	AfterInvocationCDP    crossProcessPageState      `json:"afterInvocationCDP"`
	FinalOracle           crossProcessPageState      `json:"finalOracle"`
	TargetChecks          []crossProcessTargetCheck  `json:"targetChecks"`
	Cleanup               string                     `json:"cleanup"`
	Verdict               string                     `json:"verdict"`
}

type crossProcessCommand struct {
	command []string
	cmd     *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	done    chan struct{}

	mu      sync.Mutex
	waitErr error
}

func startCrossProcessCommand(ctx context.Context, command []string) (*crossProcessCommand, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("cross-process probe command is empty")
	}
	process := &crossProcessCommand{
		command: append([]string(nil), command...),
		done:    make(chan struct{}),
	}
	process.cmd = exec.CommandContext(ctx, command[0], command[1:]...)
	process.cmd.Stdout = &process.stdout
	process.cmd.Stderr = &process.stderr
	if err := process.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command[0], err)
	}
	go func() {
		err := process.cmd.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func (p *crossProcessCommand) wait(ctx context.Context) error {
	if p == nil {
		return errors.New("cross-process probe command is nil")
	}
	select {
	case <-p.done:
		p.mu.Lock()
		err := p.waitErr
		p.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *crossProcessCommand) stop() {
	if p == nil || p.cmd == nil {
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
}

func (p *crossProcessCommand) report() crossProcessProcessReport {
	if p == nil {
		return crossProcessProcessReport{}
	}
	result := crossProcessProcessReport{Started: p.cmd != nil}
	select {
	case <-p.done:
		result.Finished = true
	default:
	}
	p.mu.Lock()
	if p.waitErr != nil {
		result.Exit = p.waitErr.Error()
	}
	p.mu.Unlock()
	return result
}

func (p *crossProcessCommand) stdoutText() string {
	if p == nil {
		return ""
	}
	return p.stdout.String()
}

func (p *crossProcessCommand) stderrText() string {
	if p == nil {
		return ""
	}
	return p.stderr.String()
}

func directAgentCommand(agentBinary, configDir string, args ...string) []string {
	command := []string{agentBinary, "--config-dir", configDir}
	return append(command, args...)
}

func writeCrossProcessConfig(configDir, httpEndpoint string) error {
	contents := "browser:\n  connection:\n    cdp_url: " + httpEndpoint + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write temporary agent config: %w", err)
	}
	return nil
}

func findCrossProcessTab(tabs crossProcessTabsData, fixtureURL string) (crossProcessTabData, error) {
	parsed, err := url.Parse(fixtureURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return crossProcessTabData{}, fmt.Errorf("fixture URL is invalid: %q", fixtureURL)
	}
	wantOrigin := parsed.Scheme + "://" + parsed.Host
	var matches []crossProcessTabData
	for _, tab := range tabs.Tabs {
		if tab.Type == "page" && tab.Origin == wantOrigin && tab.Eligible {
			matches = append(matches, tab)
		}
	}
	if len(matches) != 1 {
		return crossProcessTabData{}, fmt.Errorf("eligible fixture tabs = %d, want exactly one: %+v", len(matches), tabs.Tabs)
	}
	if matches[0].BrowserID == "" || matches[0].TargetID == "" {
		return crossProcessTabData{}, fmt.Errorf("eligible fixture tab has incomplete identity: %+v", matches[0])
	}
	return matches[0], nil
}

func parseCrossProcessEnvelope(output string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(output))
	var envelope crossProcessEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode CLI result: %w; output=%q", err, trimWatchProcessOutput(output))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("CLI result contains more than one JSON value")
	}
	if !envelope.OK {
		return fmt.Errorf("CLI result was not successful: %s", trimWatchProcessOutput(string(envelope.Error)))
	}
	if len(envelope.Data) == 0 {
		return errors.New("CLI result omitted data")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode CLI result data: %w", err)
	}
	return nil
}

func checkCrossProcessTarget(ctx context.Context, httpEndpoint, targetID, phase string, pageContext context.Context, fixtureURL string) (crossProcessTargetCheck, error) {
	info, err := readCrossProcessTarget(ctx, httpEndpoint, targetID)
	if err != nil {
		return crossProcessTargetCheck{}, err
	}
	check := crossProcessTargetCheck{
		Phase:    phase,
		Present:  true,
		Attached: info.Attached,
		TargetID: info.ID,
		Type:     info.Type,
		URL:      info.URL,
	}
	if info.Type != "page" || info.URL != fixtureURL {
		return crossProcessTargetCheck{}, fmt.Errorf("%s target = %+v, want page %s", phase, info, fixtureURL)
	}
	if pageContext != nil {
		state, stateErr := readCrossProcessPageState(pageContext)
		if stateErr != nil {
			return crossProcessTargetCheck{}, fmt.Errorf("%s target is not responsive: %w", phase, stateErr)
		}
		check.State = state
		check.Responsive = state.Ready && state.VisibleText != ""
		if !check.Responsive {
			return crossProcessTargetCheck{}, fmt.Errorf("%s target state is not ready: %+v", phase, state)
		}
	}
	return check, nil
}

func parseAndValidateCrossProcessWatch(output, targetID string) (crossProcessWatchData, string, error) {
	var watch crossProcessWatchData
	if err := parseCrossProcessEnvelope(output, &watch); err != nil {
		return crossProcessWatchData{}, "", err
	}
	browserID, err := validateCrossProcessWatch(watch, targetID)
	if err != nil {
		return crossProcessWatchData{}, "", err
	}
	return watch, browserID, nil
}

func validateCrossProcessWatch(watch crossProcessWatchData, targetID string) (string, error) {
	if watch.Status != "canceled" {
		return "", fmt.Errorf("watch status = %q, want canceled after bounded live run", watch.Status)
	}
	wantTypes := []string{
		"selected",
		"catalog_changed",
		"catalog_changed",
		"invocation_created",
		"invocation_terminal",
		"catalog_changed",
	}
	if len(watch.Events) != len(wantTypes) {
		return "", fmt.Errorf("watch event count = %d, want %d: %+v", len(watch.Events), len(wantTypes), watch.Events)
	}
	browserID := ""
	generation := uint64(0)
	for index, event := range watch.Events {
		if event.Type != wantTypes[index] {
			return "", fmt.Errorf("watch event %d type = %q, want %q: %+v", index, event.Type, wantTypes[index], watch.Events)
		}
		if event.Sequence == 0 || (index > 0 && event.Sequence <= watch.Events[index-1].Sequence) {
			return "", fmt.Errorf("watch sequence at %d is not strictly increasing: %+v", index, watch.Events)
		}
		if event.BrowserID == "" || event.TargetID != targetID || event.Generation == 0 {
			return "", fmt.Errorf("watch event %d identity = %+v, want browser, target %q, generation", index, event, targetID)
		}
		if browserID == "" {
			browserID = event.BrowserID
		} else if event.BrowserID != browserID {
			return "", fmt.Errorf("watch event %d browser ID = %q, want %q", index, event.BrowserID, browserID)
		}
		if generation == 0 {
			generation = event.Generation
		} else if event.Generation != generation {
			return "", fmt.Errorf("watch event %d generation = %d, want %d", index, event.Generation, generation)
		}
	}
	if watch.Events[1].Reason != "tools_added" || watch.Events[2].Reason != "tools_added" || watch.Events[5].Reason != "tools_removed" {
		return "", fmt.Errorf("catalog change reasons = %q/%q/%q, want tools_added/tools_added/tools_removed", watch.Events[1].Reason, watch.Events[2].Reason, watch.Events[5].Reason)
	}
	created, terminal := watch.Events[3], watch.Events[4]
	if created.InvocationID == "" || created.InvocationID != terminal.InvocationID {
		return "", fmt.Errorf("invocation IDs = %q/%q, want one non-empty ID", created.InvocationID, terminal.InvocationID)
	}
	if created.ToolRef == "" || created.ToolRef != terminal.ToolRef {
		return "", fmt.Errorf("invocation refs = %q/%q, want one resolved ref", created.ToolRef, terminal.ToolRef)
	}
	if created.State != "dispatched" || terminal.State != "completed" {
		return "", fmt.Errorf("invocation states = %q/%q, want dispatched/completed", created.State, terminal.State)
	}
	return browserID, nil
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

	rootContext, cancelRoot := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()

	initialContext, cancelInitial := chromedp.NewContext(allocatorContext)
	initialCleanupNeeded := true
	defer func() {
		if initialCleanupNeeded {
			_, _ = detachExternalTarget(initialContext, cancelInitial)
		}
	}()
	if err := chromedp.Run(initialContext, chromedp.Navigate(fixtureURL), chromedp.WaitReady("#ready")); err != nil {
		return report, fmt.Errorf("navigate cross-process fixture: %w", err)
	}
	rawTargetID, err := targetIDFromContext(initialContext)
	if err != nil {
		return report, err
	}
	initialState, err := readCrossProcessPageState(initialContext)
	if err != nil {
		return report, err
	}
	if !initialState.Ready || initialState.Value != "initial" || initialState.VisibleText != "initial" {
		return report, fmt.Errorf("initial page state = %+v, want ready initial state", initialState)
	}
	initialOracleContext, cancelInitialOracle := context.WithTimeout(rootContext, 5*time.Second)
	initialOracle, err := waitForHTTPOracle(initialOracleContext, fixture.StateURL(), func(state crossProcessPageState) bool {
		return state.Ready && state.Value == "initial" && state.VisibleText == "initial" && len(state.Invocations) == 0
	})
	cancelInitialOracle()
	if err != nil {
		return report, err
	}

	configDir, err := os.MkdirTemp("", "webmcp-watch-cli-")
	if err != nil {
		return report, fmt.Errorf("create temporary CLI config: %w", err)
	}
	defer os.RemoveAll(configDir)
	if err := writeCrossProcessConfig(configDir, httpEndpoint); err != nil {
		return report, err
	}
	// Public target IDs are intentionally opaque and scoped to the normalized
	// browser identity. The CDP /json/list ID is only for the probe's direct
	// observer; ask the real CLI for the public ID used by its selectors.
	tabsCommand := directAgentCommand(absAgent, configDir, "webmcp", "tabs", "--cdp-url", httpEndpoint, "--eligible", "--json")
	tabLister, err := startCrossProcessCommand(rootContext, tabsCommand)
	if err != nil {
		return report, err
	}
	if waitErr := tabLister.wait(rootContext); waitErr != nil {
		return report, fmt.Errorf("discover public target ID: %w; stdout=%s stderr=%s", waitErr, trimWatchProcessOutput(tabLister.stdoutText()), trimWatchProcessOutput(tabLister.stderrText()))
	}
	var tabs crossProcessTabsData
	if err := parseCrossProcessEnvelope(tabLister.stdoutText(), &tabs); err != nil {
		return report, fmt.Errorf("parse public target discovery: %w; stderr=%s", err, trimWatchProcessOutput(tabLister.stderrText()))
	}
	publicTab, err := findCrossProcessTab(tabs, fixtureURL)
	if err != nil {
		return report, err
	}
	targetID := publicTab.TargetID
	discoveredBrowserID := publicTab.BrowserID
	watchCommand := directAgentCommand(absAgent, configDir, "webmcp", "watch", "--cdp-url", httpEndpoint, "--tab", targetID, "--timeout", crossProcessWatchTimeout.String(), "--json")
	watcher, err := startCrossProcessCommand(rootContext, watchCommand)
	if err != nil {
		return report, err
	}
	defer watcher.stop()
	if _, err := waitForCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, nil); err != nil {
		return report, fmt.Errorf("wait for exact target to remain present while watcher starts: %w; stderr=%s", err, trimWatchProcessOutput(watcher.stderrText()))
	}
	if err := sleepCrossProcess(rootContext, crossProcessActionGap); err != nil {
		return report, err
	}

	// Keep the setup client attached as the independent observer. A target
	// created by chromedp.NewContext is not launcher-owned, so detaching it
	// before the watcher starts can race chromedp's target cleanup. Keeping this
	// client alive also gives the probe a real second CDP client while both CLI
	// processes attach and detach their own sessions.
	observerContext, cancelObserver := initialContext, cancelInitial
	observerCleanupNeeded := true
	initialCleanupNeeded = false
	defer func() {
		if observerCleanupNeeded {
			_, _ = detachExternalTarget(observerContext, cancelObserver)
		}
	}()
	eventLog := newCrossProcessCDPEventLog()
	chromedp.ListenTarget(observerContext, eventLog.observe)
	if err := chromedp.Run(observerContext, chromedp.WaitReady("#ready")); err != nil {
		return report, fmt.Errorf("attach independent CDP observer: %w", err)
	}
	client := chromedp.FromContext(observerContext)
	if client == nil || client.Target == nil {
		return report, errors.New("independent CDP observer has no target client")
	}
	if err := webmcp.Enable().Do(cdp.WithExecutor(observerContext, client.Target)); err != nil {
		return report, fmt.Errorf("enable WebMCP on independent CDP observer: %w", err)
	}
	if _, err := waitForCrossProcessCDPEvent(rootContext, eventLog.events, "initial toolsAdded", func(event crossProcessCDPEvent) bool {
		return event.Type == "toolsAdded" && containsCrossProcessTool(event.ToolNames, crossProcessInitialToolName)
	}); err != nil {
		return report, err
	}

	var addResult string
	if err := chromedp.Run(observerContext, chromedp.Evaluate(crossProcessAddToolExpression(), &addResult)); err != nil {
		return report, fmt.Errorf("add dynamic DOM-declared tool: %w", err)
	}
	if addResult != "added" {
		return report, fmt.Errorf("dynamic add result = %q, want added", addResult)
	}
	if _, err := waitForCrossProcessCDPEvent(rootContext, eventLog.events, "dynamic toolsAdded", func(event crossProcessCDPEvent) bool {
		return event.Type == "toolsAdded" && containsCrossProcessTool(event.ToolNames, crossProcessDynamicToolName)
	}); err != nil {
		return report, err
	}
	if err := sleepCrossProcess(rootContext, crossProcessActionGap); err != nil {
		return report, err
	}

	invokeCommand := directAgentCommand(absAgent, configDir, "webmcp", "invoke", "--cdp-url", httpEndpoint, "--tab", targetID, crossProcessInitialToolName, "--input-json", `{"value":"cross-process"}`, "--timeout", "5s", "--json")
	invoker, err := startCrossProcessCommand(rootContext, invokeCommand)
	if err != nil {
		return report, err
	}
	defer invoker.stop()
	if waitErr := invoker.wait(rootContext); waitErr != nil {
		return report, fmt.Errorf("separate CLI invocation failed: %w; stdout=%s stderr=%s", waitErr, trimWatchProcessOutput(invoker.stdoutText()), trimWatchProcessOutput(invoker.stderrText()))
	}
	var invocation crossProcessInvocationData
	if err := parseCrossProcessEnvelope(invoker.stdoutText(), &invocation); err != nil {
		return report, fmt.Errorf("parse separate CLI invocation: %w; stderr=%s", err, trimWatchProcessOutput(invoker.stderrText()))
	}
	if invocation.Status != "completed" || invocation.InvocationID == "" || invocation.ToolRef == "" {
		return report, fmt.Errorf("separate CLI invocation result = %+v, want completed ID and ref", invocation)
	}
	oracleContext, cancelOracle := context.WithTimeout(rootContext, 5*time.Second)
	afterInvocationOracle, err := waitForHTTPOracle(oracleContext, fixture.StateURL(), func(state crossProcessPageState) bool {
		return stateMatchesInvocation(state, crossProcessInvocationValue)
	})
	cancelOracle()
	if err != nil {
		return report, err
	}
	afterInvocationCDP, err := readCrossProcessPageState(observerContext)
	if err != nil {
		return report, fmt.Errorf("read page oracle through independent observer after invocation: %w", err)
	}
	if !stateMatchesInvocation(afterInvocationCDP, crossProcessInvocationValue) {
		return report, fmt.Errorf("independent page state after invocation = %+v, want mutation", afterInvocationCDP)
	}
	checks := make([]crossProcessTargetCheck, 0, 5)
	afterInvokeCheck, err := checkCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, "after_invoker_detach", observerContext, fixtureURL)
	if err != nil {
		return report, err
	}
	checks = append(checks, afterInvokeCheck)
	if err := sleepCrossProcess(rootContext, crossProcessActionGap); err != nil {
		return report, err
	}

	var removeResult string
	if err := chromedp.Run(observerContext, chromedp.Evaluate(crossProcessRemoveToolExpression(), &removeResult)); err != nil {
		return report, fmt.Errorf("remove dynamic DOM-declared tool: %w", err)
	}
	if removeResult != "removed" {
		return report, fmt.Errorf("dynamic remove result = %q, want removed", removeResult)
	}
	if _, err := waitForCrossProcessCDPEvent(rootContext, eventLog.events, "dynamic toolsRemoved", func(event crossProcessCDPEvent) bool {
		return event.Type == "toolsRemoved" && containsCrossProcessTool(event.ToolNames, crossProcessDynamicToolName)
	}); err != nil {
		return report, err
	}
	if err := sleepCrossProcess(rootContext, crossProcessActionGap); err != nil {
		return report, err
	}

	if waitErr := watcher.wait(rootContext); waitErr != nil {
		return report, fmt.Errorf("watcher CLI failed: %w; stdout=%s stderr=%s", waitErr, trimWatchProcessOutput(watcher.stdoutText()), trimWatchProcessOutput(watcher.stderrText()))
	}
	watch, browserID, err := parseAndValidateCrossProcessWatch(watcher.stdoutText(), targetID)
	if err != nil {
		return report, fmt.Errorf("validate ordered watcher transcript: %w; stderr=%s", err, trimWatchProcessOutput(watcher.stderrText()))
	}
	checksAfterWatcher, err := checkCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, "after_watcher_detach", observerContext, fixtureURL)
	if err != nil {
		return report, err
	}
	checks = append(checks, checksAfterWatcher)

	if _, err := detachExternalTarget(observerContext, cancelObserver); err != nil {
		return report, fmt.Errorf("detach independent observer: %w", err)
	}
	observerCleanupNeeded = false
	initialCleanupNeeded = false
	notAttached := false
	if _, err := waitForCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, &notAttached); err != nil {
		return report, fmt.Errorf("target after independent observer detach: %w", err)
	}
	checksAfterObserver, err := checkCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, "after_observer_detach", nil, fixtureURL)
	if err != nil {
		return report, err
	}
	checks = append(checks, checksAfterObserver)

	freshContext, cancelFresh := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(target.ID(rawTargetID)))
	freshCleanupNeeded := true
	defer func() {
		if freshCleanupNeeded {
			_, _ = detachExternalTarget(freshContext, cancelFresh)
		}
	}()
	if err := chromedp.Run(freshContext, chromedp.WaitReady("#ready")); err != nil {
		return report, fmt.Errorf("reattach target after CLI cleanup: %w", err)
	}
	freshState, err := readCrossProcessPageState(freshContext)
	if err != nil {
		return report, fmt.Errorf("read target after CLI cleanup: %w", err)
	}
	if !stateMatchesInvocation(freshState, crossProcessInvocationValue) {
		return report, fmt.Errorf("reattached page state = %+v, want preserved mutation", freshState)
	}
	checksAfterReattach, err := checkCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, "reattached_after_cli_cleanup", freshContext, fixtureURL)
	if err != nil {
		return report, err
	}
	checks = append(checks, checksAfterReattach)
	if _, err := detachExternalTarget(freshContext, cancelFresh); err != nil {
		return report, fmt.Errorf("detach final independent verifier: %w", err)
	}
	freshCleanupNeeded = false
	if _, err := waitForCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, &notAttached); err != nil {
		return report, fmt.Errorf("target after final verifier detach: %w", err)
	}
	checksAfterFinal, err := checkCrossProcessTarget(rootContext, httpEndpoint, rawTargetID, "after_final_verifier_detach", nil, fixtureURL)
	if err != nil {
		return report, err
	}
	checks = append(checks, checksAfterFinal)

	finalOracle, err := waitForHTTPOracle(rootContext, fixture.StateURL(), func(state crossProcessPageState) bool {
		return stateMatchesInvocation(state, crossProcessInvocationValue)
	})
	if err != nil {
		return report, err
	}
	cdpEvents := eventLog.snapshot()
	if browserID != discoveredBrowserID {
		return report, fmt.Errorf("watch browser ID = %q, want public target-list browser ID %q", browserID, discoveredBrowserID)
	}
	return crossProcessProbeReport{
		ObservedAt:            time.Now().UTC().Format(time.RFC3339),
		Endpoint:              httpEndpoint,
		FixtureURL:            fixtureURL,
		TargetID:              targetID,
		RawTargetID:           rawTargetID,
		BrowserID:             browserID,
		InvocationID:          invocation.InvocationID,
		Watcher:               watcher.report(),
		Invoker:               invoker.report(),
		WatchStatus:           watch.Status,
		WatchEvents:           watch.Events,
		InvokerResult:         invocation,
		CDPEvents:             cdpEvents,
		InitialOracle:         initialOracle,
		AfterInvocationOracle: afterInvocationOracle,
		AfterInvocationCDP:    afterInvocationCDP,
		FinalOracle:           finalOracle,
		TargetChecks:          checks,
		Cleanup:               "watcher and invoker detached external target; independent CDP verifier reattached and detached it; browser remained launcher-owned",
		Verdict:               "PASS",
	}, nil
}

func containsCrossProcessTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
