package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const chromeRecoveryEnv = "WEBMCP_CHROME_RECOVERY"

// TestPinnedChromeTopologyRecoverySuite is the browser-real Lane H proof. It
// is deliberately separate from the ordinary adapter and CLI integration
// tests: the first check is the only pre-acquisition observable operation, so
// the default package test run remains hermetic and offline.
func TestPinnedChromeTopologyRecoverySuite(t *testing.T) {
	if os.Getenv(chromeRecoveryEnv) != "1" {
		t.Skipf("set %s=1 to run the pinned Chrome topology recovery suite", chromeRecoveryEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pinned, err := acquirePinnedChrome(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("acquire O0-pinned Chrome for topology recovery: %v", err)
	}
	t.Logf("WEBMCP_CHROME_RECOVERY_PIN channel=%s version=%s revision=%s platform=%s flags=WebMCP,WebMCPTesting,DevToolsWebMCPSupport profile=<temporary> loopback=true fixture_headers=Origin-Agent-Cluster:?1,Permissions-Policy:tools=(self)", lockedChromeChannel, lockedChromeVersion, lockedChromeRevision, lockedChromePlatform)

	if !t.Run("kill_mid_invocation_same_port_reselect", func(t *testing.T) {
		caseContext, caseCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer caseCancel()
		testRecoveryLossAndReplacement(t, caseContext, pinned)
	}) {
		return
	}
	if !t.Run("navigation_storm", func(t *testing.T) {
		caseContext, caseCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer caseCancel()
		testRecoveryNavigationStorm(t, caseContext, pinned)
	}) {
		return
	}
	if !t.Run("target_close_is_not_navigation", func(t *testing.T) {
		caseContext, caseCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer caseCancel()
		testRecoveryTargetClosure(t, caseContext, pinned)
	}) {
		return
	}
	if !t.Run("in_flight_cancellation_external_tab", func(t *testing.T) {
		caseContext, caseCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer caseCancel()
		testRecoveryInFlightCancellation(t, caseContext, pinned)
	}) {
		return
	}
	if !t.Run("spoken_correction_oracle", func(t *testing.T) {
		caseContext, caseCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer caseCancel()
		testRecoverySpokenCorrection(t, caseContext, pinned)
	}) {
		return
	}

	t.Log("WEBMCP_CHROME_RECOVERY_PASS cases=5 exit=0 output=redacted")
}

type recoveryDiscoverer struct {
	mu        sync.Mutex
	candidate webmcp.BrowserCandidate
}

func (d *recoveryDiscoverer) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	d.mu.Lock()
	candidate := cloneRecoveryCandidate(d.candidate)
	d.mu.Unlock()
	if candidate.ID == "" || options.BrowserID != "" && candidate.ID != options.BrowserID {
		return nil, nil
	}
	return []webmcp.BrowserCandidate{candidate}, nil
}

func (d *recoveryDiscoverer) Set(candidate webmcp.BrowserCandidate) {
	d.mu.Lock()
	d.candidate = cloneRecoveryCandidate(candidate)
	d.mu.Unlock()
}

func cloneRecoveryCandidate(candidate webmcp.BrowserCandidate) webmcp.BrowserCandidate {
	candidate.Diagnostics = append([]webmcp.Diagnostic(nil), candidate.Diagnostics...)
	return candidate
}

type recoverySelection struct {
	browser         *runningChrome
	candidate       webmcp.BrowserCandidate
	target          webmcp.Target
	adapter         *Runtime
	discovery       *discovery.Service
	discoverer      *recoveryDiscoverer
	broker          *webmcp.StatefulBroker
	watch           <-chan webmcp.BrokerEvent
	initialCatalog  webmcp.ToolCatalogSnapshot
	initialComplete webmcp.ToolDescriptor
	initialCancel   webmcp.ToolDescriptor
}

func newRecoverySelection(t *testing.T, ctx context.Context, pinned pinnedChrome, fixture *fixtureServer, port int) *recoverySelection {
	t.Helper()
	assertFixtureHeaders(t, ctx, fixture.URL())
	runPinned := pinned
	runPinned.WorkDir = t.TempDir()
	browser, err := launchPinnedChromeAtPort(ctx, runPinned, fixture.URL(), port)
	if err != nil {
		t.Fatalf("launch O0-pinned Chrome: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("owned Chrome cleanup: %v", closeErr)
		}
	})

	version, err := waitForDevToolsVersion(ctx, browserHTTPURL(browser.endpoint()), lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome identity: %v", err)
	}
	discoveryService := discovery.New(discovery.Options{})
	candidate, err := recoveryCandidate(ctx, discoveryService, browser, version)
	if err != nil {
		t.Fatalf("normalize pinned Chrome candidate: %v", err)
	}
	adapter := NewRuntime(WithEventBuffer(256), WithCommandTimeout(8*time.Second))
	target, err := recoveryFixtureTarget(ctx, adapter, candidate, fixture.URL())
	if err != nil {
		t.Fatalf("discover fixture target through Chrome adapter: %v", err)
	}

	discoverer := &recoveryDiscoverer{candidate: cloneRecoveryCandidate(candidate)}
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           adapter,
		Discoverer:        discoverer,
		Ownership:         webmcp.TargetOwnershipExternal,
		InvocationTimeout: 20 * time.Second,
		WatchBuffer:       256,
	})
	// Register broker cleanup after browser cleanup so the broker releases its
	// target session and handle before the test terminates the owned process.
	t.Cleanup(func() {
		if closeErr := broker.Close(); closeErr != nil {
			var classified *webmcp.ClassifiedError
			if errors.As(closeErr, &classified) && classified != nil {
				t.Logf("broker cleanup: code=%s phase=%v cause=%T:%v", classified.Code, classified.Details["phase"], classified.Cause, classified.Cause)
			} else {
				t.Logf("broker cleanup: %v", closeErr)
			}
		}
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("owned Chrome cleanup: %v", closeErr)
		}
	})

	watchContext, cancelWatch := context.WithCancel(ctx)
	watch := broker.Watch(watchContext)
	t.Cleanup(cancelWatch)
	if candidates, err := broker.Discover(ctx, webmcp.DiscoverOptions{}); err != nil || len(candidates) != 1 || candidates[0].ID != candidate.ID {
		t.Fatalf("discover current pinned browser: candidates=%v err=%v", len(candidates), err)
	}
	if _, err := broker.Select(ctx, webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		var classified *webmcp.ClassifiedError
		if errors.As(err, &classified) && classified != nil {
			t.Fatalf("select fixture target: code=%s details=%v cause=%T:%v", classified.Code, classified.Details, classified.Cause, classified.Cause)
		}
		t.Fatalf("select fixture target: %T:%v", err, err)
	}
	catalog, err := waitForRecoveryCatalog(ctx, broker, 1)
	if err != nil {
		t.Fatalf("wait for initial real Chrome catalog: %v", err)
	}
	complete, _, cancelTool, err := findIntegrationTools(catalog.Tools)
	if err != nil {
		t.Fatalf("find recovery fixture tools: %v", err)
	}
	return &recoverySelection{
		browser:         browser,
		candidate:       candidate,
		target:          target,
		adapter:         adapter,
		discovery:       discoveryService,
		discoverer:      discoverer,
		broker:          broker,
		watch:           watch,
		initialCatalog:  catalog,
		initialComplete: complete,
		initialCancel:   cancelTool,
	}
}

func recoveryCandidate(ctx context.Context, discoveryService *discovery.Service, browser *runningChrome, version devToolsVersion) (webmcp.BrowserCandidate, error) {
	baseURL := browserHTTPURL(version.WebSocketDebuggerURL)
	laneCandidate, err := discoveryService.Discover(ctx, discovery.ConnectionInputs{CDPURL: baseURL})
	if err != nil {
		return webmcp.BrowserCandidate{}, err
	}
	if laneCandidate.ID == "" || laneCandidate.BrowserInstanceID == "" {
		return webmcp.BrowserCandidate{}, errors.New("pinned Chrome discovery omitted opaque browser identity")
	}
	return webmcp.BrowserCandidate{
		ID:                webmcp.BrowserID(laneCandidate.ID),
		Source:            webmcp.DiscoverySourceExplicit,
		Product:           laneCandidate.Product,
		Protocol:          laneCandidate.Protocol,
		BrowserInstanceID: laneCandidate.BrowserInstanceID,
		HTTPURL:           baseURL,
		BrowserWSURL:      version.WebSocketDebuggerURL,
		Loopback:          true,
		Explicit:          true,
	}, nil
}

func recoveryFixtureTarget(ctx context.Context, adapter *Runtime, candidate webmcp.BrowserCandidate, fixtureURL string) (webmcp.Target, error) {
	targets, err := adapter.ListTargets(ctx, candidate)
	if err != nil {
		return webmcp.Target{}, err
	}
	return findFixtureTarget(targets, fixtureURL)
}

func testRecoveryLossAndReplacement(t *testing.T, ctx context.Context, pinned pinnedChrome) {
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	selection := newRecoverySelection(t, ctx, pinned, fixture, 0)
	oldPort, err := recoveryEndpointPort(selection.browser.endpoint())
	if err != nil {
		t.Fatalf("read original Chrome port: %v", err)
	}
	oldAdmission, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: selection.initialCancel.Ref,
		Input:   recoveryInput("loss"),
	})
	if err != nil || oldAdmission.State != webmcp.InvocationDispatched || oldAdmission.InvocationID == "" {
		t.Fatalf("admit kill-mid-invocation call: state=%s err=%v", oldAdmission.State, err)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Pending
	}); err != nil {
		t.Fatalf("wait for mutating invocation admission: %v", err)
	}
	if err := selection.browser.Kill(); err != nil {
		t.Fatalf("kill owned Chrome during invocation: %v", err)
	}
	terminalEvent, err := waitForRecoveryTerminalEvent(ctx, selection.watch, oldAdmission.InvocationID)
	if err != nil {
		t.Fatalf("wait for browser-loss terminal event: %v", err)
	}
	oldTerminal, err := selection.broker.WaitInvocation(ctx, oldAdmission.InvocationID)
	if err != nil {
		t.Fatalf("wait for browser-loss terminal result: %v", err)
	}
	assertRecoveryLossTerminal(t, oldTerminal, terminalEvent)
	if extra := matchingRecoveryTerminalEvents(selection.watch, oldAdmission.InvocationID); len(extra) != 0 {
		t.Fatalf("browser-loss terminal duplicated: count=%d", len(extra))
	}
	selectionFailure := waitForRecoverySelectionFailure(ctx, selection.broker)
	if !recoveryErrorCode(selectionFailure, webmcp.ErrorBrowserDisconnected) && !recoveryErrorCode(selectionFailure, webmcp.ErrorInvocationOrphaned) {
		t.Fatalf("selection after browser loss = %v, want a classified loss", selectionFailure)
	}

	replacementPinned := pinned
	replacementPinned.WorkDir = t.TempDir()
	replacement, err := launchPinnedChromeAtPort(ctx, replacementPinned, fixture.URL(), oldPort)
	if err != nil {
		t.Fatalf("launch same-port replacement Chrome: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := replacement.Close(); closeErr != nil {
			t.Logf("replacement Chrome cleanup: %v", closeErr)
		}
	})
	replacementVersion, err := waitForDevToolsVersion(ctx, browserHTTPURL(replacement.endpoint()), lockedChromeVersion)
	if err != nil {
		t.Fatalf("read replacement Chrome identity: %v", err)
	}
	replacementCandidate, err := recoveryCandidate(ctx, selection.discovery, replacement, replacementVersion)
	if err != nil {
		t.Fatalf("normalize replacement Chrome candidate: %v", err)
	}
	if replacementCandidate.ID == selection.candidate.ID || replacementCandidate.BrowserInstanceID == selection.candidate.BrowserInstanceID {
		t.Fatal("same-port replacement retained the retired browser identity")
	}
	replacementPort, err := recoveryEndpointPort(replacement.endpoint())
	if err != nil || replacementPort != oldPort {
		t.Fatalf("replacement Chrome port = %d err=%v, want original port %d", replacementPort, err, oldPort)
	}
	selection.discoverer.Set(replacementCandidate)
	if candidates, err := selection.broker.Discover(ctx, webmcp.DiscoverOptions{}); err != nil || len(candidates) != 1 || candidates[0].ID != replacementCandidate.ID {
		t.Fatalf("discover same-port replacement: candidates=%v err=%v", len(candidates), err)
	}
	replacementTarget, err := recoveryFixtureTarget(ctx, selection.adapter, replacementCandidate, fixture.URL())
	if err != nil {
		t.Fatalf("discover replacement fixture target: %v", err)
	}
	if _, err := selection.broker.Select(ctx, webmcp.TargetSelector{BrowserID: replacementCandidate.ID, TargetID: replacementTarget.ID}); err != nil {
		t.Fatalf("explicitly select same-port replacement: %v", err)
	}
	replacementCatalog, err := waitForRecoveryCatalog(ctx, selection.broker, 1)
	if err != nil {
		t.Fatalf("wait for replacement catalog: %v", err)
	}
	replacementComplete, _, _, err := findIntegrationTools(replacementCatalog.Tools)
	if err != nil {
		t.Fatalf("find replacement complete tool: %v", err)
	}
	assertRecoveryStaleToolRef(t, selection.broker, selection.initialCancel.Ref)
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && !oracle.Pending && oracle.Value == "initial"
	}); err != nil {
		t.Fatalf("wait for clean replacement page state: %v", err)
	}
	freshAdmission, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: replacementComplete.Ref,
		Input:   recoveryInput("fresh"),
	})
	if err != nil || freshAdmission.InvocationID == oldAdmission.InvocationID {
		t.Fatalf("admit fresh replacement invocation: state=%s id-reused=%t err=%v", freshAdmission.State, freshAdmission.InvocationID == oldAdmission.InvocationID, err)
	}
	freshTerminal, err := selection.broker.WaitInvocation(ctx, freshAdmission.InvocationID)
	if err != nil {
		t.Fatalf("wait for fresh replacement invocation: %v", err)
	}
	if freshTerminal.ErrorCode != "" || freshTerminal.State != webmcp.InvocationCompleted || !json.Valid(freshTerminal.Output) {
		t.Fatalf("fresh replacement terminal = state=%s code=%s valid_output=%t", freshTerminal.State, freshTerminal.ErrorCode, json.Valid(freshTerminal.Output))
	}
	if err := selection.broker.Cancel(ctx, webmcp.CancelRequest{InvocationID: oldAdmission.InvocationID, Reason: "retired_session"}); err != nil {
		t.Fatalf("idempotent cancel of retired invocation: %v", err)
	}
	t.Logf("case=kill_mid_invocation_same_port_reselect old_terminal=%s new_terminal=%s old_browser=%s new_browser=%s generation=%d", oldTerminal.ErrorCode, freshTerminal.State, selection.candidate.ID, replacementCandidate.ID, replacementCatalog.Generation)
}

func testRecoveryNavigationStorm(t *testing.T, ctx context.Context, pinned pinnedChrome) {
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	selection := newRecoverySelection(t, ctx, pinned, fixture, 0)
	retiredRefs := recoveryCatalogRefs(selection.initialCatalog)
	admitted, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: selection.initialCancel.Ref,
		Input:   recoveryInput("navigate"),
	})
	if err != nil || admitted.State != webmcp.InvocationDispatched {
		t.Fatalf("admit navigation-raced invocation: state=%s err=%v", admitted.State, err)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Pending
	}); err != nil {
		t.Fatalf("wait for navigation-raced invocation admission: %v", err)
	}
	previousGeneration := selection.initialCatalog.Generation
	drainRecoveryEvents(selection.watch)
	firstURL := fixture.URL() + "?recovery=nav-1"
	if err := navigateRecoveryTarget(ctx, selection.browser.endpoint(), selection.target.ID, firstURL); err != nil {
		t.Fatalf("navigate selected target for terminal reconciliation: %v", err)
	}
	firstTerminalEvent, err := waitForRecoveryTerminalEvent(ctx, selection.watch, admitted.InvocationID)
	if err != nil {
		t.Fatalf("wait for page-navigation terminal event: %v", err)
	}
	firstTerminal, err := selection.broker.WaitInvocation(ctx, admitted.InvocationID)
	if err != nil {
		t.Fatalf("wait for page-navigation terminal result: %v", err)
	}
	assertRecoveryNavigationTerminal(t, firstTerminal, firstTerminalEvent, previousGeneration)
	firstCatalog, err := waitForRecoveryCatalog(ctx, selection.broker, previousGeneration+1)
	if err != nil {
		t.Fatalf("wait for first fresh navigation catalog: %v", err)
	}
	if firstCatalog.Generation <= previousGeneration {
		t.Fatalf("first navigation generation = %d, want > %d", firstCatalog.Generation, previousGeneration)
	}
	assertRecoveryStaleToolRef(t, selection.broker, selection.initialComplete.Ref)
	generations := []uint64{firstCatalog.Generation}
	navigationEvents := drainRecoveryEvents(selection.watch)
	currentCatalog := firstCatalog
	for cycle := 2; cycle <= 6; cycle++ {
		before := currentCatalog.Generation
		previousRefs := recoveryCatalogRefs(currentCatalog)
		destination := fixture.URL() + "?recovery=storm-" + strconv.Itoa(cycle)
		if err := navigateRecoveryTarget(ctx, selection.browser.endpoint(), selection.target.ID, destination); err != nil {
			t.Fatalf("navigate storm cycle %d: %v", cycle, err)
		}
		currentCatalog, err = waitForRecoveryCatalog(ctx, selection.broker, before+1)
		if err != nil {
			t.Fatalf("wait for storm cycle %d catalog: %v", cycle, err)
		}
		if currentCatalog.Generation <= before {
			t.Fatalf("storm cycle %d generation = %d, previous = %d", cycle, currentCatalog.Generation, before)
		}
		for _, tool := range currentCatalog.Tools {
			if tool.Generation != currentCatalog.Generation {
				t.Fatalf("storm cycle %d tool generation = %d, catalog = %d", cycle, tool.Generation, currentCatalog.Generation)
			}
		}
		if len(currentCatalog.Tools) == 0 {
			t.Fatalf("storm cycle %d produced an empty catalog", cycle)
		}
		for _, ref := range previousRefs {
			assertRecoveryStaleToolRef(t, selection.broker, ref)
		}
		retiredRefs = append(retiredRefs, previousRefs...)
		generations = append(generations, currentCatalog.Generation)
		navigationEvents = append(navigationEvents, drainRecoveryEvents(selection.watch)...)
	}
	lastGeneration := previousGeneration
	for _, event := range navigationEvents {
		if event.Type != webmcp.BrokerEventGenerationChanged || event.Generation <= lastGeneration {
			if event.Type == webmcp.BrokerEventGenerationChanged {
				t.Fatalf("navigation event generation regressed from %d to %d", lastGeneration, event.Generation)
			}
			continue
		}
		lastGeneration = event.Generation
	}
	if len(generations) != 6 || lastGeneration < generations[len(generations)-1] {
		t.Fatalf("navigation generations = %v, events ended at %d", generations, lastGeneration)
	}
	for _, ref := range retiredRefs {
		assertRecoveryStaleToolRef(t, selection.broker, ref)
	}
	finalComplete, _, _, err := findIntegrationTools(currentCatalog.Tools)
	if err != nil {
		t.Fatalf("find final storm tool: %v", err)
	}
	fresh, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: finalComplete.Ref, Input: recoveryInput("current")})
	if err != nil {
		t.Fatalf("invoke final storm catalog: %v", err)
	}
	freshTerminal, err := selection.broker.WaitInvocation(ctx, fresh.InvocationID)
	if err != nil {
		t.Fatalf("wait for final storm invocation: %v", err)
	}
	if freshTerminal.State != webmcp.InvocationCompleted || freshTerminal.ErrorCode != "" || !json.Valid(freshTerminal.Output) {
		t.Fatalf("final storm terminal = state=%s code=%s valid_output=%t", freshTerminal.State, freshTerminal.ErrorCode, json.Valid(freshTerminal.Output))
	}
	t.Logf("case=navigation_storm cycles=%d generations=%v terminal=%s refs_retired=%d target_preserved=true", len(generations), generations, freshTerminal.State, len(retiredRefs))
}

func testRecoverySpokenCorrection(t *testing.T, ctx context.Context, pinned pinnedChrome) {
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	selection := newRecoverySelection(t, ctx, pinned, fixture, 0)

	// The two requests model the original customer intent followed by an
	// explicit spoken correction. The HTTP fixture oracle is independent from
	// the broker result and the final CDP read happens after target detach.
	originalMessage := "blue"
	original, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: selection.initialComplete.Ref,
		Input:   recoveryInput(originalMessage),
		Reason:  "customer_initial_request",
	})
	if err != nil || original.State != webmcp.InvocationDispatched {
		t.Fatalf("admit original correction scenario action: state=%s err=%v", original.State, err)
	}
	originalEvent, err := waitForRecoveryTerminalEvent(ctx, selection.watch, original.InvocationID)
	if err != nil {
		t.Fatalf("wait original correction scenario terminal event: %v", err)
	}
	originalTerminal, err := selection.broker.WaitInvocation(ctx, original.InvocationID)
	if err != nil {
		t.Fatalf("wait original correction scenario terminal result: %v", err)
	}
	if originalTerminal.State != webmcp.InvocationCompleted || originalTerminal.ErrorCode != "" || originalEvent.State != webmcp.InvocationCompleted {
		t.Fatalf("original correction scenario terminal = state=%s code=%s event_state=%s", originalTerminal.State, originalTerminal.ErrorCode, originalEvent.State)
	}
	originalOracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "completed:"+originalMessage && oracle.VisibleText == "completed:"+originalMessage && !oracle.Pending
	})
	if err != nil {
		t.Fatalf("wait original independent correction oracle: %v", err)
	}

	correctionMessage := "unset"
	corrected, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: selection.initialComplete.Ref,
		Input:   recoveryInput(correctionMessage),
		Reason:  "customer_spoken_correction",
	})
	if err != nil || corrected.State != webmcp.InvocationDispatched {
		t.Fatalf("admit spoken correction action: state=%s err=%v", corrected.State, err)
	}
	correctedEvent, err := waitForRecoveryTerminalEvent(ctx, selection.watch, corrected.InvocationID)
	if err != nil {
		t.Fatalf("wait spoken correction terminal event: %v", err)
	}
	correctedTerminal, err := selection.broker.WaitInvocation(ctx, corrected.InvocationID)
	if err != nil {
		t.Fatalf("wait spoken correction terminal result: %v", err)
	}
	if correctedTerminal.State != webmcp.InvocationCompleted || correctedTerminal.ErrorCode != "" || correctedEvent.State != webmcp.InvocationCompleted {
		t.Fatalf("spoken correction terminal = state=%s code=%s event_state=%s", correctedTerminal.State, correctedTerminal.ErrorCode, correctedEvent.State)
	}
	correctedOracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "completed:"+correctionMessage && oracle.VisibleText == "completed:"+correctionMessage && !oracle.Pending
	})
	if err != nil {
		t.Fatalf("wait corrected independent oracle: %v", err)
	}
	if correctedOracle.Value == originalOracle.Value {
		t.Fatalf("correction oracle did not change: original=%+v corrected=%+v", originalOracle, correctedOracle)
	}

	if err := selection.broker.Close(); err != nil {
		t.Fatalf("detach external correction target: %v", err)
	}
	afterDetach, err := inspectExternalTarget(ctx, selection.browser.endpoint(), string(selection.target.ID))
	if err != nil {
		t.Fatalf("probe corrected target after detach: %v", err)
	}
	assertPageStateMatchesOracle(t, afterDetach, correctedOracle)
	t.Logf("case=spoken_correction customer_original=%q customer_correction=%q original_terminal=%s correction_terminal=%s original_oracle=%q corrected_oracle=%q detach_survived=true", originalMessage, correctionMessage, originalTerminal.State, correctedTerminal.State, originalOracle.Value, correctedOracle.Value)
}

func testRecoveryInFlightCancellation(t *testing.T, ctx context.Context, pinned pinnedChrome) {
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	selection := newRecoverySelection(t, ctx, pinned, fixture, 0)

	// The invocation-created broker event is the synchronization point. No
	// timer is used to guess when the page-side operation became cancellable.
	admitted, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: selection.initialCancel.Ref,
		Input:   recoveryInput("overlap"),
	})
	if err != nil || admitted.State != webmcp.InvocationDispatched || admitted.InvocationID == "" {
		t.Fatalf("admit in-flight cancellation call: state=%s id=%s err=%v", admitted.State, admitted.InvocationID, err)
	}
	created, err := waitForRecoveryInvocationCreated(ctx, selection.watch, admitted.InvocationID)
	if err != nil {
		t.Fatalf("wait for observed in-flight invocation: %v", err)
	}
	if created.State != webmcp.InvocationQueued || created.InvocationID != admitted.InvocationID {
		t.Fatalf("created invocation = %+v, want queued identity %s", created, admitted.InvocationID)
	}
	// The created event is queued by the broker contract. The pending page
	// oracle below is the synchronization point that proves the browser-side
	// operation is now in flight before the exact cancellation request.
	beforeCancel, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Pending && oracle.Value == "pending:overlap"
	})
	if err != nil {
		t.Fatalf("wait for in-flight page oracle: %v", err)
	}

	if err := selection.broker.Cancel(ctx, webmcp.CancelRequest{
		InvocationID: admitted.InvocationID,
		Reason:       "customer interruption",
	}); err != nil {
		t.Fatalf("cancel exact in-flight invocation: %v", err)
	}
	terminal, err := selection.broker.WaitInvocation(ctx, admitted.InvocationID)
	if err != nil {
		t.Fatalf("wait for canceled in-flight invocation: %v", err)
	}
	if terminal.InvocationID != admitted.InvocationID || terminal.State != webmcp.InvocationCanceled || terminal.ErrorCode != string(webmcp.ErrorInvocationCanceled) {
		t.Fatalf("canceled terminal = %+v, want exact canceled disposition", terminal)
	}
	afterCancel, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		for _, invocation := range oracle.Invocations {
			if invocation == "canceled:"+cancelToolName {
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("wait for page cancellation observation: %v", err)
	}
	if afterCancel.Value != beforeCancel.Value || !afterCancel.Pending {
		t.Fatalf("page oracle after cancellation = %+v, want preserved pending value %q", afterCancel, beforeCancel.Value)
	}

	if err := selection.broker.Close(); err != nil {
		t.Fatalf("detach external target after cancellation: %v", err)
	}
	if err := mutateExternalTarget(ctx, selection.browser.endpoint(), string(selection.target.ID), "post-detach-probe"); err != nil {
		t.Fatalf("mutate external target after detach: %v", err)
	}
	afterDetach, err := inspectExternalTarget(ctx, selection.browser.endpoint(), string(selection.target.ID))
	if err != nil {
		t.Fatalf("read external target after detach: %v", err)
	}
	if !afterDetach.Ready || afterDetach.Value != "post-detach-probe" || afterDetach.VisibleText != "post-detach-probe" {
		t.Fatalf("post-detach page state = %+v, want responsive mutation", afterDetach)
	}
	t.Logf("case=in_flight_cancellation_external_tab invocation=%s observed_state=%q terminal=%s after_cancel=%q detached_target=%s post_detach_mutation=%q", admitted.InvocationID, beforeCancel.Value, terminal.State, afterCancel.Value, selection.target.ID, afterDetach.Value)
}

func testRecoveryTargetClosure(t *testing.T, ctx context.Context, pinned pinnedChrome) {
	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	selection := newRecoverySelection(t, ctx, pinned, fixture, 0)
	admitted, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{
		ToolRef: selection.initialCancel.Ref,
		Input:   recoveryInput("page-navigation"),
	})
	if err != nil || admitted.State != webmcp.InvocationDispatched {
		t.Fatalf("admit navigation comparison call: state=%s err=%v", admitted.State, err)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool { return oracle.Ready && oracle.Pending }); err != nil {
		t.Fatalf("wait for navigation comparison call: %v", err)
	}
	navigationURL := fixture.URL() + "?recovery=preserve-target"
	if err := navigateRecoveryTarget(ctx, selection.browser.endpoint(), selection.target.ID, navigationURL); err != nil {
		t.Fatalf("navigate target in closure comparison: %v", err)
	}
	navigationEvent, err := waitForRecoveryTerminalEvent(ctx, selection.watch, admitted.InvocationID)
	if err != nil {
		t.Fatalf("wait for navigation comparison event: %v", err)
	}
	navigationTerminal, err := selection.broker.WaitInvocation(ctx, admitted.InvocationID)
	if err != nil {
		t.Fatalf("wait for navigation comparison result: %v", err)
	}
	assertRecoveryNavigationTerminal(t, navigationTerminal, navigationEvent, selection.initialCatalog.Generation)
	currentCatalog, err := waitForRecoveryCatalog(ctx, selection.broker, selection.initialCatalog.Generation+1)
	if err != nil {
		t.Fatalf("wait for catalog after live navigation: %v", err)
	}
	assertRecoveryStaleToolRef(t, selection.broker, selection.initialCancel.Ref)
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && !oracle.Pending && oracle.Value == "initial"
	}); err != nil {
		t.Fatalf("wait for clean navigated page: %v", err)
	}
	if _, err := waitForFixtureTarget(ctx, browserHTTPURL(selection.browser.endpoint()), selection.target.ID, navigationURL, true); err != nil {
		t.Fatalf("target after page navigation: %v", err)
	}

	_, _, cancelTool, err := findIntegrationTools(currentCatalog.Tools)
	if err != nil {
		t.Fatalf("find cancellation tool after navigation: %v", err)
	}
	closeAdmission, err := selection.broker.Invoke(ctx, webmcp.InvokeRequest{ToolRef: cancelTool.Ref, Input: recoveryInput("target-close")})
	if err != nil || closeAdmission.State != webmcp.InvocationDispatched {
		t.Fatalf("admit target-close call: state=%s err=%v", closeAdmission.State, err)
	}
	if _, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool { return oracle.Ready && oracle.Pending }); err != nil {
		t.Fatalf("wait for target-close call: %v", err)
	}
	if err := closeRecoveryTarget(ctx, browserHTTPURL(selection.browser.endpoint()), selection.target.ID); err != nil {
		t.Fatalf("close selected target through browser control: %v", err)
	}
	closeEvent, err := waitForRecoveryTerminalEvent(ctx, selection.watch, closeAdmission.InvocationID)
	if err != nil {
		t.Fatalf("wait for target-close terminal event: %v", err)
	}
	closeTerminal, err := selection.broker.WaitInvocation(ctx, closeAdmission.InvocationID)
	if err != nil {
		t.Fatalf("wait for target-close terminal result: %v", err)
	}
	if closeTerminal.ErrorCode != string(webmcp.ErrorTargetDetached) && closeTerminal.ErrorCode != string(webmcp.ErrorInvocationOrphaned) {
		t.Fatalf("target-close terminal code = %s, want target_detached or invocation_orphaned", closeTerminal.ErrorCode)
	}
	if closeEvent.Type != webmcp.BrokerEventInvocationTerminal || closeEvent.Reason != closeTerminal.ErrorCode {
		t.Fatalf("target-close terminal event = type=%s reason=%s result=%s", closeEvent.Type, closeEvent.Reason, closeTerminal.ErrorCode)
	}
	if extra := matchingRecoveryTerminalEvents(selection.watch, closeAdmission.InvocationID); len(extra) != 0 {
		t.Fatalf("target-close terminal duplicated: count=%d", len(extra))
	}
	if _, err := waitForFixtureTarget(ctx, browserHTTPURL(selection.browser.endpoint()), selection.target.ID, navigationURL, false); err != nil {
		t.Fatalf("target remained after browser target close: %v", err)
	}
	if _, err := waitForDevToolsVersion(ctx, browserHTTPURL(selection.browser.endpoint()), lockedChromeVersion); err != nil {
		t.Fatalf("browser did not remain available after target close: %v", err)
	}
	selectionFailure := waitForRecoverySelectionFailure(ctx, selection.broker)
	if !recoveryErrorCode(selectionFailure, webmcp.ErrorTargetDetached) {
		t.Fatalf("selection after target close = %v, want target_detached", selectionFailure)
	}
	for _, event := range drainRecoveryEvents(selection.watch) {
		if event.Type == webmcp.BrokerEventGenerationChanged && event.Generation > currentCatalog.Generation {
			t.Fatalf("target closure synthesized navigation generation %d after %d", event.Generation, currentCatalog.Generation)
		}
	}
	t.Logf("case=target_close_is_not_navigation navigation_terminal=%s closure_terminal=%s generation=%d browser_preserved=true target_retired=true", navigationTerminal.ErrorCode, closeTerminal.ErrorCode, currentCatalog.Generation)
}

func recoveryInput(message string) json.RawMessage {
	value, _ := json.Marshal(map[string]string{"message": message})
	return value
}

func waitForRecoveryCatalog(ctx context.Context, broker *webmcp.StatefulBroker, minimumGeneration uint64) (webmcp.ToolCatalogSnapshot, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		catalog, err := broker.ListTools(ctx, webmcp.ListToolsOptions{IncludeSchemas: true})
		if err == nil && catalog.Generation >= minimumGeneration && len(catalog.Tools) >= 3 {
			return catalog, nil
		}
		select {
		case <-ctx.Done():
			return webmcp.ToolCatalogSnapshot{}, fmt.Errorf("catalog did not reach generation %d: %w", minimumGeneration, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForRecoverySelectionFailure(ctx context.Context, broker *webmcp.StatefulBroker) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := broker.Selected(ctx)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForRecoveryTerminalEvent(ctx context.Context, events <-chan webmcp.BrokerEvent, invocationID webmcp.InvocationID) (webmcp.BrokerEvent, error) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return webmcp.BrokerEvent{}, errors.New("broker watch closed before terminal event")
			}
			if event.Type == webmcp.BrokerEventInvocationTerminal && event.InvocationID == invocationID {
				return event, nil
			}
		case <-ctx.Done():
			return webmcp.BrokerEvent{}, ctx.Err()
		}
	}
}

func waitForRecoveryInvocationCreated(ctx context.Context, events <-chan webmcp.BrokerEvent, invocationID webmcp.InvocationID) (webmcp.BrokerEvent, error) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return webmcp.BrokerEvent{}, errors.New("broker watch closed before invocation-created event")
			}
			if event.Type == webmcp.BrokerEventInvocationCreated && event.InvocationID == invocationID {
				return event, nil
			}
		case <-ctx.Done():
			return webmcp.BrokerEvent{}, ctx.Err()
		}
	}
}

func drainRecoveryEvents(events <-chan webmcp.BrokerEvent) []webmcp.BrokerEvent {
	var result []webmcp.BrokerEvent
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return result
			}
			result = append(result, event)
		default:
			return result
		}
	}
}

func matchingRecoveryTerminalEvents(events <-chan webmcp.BrokerEvent, invocationID webmcp.InvocationID) []webmcp.BrokerEvent {
	var result []webmcp.BrokerEvent
	for _, event := range drainRecoveryEvents(events) {
		if event.Type == webmcp.BrokerEventInvocationTerminal && event.InvocationID == invocationID {
			result = append(result, event)
		}
	}
	return result
}

func assertRecoveryLossTerminal(t *testing.T, result webmcp.InvokeResult, event webmcp.BrokerEvent) {
	t.Helper()
	if result.ErrorCode != string(webmcp.ErrorBrowserDisconnected) && result.ErrorCode != string(webmcp.ErrorInvocationOrphaned) {
		t.Fatalf("browser-loss result code = %s, want browser_disconnected or invocation_orphaned", result.ErrorCode)
	}
	if event.Type != webmcp.BrokerEventInvocationTerminal || event.Reason != result.ErrorCode {
		t.Fatalf("browser-loss terminal event = type=%s reason=%s result=%s", event.Type, event.Reason, result.ErrorCode)
	}
	if result.ErrorCode == string(webmcp.ErrorBrowserDisconnected) && result.ErrorDetails["reconnect_required"] != true {
		t.Fatal("browser-loss result omitted reconnect_required=true")
	}
	if result.ErrorCode == string(webmcp.ErrorInvocationOrphaned) && result.ErrorDetails["terminal_observed"] != false {
		t.Fatal("orphaned browser-loss result omitted terminal_observed=false")
	}
}

func assertRecoveryNavigationTerminal(t *testing.T, result webmcp.InvokeResult, event webmcp.BrokerEvent, previousGeneration uint64) {
	t.Helper()
	if result.ErrorCode != string(webmcp.ErrorPageNavigated) || result.State != webmcp.InvocationError {
		t.Fatalf("navigation result = state=%s code=%s, want page_navigated error", result.State, result.ErrorCode)
	}
	if event.Type != webmcp.BrokerEventInvocationTerminal || event.Reason != string(webmcp.ErrorPageNavigated) {
		t.Fatalf("navigation terminal event = type=%s reason=%s", event.Type, event.Reason)
	}
	previous, previousOK := result.ErrorDetails["previous_generation"].(uint64)
	current, currentOK := result.ErrorDetails["current_generation"].(uint64)
	if !previousOK || !currentOK || previous < previousGeneration || current <= previous {
		t.Fatalf("navigation generations = previous=%v current=%v, want previous >= %d < current", result.ErrorDetails["previous_generation"], result.ErrorDetails["current_generation"], previousGeneration)
	}
}

func recoveryErrorCode(err error, code webmcp.ErrorCode) bool {
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == code
}

func assertRecoveryStaleToolRef(t *testing.T, broker *webmcp.StatefulBroker, ref webmcp.ToolRef) {
	t.Helper()
	_, err := broker.Invoke(context.Background(), webmcp.InvokeRequest{ToolRef: ref, Input: recoveryInput("stale")})
	if !recoveryErrorCode(err, webmcp.ErrorStaleToolRef) {
		t.Fatalf("retired tool ref result = %v, want stale_tool_ref", err)
	}
}

func recoveryCatalogRefs(catalog webmcp.ToolCatalogSnapshot) []webmcp.ToolRef {
	refs := make([]webmcp.ToolRef, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		refs = append(refs, tool.Ref)
	}
	return refs
}

func recoveryEndpointPort(endpoint string) (int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Port() == "" {
		return 0, fmt.Errorf("parse Chrome endpoint port")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid Chrome endpoint port")
	}
	return port, nil
}

func navigateRecoveryTarget(ctx context.Context, endpoint string, targetID webmcp.TargetID, destination string) (err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.Navigate(destination), chromedp.WaitReady("#ready")); err != nil {
		return fmt.Errorf("navigate target: %w", err)
	}
	return nil
}

func mutateExternalTarget(ctx context.Context, endpoint, targetID, value string) (err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 30*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	defer cancelAllocator()
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	encodedValue, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return fmt.Errorf("encode post-detach probe value: %w", marshalErr)
	}
	expression := `(() => {
  const state = window.__webmcpLaneD;
  if (!state) return false;
  state.value = ` + string(encodedValue) + `;
  const visible = document.querySelector("#state");
  if (visible) visible.textContent = state.value;
  return state.value;
})()`
	var mutated string
	if err := chromedp.Run(targetContext, chromedp.WaitReady("#state"), chromedp.Evaluate(expression, &mutated)); err != nil {
		return fmt.Errorf("mutate external target: %w", err)
	}
	if mutated != value {
		return fmt.Errorf("post-detach probe mutation returned %q, want %q", mutated, value)
	}
	return nil
}

func closeRecoveryTarget(ctx context.Context, baseURL string, targetID webmcp.TargetID) error {
	requestURL := strings.TrimRight(baseURL, "/") + "/json/close/" + url.PathEscape(string(targetID))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("target close HTTP status: %s", response.Status)
	}
	return nil
}
