package chrome

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	looptranscript "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const (
	conversationalCustomerLiveEnv       = "WEBMCP_CONVERSATIONAL_CUSTOMER_LIVE"
	conversationalCustomerAPIKeyEnv     = "WEBMCP_CONVERSATIONAL_OPENAI_API_KEY"
	conversationalCustomerAudioDirEnv   = "WEBMCP_CONVERSATIONAL_AUDIO_DIR"
	conversationalCustomerValidatorEnv  = "WEBMCP_CONVERSATIONAL_VALIDATOR_COMMAND"
	conversationalCustomerReportPathEnv = "WEBMCP_CONVERSATIONAL_REPORT_PATH"
	conversationalCustomerMainRootEnv   = "WEBMCP_CONVERSATIONAL_MAIN_SOURCE_ROOT"
	conversationalCustomerLaneRootEnv   = "WEBMCP_CONVERSATIONAL_LANE_I_SOURCE_ROOT"
	conversationalCustomerPostLaneEnv   = "WEBMCP_CONVERSATIONAL_POST_LANE_I_FINDING"
	conversationalCustomerLaneNumber    = "269"
	conversationalCustomerModelEnv      = "WEBMCP_CONVERSATIONAL_MODEL"

	conversationalCustomerHomePage     = "home"
	conversationalCustomerSettingsPage = "settings"
	conversationalCustomerLabel        = "live alpha"
	conversationalCustomerTheme        = "live dark"
	conversationalCustomerPriority     = "high"
	conversationalCustomerCorrected    = "live corrected"
)

//go:embed testdata/webmcp_conversational_customer.html
var conversationalCustomerFixtureHTML []byte

// TestPinnedChromeWebMCPConversationalCustomerLive is the single credentialed
// acceptance runner for Story 006. It is skipped before any browser, network,
// provider, or credential side effect unless explicitly enabled. All browser
// work remains in the production agent binary; this test only supplies the
// independent fixture/oracle and report boundaries.
func TestPinnedChromeWebMCPConversationalCustomerLive(t *testing.T) {
	if os.Getenv(conversationalCustomerLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the credentialed canonical conversation", conversationalCustomerLiveEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	apiKey := requiredNewlineFreeEnv(t, conversationalCustomerAPIKeyEnv)
	// The production config loader consumes this scoped environment variable.
	// It is intentionally never passed as a process argument or report field.
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", apiKey)
	audioPaths := conversationalCustomerAudioPaths(t)
	validatorCommand := conversationalCustomerValidatorCommand(t)
	lane := readConversationalCustomerLaneStatus(t, ctx)
	sourceRoot := conversationalCustomerSourceRoot(t, lane)

	workDir := t.TempDir()
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	fixture := newConversationalCustomerFixtureServer()
	t.Cleanup(fixture.Close)
	homeURL := fixture.URL(conversationalCustomerHomePage)
	settingsURL := fixture.URL(conversationalCustomerSettingsPage)

	browser, err := launchPinnedChrome(ctx, pinned, homeURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := browser.Close(); closeErr != nil {
			t.Logf("Chrome cleanup: %v", closeErr)
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	target, err := waitForFixturePageTarget(ctx, baseURL, homeURL)
	if err != nil {
		t.Fatalf("discover exact conversational fixture target: %v", err)
	}

	binaryPath := filepath.Join(workDir, "agent")
	if err := buildGateBinary(ctx, sourceRoot, binaryPath); err != nil {
		t.Fatalf("build production agent binary from %s: %v", sourceRoot, err)
	}
	configDir := filepath.Join(workDir, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("create live config directory: %v", err)
	}
	if err := writeGateConfig(configDir, baseURL, fixture.Origin()); err != nil {
		t.Fatalf("write live browser config: %v", err)
	}
	browserID, targetID, err := selectConversationalCustomerTarget(ctx, binaryPath, configDir, homeURL)
	if err != nil {
		t.Fatalf("select exact conversational fixture target: %v", err)
	}
	if target.ID != string(targetID) {
		t.Fatalf("selected target = %s, discovered target = %s", targetID, target.ID)
	}

	observer, observerClose, err := openConversationalCustomerObserver(ctx, browserID, targetID, version)
	if err != nil {
		t.Fatalf("open independent browser event observer: %v", err)
	}
	collector := newConversationalCustomerEventCollector()
	go collector.consume(observer)
	t.Cleanup(func() {
		if closeErr := observerClose(); closeErr != nil {
			t.Logf("observer cleanup: %v", closeErr)
		}
	})

	if _, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerHomePage
	}); err != nil {
		t.Fatalf("wait for initial independent fixture oracle: %v", err)
	}

	scenario := newConversationalCustomerScenario(homeURL, settingsURL)
	if err := scenario.Validate(); err != nil {
		t.Fatalf("canonical scenario validation: %v", err)
	}
	initialOracle, err := readConversationalCustomerOracle(ctx, fixture.StateURL())
	if err != nil {
		t.Fatalf("read initial independent oracle: %v", err)
	}

	promptPath := filepath.Join(workDir, "system-prompt.txt")
	if err := os.WriteFile(promptPath, []byte(conversationalCustomerSystemPrompt()), 0o600); err != nil {
		t.Fatalf("write fixed live system prompt: %v", err)
	}
	recordDir := filepath.Join(workDir, "recording")
	sessionArgs := []string{
		"session",
		"--browser-tools=webmcp",
		"--browser-cdp-url", baseURL,
		"--browser-browser", browserID,
		"--browser-tab", string(targetID),
		"--browser-allowed-origin", fixture.Origin(),
		"--browser-cancel-on-interrupt", "always",
		"--provider", "openai",
		"--model", conversationalCustomerModel(),
		"--system-prompt", promptPath,
		"--record-dir", recordDir,
		"--wait-for-close",
		"--max-duration", "8m",
		"--audio-interrupt", audioPaths[4],
		"--audio-interrupt-on-tool", "webmcp_customer_pending",
	}
	for _, path := range audioPaths {
		sessionArgs = append(sessionArgs, "--audio-in-turn", path)
	}
	session, err := startGateCommand(ctx, binaryPath, configDir, sessionArgs...)
	if err != nil {
		t.Fatalf("start production conversational session: %v", err)
	}

	// The event boundaries below are the customer-navigation clock. Each
	// navigation is issued after the preceding browser terminal event, before
	// the scheduler can release the next turn, and then admitted only after the
	// independent target reports the new page.
	labelTerminal, err := collector.wait(ctx, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.ToolName == "webmcp_customer_set_label" && event.Status != ""
	})
	if err != nil {
		t.Fatalf("wait for initial label terminal event: %v", err)
	}
	labelAfter, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Label == conversationalCustomerLabel
	})
	if err != nil {
		t.Fatalf("wait for initial label oracle: %v", err)
	}

	themeTerminal, err := collector.wait(ctx, 0, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.ToolName == "webmcp_customer_set_theme" && event.Status != ""
	})
	if err != nil {
		t.Fatalf("wait for second theme terminal event: %v", err)
	}
	themeAfter, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Theme == conversationalCustomerTheme
	})
	if err != nil {
		t.Fatalf("wait for second theme oracle: %v", err)
	}

	settingsNavigationStart := collector.len()
	if err := navigateConversationalCustomerTarget(ctx, browser.endpoint(), targetID, settingsURL); err != nil {
		t.Fatalf("customer navigate to settings: %v", err)
	}
	settingsNavigation, err := collector.wait(ctx, settingsNavigationStart, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventPageNavigated || event.Type == webmcp.EventFrameNavigated
	})
	if err != nil {
		t.Fatalf("observe settings navigation: %v", err)
	}
	settingsBefore, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerSettingsPage
	})
	if err != nil {
		t.Fatalf("wait for settings oracle: %v", err)
	}

	priorityTerminal, err := collector.wait(ctx, settingsNavigationStart, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.ToolName == "webmcp_customer_set_priority" && event.Status != ""
	})
	if err != nil {
		t.Fatalf("wait for fresh settings priority terminal event: %v", err)
	}
	settingsAfter, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerSettingsPage && oracle.Priority == conversationalCustomerPriority
	})
	if err != nil {
		t.Fatalf("wait for settings priority oracle: %v", err)
	}

	homeNavigationStart := collector.len()
	if err := navigateConversationalCustomerTarget(ctx, browser.endpoint(), targetID, homeURL); err != nil {
		t.Fatalf("customer navigate back to home: %v", err)
	}
	homeNavigation, err := collector.wait(ctx, homeNavigationStart, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventPageNavigated || event.Type == webmcp.EventFrameNavigated
	})
	if err != nil {
		t.Fatalf("observe home correction navigation: %v", err)
	}
	correctionBefore, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerHomePage && oracle.Label == conversationalCustomerLabel && oracle.Theme == conversationalCustomerTheme
	})
	if err != nil {
		t.Fatalf("wait for correction baseline oracle: %v", err)
	}
	correctionTerminal, err := collector.wait(ctx, homeNavigationStart, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.ToolName == "webmcp_customer_set_label" && event.Status != "" && event.Sequence > homeNavigation.Sequence
	})
	if err != nil {
		t.Fatalf("wait for correction terminal event: %v", err)
	}
	correctionAfter, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerHomePage && oracle.Label == conversationalCustomerCorrected
	})
	if err != nil {
		t.Fatalf("wait for correction oracle: %v", err)
	}

	pending, err := collector.wait(ctx, homeNavigationStart, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.ToolName == "webmcp_customer_pending" && event.InvocationID != ""
	})
	if err != nil {
		t.Fatalf("wait for synchronized pending invocation: %v", err)
	}
	if _, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool { return oracle.Pending }); err != nil {
		t.Fatalf("wait for pending oracle: %v", err)
	}
	cancelResult, err := cancelConversationalCustomerInvocation(ctx, binaryPath, configDir, pending.InvocationID)
	if err != nil {
		t.Fatalf("cancel pending invocation through separate browser process: %v", err)
	}
	if cancelResult.Status != "cancel_requested" {
		t.Fatalf("cancel pending invocation status = %q", cancelResult.Status)
	}
	if _, err := waitForConversationalCustomerOracle(ctx, fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return !oracle.Pending && conversationalCustomerOracleHasInvocation(oracle, "canceled:webmcp_customer_pending")
	}); err != nil {
		t.Fatalf("wait for pending cancellation oracle: %v", err)
	}

	// The explicit customer cancel turn is the final scheduled audio input. It
	// is released only after the external browser cancellation unblocks the
	// pending invocation; let the shared scheduled-audio close path deliver
	// that turn and terminate the production session. Cancelling the process
	// here would drop the very customer turn this acceptance run must prove.
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cleanupCancel()
	sessionResult, waitErr := session.wait(cleanupContext)
	if waitErr != nil {
		t.Fatalf("wait for production conversational session: %v", waitErr)
	}
	if sessionResult.Err != nil && sessionResult.ExitCode != 0 {
		t.Fatalf("production conversational session exited with code %d", sessionResult.ExitCode)
	}

	if closeErr := observerClose(); closeErr != nil {
		t.Fatalf("detach independent browser observer: %v", closeErr)
	}
	postProbe, err := inspectConversationalCustomerTarget(ctx, browser.endpoint(), browserID, targetID)
	if err != nil {
		t.Fatalf("independent post-detach tab probe: %v", err)
	}
	postOracle, err := readConversationalCustomerOracle(ctx, fixture.StateURL())
	if err != nil {
		t.Fatalf("read post-session oracle: %v", err)
	}

	result, err := buildConversationalCustomerResult(
		scenario,
		collector.snapshot(),
		filepath.Join(recordDir, "session-log.jsonl"),
		[]conversationalCustomerNavigationObservation{
			{StepID: "stale_recovery", Event: settingsNavigation},
			{StepID: "correction", Event: homeNavigation},
		},
		[]conversationalCustomerOracleObservation{
			{StepID: "initial_action", Phase: servicetest.BrowserConversationOracleBefore, Oracle: initialOracle},
			{StepID: "initial_action", Phase: servicetest.BrowserConversationOracleAfter, Oracle: labelAfter},
			{StepID: "second_action", Phase: servicetest.BrowserConversationOracleBefore, Oracle: labelAfter},
			{StepID: "second_action", Phase: servicetest.BrowserConversationOracleAfter, Oracle: themeAfter},
			{StepID: "stale_recovery", Phase: servicetest.BrowserConversationOracleBefore, Oracle: settingsBefore},
			{StepID: "stale_recovery", Phase: servicetest.BrowserConversationOracleAfter, Oracle: settingsAfter},
			{StepID: "correction", Phase: servicetest.BrowserConversationOracleBefore, Oracle: correctionBefore},
			{StepID: "correction", Phase: servicetest.BrowserConversationOracleAfter, Oracle: correctionAfter},
			{StepID: "", Phase: servicetest.BrowserConversationOraclePostSession, Oracle: postOracle},
		},
		browserID,
		targetID,
		postProbe,
		pending,
		cancelResult,
	)
	if err != nil {
		t.Fatalf("build joined live evidence: %v", err)
	}
	mechanical, err := servicetest.EvaluateBrowserConversation(scenario, result, nil)
	if err != nil {
		t.Fatalf("evaluate joined live evidence: %v", err)
	}
	result.Mechanical = mechanical
	validator, err := servicetest.NewBrowserConversationCommandValidator(validatorCommand, 90*time.Second)
	if err != nil {
		t.Fatalf("construct validator command: %v", err)
	}
	validator.Env = sanitizedValidatorEnvironment()
	verdict, validatorErr := validator.ValidateBrowserConversation(result)
	if validatorErr != nil {
		result.Validator = servicetest.BrowserConversationValidatorVerdict{
			Version: servicetest.BrowserConversationValidatorVersion,
			Status:  servicetest.BrowserConversationValidatorNotRun,
			Summary: "validator command failed before a structured verdict was returned",
		}
	} else {
		result.Validator = verdict
	}
	metadata := servicetest.BrowserConversationReportMetadata{
		Command:       fmt.Sprintf("agent session --browser-tools=webmcp --provider openai --model %s --record-dir <recording> --audio-in-turn <six finite files> --audio-interrupt <finite file> --audio-interrupt-on-tool webmcp_customer_pending", conversationalCustomerModel()),
		Configuration: fmt.Sprintf("browser backend=webmcp cdp=loopback allowed_origin=%s browser=%s target=%s", fixture.Origin(), browserID, targetID),
		DependencyBaseline: []string{
			"go=" + runtime.Version(),
			"chrome_channel=" + lockedChromeChannel,
			"chrome_version=" + lockedChromeVersion,
			"chrome_revision=" + lockedChromeRevision,
		},
		Provider:         "openai",
		Model:            conversationalCustomerModel(),
		BrowserChannel:   lockedChromeChannel,
		BrowserVersion:   lockedChromeVersion,
		BrowserRevision:  lockedChromeRevision,
		PR269Status:      lane.State,
		LaneIBranch:      lane.HeadRefName,
		LaneIPullRequest: "https://github.com/portpowered/go-agent-harness/pull/" + conversationalCustomerLaneNumber,
	}
	report, reportErr := servicetest.RenderBrowserConversationReport(result, metadata)
	if reportErr != nil {
		t.Fatalf("render sanitized live report: %v", reportErr)
	}
	if reportPath := strings.TrimSpace(os.Getenv(conversationalCustomerReportPathEnv)); reportPath != "" {
		file, openErr := os.OpenFile(reportPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if openErr != nil {
			t.Fatalf("open requested report path: %v", openErr)
		}
		writeErr := servicetest.WriteBrowserConversationReport(file, result, metadata)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("write requested report path: %v", errors.Join(writeErr, closeErr))
		}
	}
	t.Log(report)
	if validatorErr != nil {
		t.Fatalf("validator command did not return a structured verdict")
	}
	if !mechanical.Passed {
		t.Fatalf("mechanical live acceptance failed: %s", strings.Join(mechanical.Failures, "; "))
	}
	if !verdict.Passed {
		t.Fatalf("validator live acceptance failed: %s", verdict.Summary)
	}
	if os.Getenv(conversationalCustomerPostLaneEnv) == "1" && lane.State != "MERGED" {
		if err := postConversationalCustomerLaneFinding(ctx, report); err != nil {
			t.Fatalf("post sanitized Lane I finding: %v", err)
		}
	}

	_ = labelTerminal
	_ = themeTerminal
	_ = priorityTerminal
	_ = correctionTerminal
}

type conversationalCustomerOracle struct {
	Page        string   `json:"page"`
	Ready       bool     `json:"ready"`
	Label       string   `json:"label"`
	Theme       string   `json:"theme"`
	Priority    string   `json:"priority"`
	Pending     bool     `json:"pending"`
	VisibleText string   `json:"visibleText"`
	Invocations []string `json:"invocations"`
}

type conversationalCustomerPageState struct {
	Page        string `json:"page"`
	Ready       bool   `json:"ready"`
	Label       string `json:"label"`
	Theme       string `json:"theme"`
	Priority    string `json:"priority"`
	Pending     bool   `json:"pending"`
	VisibleText string `json:"visibleText"`
}

type conversationalCustomerFixtureServer struct {
	server *httptest.Server
	mu     sync.Mutex
	oracle conversationalCustomerOracle
}

func newConversationalCustomerFixtureServer() *conversationalCustomerFixtureServer {
	fixture := &conversationalCustomerFixtureServer{oracle: conversationalCustomerOracle{
		Page: conversationalCustomerHomePage, Label: "unset", Theme: "default", Priority: "normal", VisibleText: "unset/default",
	}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/", "/settings":
			if request.Method != http.MethodGet {
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.Header().Set("Origin-Agent-Cluster", "?1")
			writer.Header().Set("Permissions-Policy", "tools=(self)")
			_, _ = writer.Write(conversationalCustomerFixtureHTML)
		case "/__test/conversational-state":
			fixture.handleOracle(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	return fixture
}

func (f *conversationalCustomerFixtureServer) Origin() string { return f.server.URL }

func (f *conversationalCustomerFixtureServer) URL(page string) string {
	if page == conversationalCustomerSettingsPage {
		return f.server.URL + "/settings"
	}
	return f.server.URL + "/"
}

func (f *conversationalCustomerFixtureServer) StateURL() string {
	return f.server.URL + "/__test/conversational-state"
}

func (f *conversationalCustomerFixtureServer) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

func (f *conversationalCustomerFixtureServer) handleOracle(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request.Method {
	case http.MethodGet:
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(f.oracle)
	case http.MethodPost:
		var oracle conversationalCustomerOracle
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&oracle); err != nil {
			http.Error(writer, "invalid oracle", http.StatusBadRequest)
			return
		}
		oracle.Invocations = append([]string(nil), oracle.Invocations...)
		f.oracle = oracle
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newConversationalCustomerScenario(homeURL, settingsURL string) servicetest.BrowserConversationScenario {
	homeBefore := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, "unset", "default", "normal", false, "unset/default")
	labelAfter := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerLabel, "default", "normal", false, conversationalCustomerLabel+"/default")
	themeAfter := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerLabel, conversationalCustomerTheme, "normal", false, conversationalCustomerLabel+"/"+conversationalCustomerTheme)
	settingsBefore := conversationalCustomerState(settingsURL, conversationalCustomerSettingsPage, true, conversationalCustomerLabel, conversationalCustomerTheme, "normal", false, "normal")
	settingsAfter := conversationalCustomerState(settingsURL, conversationalCustomerSettingsPage, true, conversationalCustomerLabel, conversationalCustomerTheme, conversationalCustomerPriority, false, conversationalCustomerPriority)
	correctionBefore := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerLabel, conversationalCustomerTheme, conversationalCustomerPriority, false, conversationalCustomerLabel+"/"+conversationalCustomerTheme)
	correctionAfter := conversationalCustomerState(homeURL, conversationalCustomerHomePage, true, conversationalCustomerCorrected, conversationalCustomerTheme, conversationalCustomerPriority, false, conversationalCustomerCorrected+"/"+conversationalCustomerTheme)
	return servicetest.BrowserConversationScenario{
		Version: servicetest.BrowserConversationScenarioVersion,
		ID:      "canonical-webmcp-conversational-customer",
		Name:    "canonical WebMCP conversational customer",
		Fixture: servicetest.BrowserConversationFixture{
			ID:          "declarative-conversational-customer",
			Pages:       []servicetest.BrowserConversationPage{{ID: conversationalCustomerHomePage, URL: homeURL}, {ID: conversationalCustomerSettingsPage, URL: settingsURL}},
			InitialPage: conversationalCustomerHomePage,
		},
		RunTimeout: 10 * time.Minute,
		Steps: []servicetest.BrowserConversationStep{
			{ID: "initial_action", Utterance: "Set the customer label to live alpha.", PageID: conversationalCustomerHomePage, ExpectedState: &servicetest.BrowserStateTransition{PageID: conversationalCustomerHomePage, Before: homeBefore, After: labelAfter}, Deadline: 90 * time.Second},
			{ID: "second_action", Utterance: "Now set the customer theme to live dark.", PageID: conversationalCustomerHomePage, ExpectedState: &servicetest.BrowserStateTransition{PageID: conversationalCustomerHomePage, Before: labelAfter, After: themeAfter}, Deadline: 90 * time.Second},
			{ID: "stale_recovery", Utterance: "Set the customer priority to high.", PageID: conversationalCustomerSettingsPage, ExpectedState: &servicetest.BrowserStateTransition{PageID: conversationalCustomerSettingsPage, Before: settingsBefore, After: settingsAfter}, Navigation: &servicetest.BrowserCustomerNavigation{FromPageID: conversationalCustomerHomePage, ToPageID: conversationalCustomerSettingsPage, URL: settingsURL}, Deadline: 120 * time.Second},
			{ID: "correction", Utterance: "Actually change the customer label to live corrected.", PageID: conversationalCustomerHomePage, Navigation: &servicetest.BrowserCustomerNavigation{FromPageID: conversationalCustomerSettingsPage, ToPageID: conversationalCustomerHomePage, URL: homeURL}, Correction: &servicetest.BrowserConversationCorrection{TargetStepID: "initial_action", ExpectedState: servicetest.BrowserStateTransition{PageID: conversationalCustomerHomePage, Before: correctionBefore, After: correctionAfter}}, Deadline: 90 * time.Second},
			{ID: "interrupt", Utterance: "Hold this customer request while I decide.", PageID: conversationalCustomerHomePage, Interrupt: &servicetest.BrowserConversationInterrupt{Trigger: servicetest.BrowserInterruptOnInFlightInvocation, ToolName: "webmcp_customer_pending"}, Deadline: 90 * time.Second},
			{ID: "cancel", Utterance: "Stop and cancel that request.", PageID: conversationalCustomerHomePage, Cancel: &servicetest.BrowserConversationCancelRequest{Reason: "customer explicitly stopped the pending request"}, Deadline: 90 * time.Second},
		},
		PostSession: servicetest.BrowserConversationTabStateRequired{PageID: conversationalCustomerHomePage, MustRemainAlive: true, MustBeResponsive: true, MustAllowMutation: true},
	}
}

func conversationalCustomerState(_ string, page string, ready bool, label, theme, priority string, pending bool, visible string) json.RawMessage {
	state, _ := json.Marshal(conversationalCustomerPageState{Page: page, Ready: ready, Label: label, Theme: theme, Priority: priority, Pending: pending, VisibleText: visible})
	return state
}

func conversationalCustomerSystemPrompt() string {
	return `You are operating a real declarative WebMCP customer fixture through the browser tools in this session. Follow each customer request in order. Use webmcp_list_tools to discover current page tools and use only the exact current tool_ref and a syntactically valid JSON object string in webmcp_invoke. Never invent, reuse, or receive tool references or encoded arguments out of band. After customer navigation, list tools again; if a stale_tool_ref error occurs, retain that failed attempt as evidence and retry only with a freshly listed reference. Perform the requested page mutation before speaking confirmation, and ground confirmation in the resulting page state. A customer interruption or stop request cancels in-flight work; never claim a canceled action completed.`
}

func conversationalCustomerModel() string {
	if value := strings.TrimSpace(os.Getenv(conversationalCustomerModelEnv)); value != "" {
		return value
	}
	return "gpt-realtime"
}

func requiredNewlineFreeEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when %s=1", name, conversationalCustomerLiveEnv)
	}
	if strings.ContainsAny(value, "\r\n") {
		t.Fatalf("%s must not contain newline characters", name)
	}
	return value
}

func conversationalCustomerAudioPaths(t *testing.T) []string {
	t.Helper()
	directory := requiredNewlineFreeEnv(t, conversationalCustomerAudioDirEnv)
	names := []string{"initial.wav", "second.wav", "navigation.wav", "correction.wav", "interrupt.wav", "cancel.wav"}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			t.Fatalf("finite audio fixture %q must be a non-empty regular file", path)
		}
		paths = append(paths, path)
	}
	return paths
}

func conversationalCustomerValidatorCommand(t *testing.T) []string {
	t.Helper()
	raw := requiredNewlineFreeEnv(t, conversationalCustomerValidatorEnv)
	var command []string
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		if err := json.Unmarshal([]byte(raw), &command); err != nil {
			t.Fatalf("decode %s JSON command: %v", conversationalCustomerValidatorEnv, err)
		}
	} else {
		command = strings.Fields(raw)
	}
	if len(command) == 0 {
		t.Fatalf("%s must contain an executable", conversationalCustomerValidatorEnv)
	}
	for _, part := range command {
		if conversationalCustomerContainsCredentialMarker(part) {
			t.Fatalf("%s contains credential-shaped command data", conversationalCustomerValidatorEnv)
		}
	}
	return command
}

func conversationalCustomerContainsCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"api_key", "api-key", "authorization:", "bearer ", "access_token", "refresh_token", "client_secret", "password", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

type conversationalCustomerLaneStatus struct {
	State       string          `json:"state"`
	HeadRefName string          `json:"headRefName"`
	MergeCommit json.RawMessage `json:"mergeCommit"`
	URL         string          `json:"url"`
}

func readConversationalCustomerLaneStatus(t *testing.T, ctx context.Context) conversationalCustomerLaneStatus {
	t.Helper()
	command := exec.CommandContext(ctx, "gh", "pr", "view", conversationalCustomerLaneNumber, "--json", "state,headRefName,mergeCommit,url")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("read Lane I PR status: %v", err)
	}
	var status conversationalCustomerLaneStatus
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatalf("decode Lane I PR status: %v", err)
	}
	status.State = strings.ToUpper(strings.TrimSpace(status.State))
	if status.State != "MERGED" && status.State != "OPEN" {
		t.Fatalf("Lane I PR status %q is not a supported pre-run state", status.State)
	}
	if status.HeadRefName == "" {
		t.Fatalf("Lane I PR status omitted headRefName")
	}
	return status
}

func conversationalCustomerSourceRoot(t *testing.T, lane conversationalCustomerLaneStatus) string {
	t.Helper()
	var name, wantBranch string
	if lane.State == "MERGED" {
		name, wantBranch = conversationalCustomerMainRootEnv, "main"
	} else {
		name, wantBranch = conversationalCustomerLaneRootEnv, lane.HeadRefName
	}
	root := requiredNewlineFreeEnv(t, name)
	command := exec.Command("git", "-C", root, "branch", "--show-current")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("inspect %s source branch: %v", name, err)
	}
	if branch := strings.TrimSpace(string(output)); branch != wantBranch {
		t.Fatalf("%s source branch = %q, want %q", name, branch, wantBranch)
	}
	return root
}

func selectConversationalCustomerTarget(ctx context.Context, binaryPath, configDir, fixtureURL string) (string, webmcp.TargetID, error) {
	browsers, err := runConversationalCustomerJSONCommand(ctx, binaryPath, configDir, "webmcp", "browsers", "--json")
	if err != nil {
		return "", "", err
	}
	var browserData struct {
		Browsers []struct {
			ID string `json:"id"`
		} `json:"browsers"`
	}
	if err := json.Unmarshal(browsers, &browserData); err != nil || len(browserData.Browsers) != 1 || browserData.Browsers[0].ID == "" {
		return "", "", errors.New("pinned Chrome discovery did not return exactly one browser")
	}
	browserID := browserData.Browsers[0].ID
	tabs, err := runConversationalCustomerJSONCommand(ctx, binaryPath, configDir, "webmcp", "tabs", "--browser", browserID, "--eligible", "--json")
	if err != nil {
		return "", "", err
	}
	var tabData struct {
		Tabs []struct {
			TargetID string `json:"target_id"`
			Origin   string `json:"origin"`
			URL      string `json:"url"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(tabs, &tabData); err != nil {
		return "", "", errors.New("decode pinned Chrome target discovery")
	}
	var targetID webmcp.TargetID
	for _, tab := range tabData.Tabs {
		if tab.TargetID != "" && (tab.URL == "" || tab.URL == fixtureURL) {
			if targetID != "" {
				return "", "", errors.New("pinned Chrome target discovery was ambiguous")
			}
			targetID = webmcp.TargetID(tab.TargetID)
		}
	}
	if targetID == "" {
		return "", "", errors.New("pinned Chrome target discovery omitted the fixture target")
	}
	if _, err := runConversationalCustomerJSONCommand(ctx, binaryPath, configDir, "webmcp", "select", "--browser", browserID, "--tab", string(targetID), "--json"); err != nil {
		return "", "", err
	}
	return browserID, targetID, nil
}

func runConversationalCustomerJSONCommand(ctx context.Context, binaryPath, configDir string, args ...string) (json.RawMessage, error) {
	process, err := startGateCommand(ctx, binaryPath, configDir, args...)
	if err != nil {
		return nil, errors.New("start browser command")
	}
	result, waitErr := process.wait(ctx)
	if waitErr != nil || result.Err != nil || result.ExitCode != 0 {
		return nil, errors.New("browser command failed")
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(result.Stdout))
	if err != nil || !envelope.OK {
		return nil, errors.New("browser command returned a failed result")
	}
	return append(json.RawMessage(nil), envelope.Data...), nil
}

func openConversationalCustomerObserver(ctx context.Context, browserID string, targetID webmcp.TargetID, version devToolsVersion) (webmcp.TargetSession, func() error, error) {
	candidate := webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID(browserID),
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      version.Browser,
		Protocol:     version.ProtocolVersion,
		HTTPURL:      browserHTTPURL(version.WebSocketDebuggerURL),
		BrowserWSURL: version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	runtime := NewRuntime(WithEventBuffer(512), WithCommandTimeout(20*time.Second))
	handle, err := runtime.Open(ctx, candidate)
	if err != nil {
		return nil, nil, err
	}
	session, err := handle.Attach(ctx, targetID, webmcp.TargetOwnershipExternal)
	if err != nil {
		_ = handle.Close()
		return nil, nil, err
	}
	if err := session.EnableWebMCP(ctx); err != nil {
		_ = session.Close()
		_ = handle.Close()
		return nil, nil, err
	}
	closeObserver := func() error {
		sessionErr := session.Close()
		handleErr := handle.Close()
		return errors.Join(sessionErr, handleErr)
	}
	return session, closeObserver, nil
}

type conversationalCustomerEventCollector struct {
	mu      sync.Mutex
	events  []webmcp.BrowserEvent
	changed chan struct{}
}

func newConversationalCustomerEventCollector() *conversationalCustomerEventCollector {
	return &conversationalCustomerEventCollector{changed: make(chan struct{})}
}

func (c *conversationalCustomerEventCollector) consume(session webmcp.TargetSession) {
	for event := range session.Events() {
		c.mu.Lock()
		c.events = append(c.events, event)
		close(c.changed)
		c.changed = make(chan struct{})
		c.mu.Unlock()
	}
}

func (c *conversationalCustomerEventCollector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *conversationalCustomerEventCollector) snapshot() []webmcp.BrowserEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]webmcp.BrowserEvent(nil), c.events...)
}

func (c *conversationalCustomerEventCollector) wait(ctx context.Context, start int, match func(webmcp.BrowserEvent) bool) (webmcp.BrowserEvent, error) {
	for {
		c.mu.Lock()
		for index := start; index < len(c.events); index++ {
			if match(c.events[index]) {
				event := c.events[index]
				c.mu.Unlock()
				return event, nil
			}
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return webmcp.BrowserEvent{}, ctx.Err()
		}
	}
}

func navigateConversationalCustomerTarget(ctx context.Context, endpoint string, targetID webmcp.TargetID, pageURL string) error {
	rootContext, cancelRoot := context.WithTimeout(ctx, 20*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		_ = detachExternalIntegrationTarget(targetContext, cancelTarget)
		cancelAllocator()
	}()
	return chromedp.Run(targetContext, chromedp.Navigate(pageURL))
}

type conversationalCustomerNavigationObservation struct {
	StepID string
	Event  webmcp.BrowserEvent
}

type conversationalCustomerOracleObservation struct {
	StepID string
	Phase  servicetest.BrowserConversationOraclePhase
	Oracle conversationalCustomerOracle
}

type conversationalCustomerProbe struct {
	PageID            string
	BrowserID         webmcp.BrowserID
	TargetID          webmcp.TargetID
	Alive             bool
	Responsive        bool
	AllowsMutation    bool
	ReadSucceeded     bool
	MutationSucceeded bool
}

type conversationalCustomerCancelResult struct {
	InvocationID string
	Status       string
}

func cancelConversationalCustomerInvocation(ctx context.Context, binaryPath, configDir string, invocationID webmcp.InvocationID) (conversationalCustomerCancelResult, error) {
	data, err := runConversationalCustomerJSONCommand(ctx, binaryPath, configDir, "webmcp", "cancel", "--invocation", string(invocationID), "--json")
	if err != nil {
		return conversationalCustomerCancelResult{}, err
	}
	var result conversationalCustomerCancelResult
	if err := json.Unmarshal(data, &result); err != nil {
		return conversationalCustomerCancelResult{}, errors.New("decode cancellation result")
	}
	return result, nil
}

func inspectConversationalCustomerTarget(ctx context.Context, endpoint, browserID string, targetID webmcp.TargetID) (conversationalCustomerProbe, error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 20*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		_ = detachExternalIntegrationTarget(targetContext, cancelTarget)
		cancelAllocator()
	}()
	if err := chromedp.Run(targetContext, chromedp.WaitReady("#state")); err != nil {
		return conversationalCustomerProbe{}, err
	}
	var state conversationalCustomerOracle
	if err := chromedp.Run(targetContext, chromedp.Evaluate(conversationalCustomerPageStateExpression(), &state)); err != nil {
		return conversationalCustomerProbe{}, err
	}
	var mutationSucceeded bool
	if err := chromedp.Run(targetContext, chromedp.Evaluate(`(() => { const state = document.querySelector("#state"); if (!state) return false; state.dataset.detachProbe = "responsive"; return state.dataset.detachProbe === "responsive"; })()`, &mutationSucceeded)); err != nil {
		return conversationalCustomerProbe{}, err
	}
	return conversationalCustomerProbe{PageID: state.Page, BrowserID: webmcp.BrowserID(browserID), TargetID: targetID, Alive: true, Responsive: state.Ready, AllowsMutation: mutationSucceeded, ReadSucceeded: true, MutationSucceeded: mutationSucceeded}, nil
}

func conversationalCustomerPageStateExpression() string {
	return `(() => { const state = window.__webmcpConversationalCustomer; const visible = document.querySelector("#state"); return { page: state && state.page ? String(state.page) : "", ready: Boolean(state && state.ready), label: state && state.label !== undefined ? String(state.label) : "", theme: state && state.theme !== undefined ? String(state.theme) : "", priority: state && state.priority !== undefined ? String(state.priority) : "", pending: Boolean(state && state.pending), visibleText: visible ? String(visible.textContent || "") : "" }; })()`
}

func readConversationalCustomerOracle(ctx context.Context, endpoint string) (conversationalCustomerOracle, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return conversationalCustomerOracle{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return conversationalCustomerOracle{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return conversationalCustomerOracle{}, fmt.Errorf("fixture oracle HTTP status: %s", response.Status)
	}
	var oracle conversationalCustomerOracle
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&oracle); err != nil {
		return conversationalCustomerOracle{}, err
	}
	return oracle, nil
}

func waitForConversationalCustomerOracle(ctx context.Context, endpoint string, match func(conversationalCustomerOracle) bool) (conversationalCustomerOracle, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last conversationalCustomerOracle
	var lastErr error
	for {
		oracle, err := readConversationalCustomerOracle(ctx, endpoint)
		if err == nil {
			last = oracle
			if match(oracle) {
				return oracle, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return last, fmt.Errorf("wait for conversational customer oracle: %w (last=%+v err=%v)", ctx.Err(), last, lastErr)
		}
	}
}

func conversationalCustomerOracleState(oracle conversationalCustomerOracle) json.RawMessage {
	state, _ := json.Marshal(conversationalCustomerPageState{Page: oracle.Page, Ready: oracle.Ready, Label: oracle.Label, Theme: oracle.Theme, Priority: oracle.Priority, Pending: oracle.Pending, VisibleText: oracle.VisibleText})
	return state
}

func conversationalCustomerOracleHasInvocation(oracle conversationalCustomerOracle, value string) bool {
	for _, invocation := range oracle.Invocations {
		if invocation == value {
			return true
		}
	}
	return false
}

type conversationalCustomerSessionLogEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		Text string `json:"text"`
	} `json:"input"`
	Response struct {
		Text     string `json:"text"`
		Complete bool   `json:"complete"`
	} `json:"response"`
}

func readConversationalCustomerSessionLog(path string) ([]conversationalCustomerSessionLogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var entries []conversationalCustomerSessionLogEntry
	for scanner.Scan() {
		var entry conversationalCustomerSessionLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

type conversationalCustomerProviderCall struct {
	Name      string
	Arguments string
	ToolRef   webmcp.ToolRef
	ToolName  string
	InputJSON string
	CallID    string
}

func readConversationalCustomerProviderCalls(path string) ([]conversationalCustomerProviderCall, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var calls []conversationalCustomerProviderCall
	for scanner.Scan() {
		var record looptranscript.Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		var event struct {
			Type      string          `json:"type"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Item      json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil || event.Type != "response.function_call_arguments.done" {
			continue
		}
		arguments := conversationalCustomerFunctionCallArguments(event.Arguments)
		call := conversationalCustomerProviderCall{Name: event.Name, Arguments: arguments, CallID: event.CallID}
		if call.Name == "" && len(event.Item) > 0 {
			var item struct {
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if json.Unmarshal(event.Item, &item) == nil {
				call.Name = item.Name
				if call.CallID == "" {
					call.CallID = item.CallID
				}
			}
		}
		if call.Name == webmcp.InvokeToolName {
			call.ToolRef, call.InputJSON = conversationalCustomerInvokeArguments(arguments)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return calls, nil
}

func conversationalCustomerFunctionCallArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err == nil {
			return decoded
		}
	}
	return string(raw)
}

// conversationalCustomerInvokeArguments preserves malformed input_json as
// the exact raw value. A failed outer function-call decode must remain a
// visible invalid attempt in the report instead of disappearing from the
// reconstructed provider trace.
func conversationalCustomerInvokeArguments(arguments string) (webmcp.ToolRef, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &fields); err != nil || fields == nil {
		return "", arguments
	}
	var toolRef string
	if raw, ok := fields["tool_ref"]; ok {
		_ = json.Unmarshal(raw, &toolRef)
	}
	rawInput, ok := fields["input_json"]
	if !ok {
		return webmcp.ToolRef(toolRef), ""
	}
	if strings.TrimSpace(string(rawInput)) == "null" {
		return webmcp.ToolRef(toolRef), string(rawInput)
	}
	var input string
	if err := json.Unmarshal(rawInput, &input); err == nil {
		return webmcp.ToolRef(toolRef), input
	}
	return webmcp.ToolRef(toolRef), string(rawInput)
}

func buildConversationalCustomerResult(
	scenario servicetest.BrowserConversationScenario,
	events []webmcp.BrowserEvent,
	logPath string,
	navigations []conversationalCustomerNavigationObservation,
	oracles []conversationalCustomerOracleObservation,
	browserID string,
	targetID webmcp.TargetID,
	probe conversationalCustomerProbe,
	pending webmcp.BrowserEvent,
	cancel conversationalCustomerCancelResult,
) (servicetest.BrowserConversationResult, error) {
	providerCalls, err := readConversationalCustomerProviderCalls(filepath.Join(filepath.Dir(logPath), "agent.transcript.jsonl"))
	if err != nil {
		return servicetest.BrowserConversationResult{}, fmt.Errorf("read agent transcript: %w", err)
	}
	logs, err := readConversationalCustomerSessionLog(logPath)
	if err != nil {
		return servicetest.BrowserConversationResult{}, fmt.Errorf("read session log: %w", err)
	}
	turns := make([]servicetest.BrowserConversationTurn, 0, len(logs)*2)
	logStepIDs := conversationalCustomerLogStepIDs(scenario, logs)
	for index, entry := range logs {
		stepID := logStepIDs[index]
		if strings.TrimSpace(entry.Input.Text) != "" {
			turns = append(turns, servicetest.BrowserConversationTurn{StepID: stepID, Direction: servicetest.BrowserConversationCustomerTurn, ExpectedText: expectedStepTextForStep(scenario, stepID), ObservedText: entry.Input.Text, Complete: entry.Response.Complete})
		}
		if strings.TrimSpace(entry.Response.Text) != "" {
			turns = append(turns, servicetest.BrowserConversationTurn{StepID: stepID, Direction: servicetest.BrowserConversationAssistantTurn, ObservedText: entry.Response.Text, Complete: entry.Response.Complete})
		}
	}

	toolNames := make(map[webmcp.ToolRef]string)
	toolGenerations := make(map[webmcp.ToolRef]uint64)
	toolRefsByGeneration := make(map[uint64]map[webmcp.ToolRef]struct{})
	var firstGeneration uint64
	for _, event := range events {
		for _, tool := range event.Tools {
			toolNames[tool.Ref] = tool.Name
			toolGenerations[tool.Ref] = tool.Generation
			if tool.Generation != 0 {
				refs := toolRefsByGeneration[tool.Generation]
				if refs == nil {
					refs = make(map[webmcp.ToolRef]struct{})
					toolRefsByGeneration[tool.Generation] = refs
				}
				refs[tool.Ref] = struct{}{}
				if firstGeneration == 0 || tool.Generation < firstGeneration {
					firstGeneration = tool.Generation
				}
			}
		}
	}
	terminalByInvocation := make(map[webmcp.InvocationID]webmcp.BrowserEvent)
	for _, event := range events {
		if event.Type == webmcp.EventToolResponded && event.InvocationID != "" {
			terminalByInvocation[event.InvocationID] = event
		}
	}
	matchedInvocation := make(map[webmcp.InvocationID]bool)
	var calls []servicetest.BrowserConversationBrokerCall
	appendCall := func(call servicetest.BrowserConversationBrokerCall) {
		call.Sequence = uint64(len(calls) + 1)
		calls = append(calls, call)
	}
	navigationByStep := make(map[string]webmcp.BrowserEvent)
	for _, navigation := range navigations {
		navigationByStep[navigation.StepID] = navigation.Event
	}
	navigationAdded := make(map[string]bool)
	appendNavigation := func(stepID string) {
		if navigationAdded[stepID] {
			return
		}
		event, ok := navigationByStep[stepID]
		if !ok {
			return
		}
		navigationAdded[stepID] = true
		input := json.RawMessage(`{}`)
		for _, step := range scenario.Steps {
			if step.ID == stepID && step.Navigation != nil {
				if encoded, err := json.Marshal(step.Navigation); err == nil {
					input = encoded
				}
				break
			}
		}
		appendCall(servicetest.BrowserConversationBrokerCall{
			StepID: stepID, Operation: servicetest.BrowserConversationCustomerNavigate,
			InputJSON: string(input), Generation: event.Generation,
			PreviousGeneration: event.PreviousGeneration,
		})
	}
	lastStep := "initial_action"
	labelCount := 0
	themeCount := 0
	priorityCount := 0
	for _, providerCall := range providerCalls {
		if providerCall.Name == webmcp.ListToolsToolName {
			var stepID string
			switch {
			case labelCount == 0:
				stepID = "initial_action"
			case themeCount == 0:
				stepID = "second_action"
			case priorityCount >= 2:
				stepID = "correction"
			default:
				stepID = "stale_recovery"
			}
			appendNavigation(stepID)
			refs, generation := conversationalCustomerCurrentToolRefs(toolNames, toolRefsByGeneration, stepID, navigationByStep, firstGeneration)
			appendCall(servicetest.BrowserConversationBrokerCall{StepID: stepID, Operation: servicetest.BrowserConversationListTools, InputJSON: providerCall.Arguments, Generation: generation, ToolRefs: refs})
			lastStep = stepID
			continue
		}
		if providerCall.Name == webmcp.CancelToolName {
			appendCall(servicetest.BrowserConversationBrokerCall{StepID: "cancel", Operation: servicetest.BrowserConversationCancel, InputJSON: providerCall.Arguments, State: webmcp.InvocationCanceled, Terminal: true})
			continue
		}
		if providerCall.Name != webmcp.InvokeToolName {
			continue
		}
		toolName := providerCall.ToolName
		if toolName == "" {
			toolName = toolNames[providerCall.ToolRef]
		}
		stepID := ""
		switch toolName {
		case "webmcp_customer_set_label":
			if labelCount == 0 {
				stepID = "initial_action"
			} else {
				stepID = "correction"
			}
			labelCount++
		case "webmcp_customer_set_theme":
			stepID = "second_action"
			themeCount++
		case "webmcp_customer_set_priority":
			stepID = "stale_recovery"
			priorityCount++
		case "webmcp_customer_pending":
			stepID = "interrupt"
		}
		if stepID == "" {
			stepID = lastStep
		}
		appendNavigation(stepID)
		lastStep = stepID
		appendCall(servicetest.BrowserConversationBrokerCall{StepID: stepID, Operation: servicetest.BrowserConversationInvoke, ToolRef: providerCall.ToolRef, ToolName: toolName, InputJSON: providerCall.InputJSON, State: webmcp.InvocationDispatched, Terminal: false, Generation: toolGenerations[providerCall.ToolRef]})
		matched := false
		for _, event := range events {
			providerGeneration := toolGenerations[providerCall.ToolRef]
			if event.Type != webmcp.EventToolInvoked || event.InvocationID == "" || matchedInvocation[event.InvocationID] || event.ToolName != toolName || (providerCall.ToolRef != "" && event.ToolName != "" && toolNames[providerCall.ToolRef] != "" && event.ToolName != toolNames[providerCall.ToolRef]) || (providerGeneration != 0 && event.Generation != 0 && providerGeneration != event.Generation) {
				continue
			}
			matchedInvocation[event.InvocationID] = true
			terminal, ok := terminalByInvocation[event.InvocationID]
			if !ok {
				continue
			}
			appendCall(servicetest.BrowserConversationBrokerCall{StepID: stepID, Operation: servicetest.BrowserConversationInvoke, ToolRef: providerCall.ToolRef, ToolName: toolName, InvocationID: event.InvocationID, InputJSON: providerCall.InputJSON, State: conversationalCustomerInvocationState(terminal), Terminal: true, Output: conversationalCustomerJSON(terminal.Output), ErrorCode: terminal.ErrorCode, Generation: terminal.Generation, PreviousGeneration: terminal.PreviousGeneration})
			matched = true
			break
		}
		if !matched && toolName == "webmcp_customer_set_priority" && toolGenerations[providerCall.ToolRef] != 0 {
			if navigationEvent, ok := navigationByStep["stale_recovery"]; ok && toolGenerations[providerCall.ToolRef] <= navigationEvent.PreviousGeneration {
				appendCall(servicetest.BrowserConversationBrokerCall{StepID: stepID, Operation: servicetest.BrowserConversationInvoke, ToolRef: providerCall.ToolRef, ToolName: toolName, InputJSON: providerCall.InputJSON, State: webmcp.InvocationError, Terminal: true, ErrorCode: string(webmcp.ErrorStaleToolRef), Generation: toolGenerations[providerCall.ToolRef]})
			}
		}
	}
	if pending.InvocationID != "" {
		found := false
		for _, call := range calls {
			if call.InvocationID == pending.InvocationID {
				found = true
				break
			}
		}
		if !found {
			if terminal, ok := terminalByInvocation[pending.InvocationID]; ok {
				appendCall(servicetest.BrowserConversationBrokerCall{StepID: "interrupt", Operation: servicetest.BrowserConversationInvoke, ToolName: pending.ToolName, InvocationID: pending.InvocationID, InputJSON: string(pending.Input), State: conversationalCustomerInvocationState(terminal), Terminal: true, ErrorCode: terminal.ErrorCode, Generation: terminal.Generation})
			}
		}
	}
	if cancel.InvocationID != "" {
		cancelInput, _ := json.Marshal(struct {
			InvocationID string `json:"invocation_id"`
		}{InvocationID: cancel.InvocationID})
		appendCall(servicetest.BrowserConversationBrokerCall{StepID: "cancel", Operation: servicetest.BrowserConversationCancel, InvocationID: webmcp.InvocationID(cancel.InvocationID), InputJSON: string(cancelInput), State: webmcp.InvocationCanceled, Terminal: true})
	}
	for _, step := range scenario.Steps {
		if step.Navigation != nil {
			appendNavigation(step.ID)
		}
	}
	assignConversationalCustomerTurnSequences(turns, calls)

	var oracleSnapshots []servicetest.BrowserConversationOracleSnapshot
	for index, observation := range oracles {
		oracleSnapshots = append(oracleSnapshots, servicetest.BrowserConversationOracleSnapshot{Sequence: uint64(index + 1), StepID: observation.StepID, PageID: observation.Oracle.Page, Generation: 0, Phase: observation.Phase, State: conversationalCustomerOracleState(observation.Oracle)})
	}
	lifecycle := servicetest.BrowserConversationLifecycleEvidence{Outcome: servicetest.BrowserConversationLifecycleCanceled, SessionStarted: true, SessionTerminated: true, Detached: true, DetachCount: 1, DetachRequired: true, ExternalBrowserID: webmcp.BrowserID(browserID), ExternalTargetID: targetID, ExternalTabAlive: probe.Alive, ExternalTabResponsive: probe.Responsive, ExternalTabAllowsMutation: probe.AllowsMutation, ExternalTabRead: probe.ReadSucceeded, ExternalTabMutation: probe.MutationSucceeded}
	result := servicetest.BrowserConversationResult{ScenarioID: scenario.ID, ScenarioName: scenario.Name, Finalized: true, Turns: turns, BrokerCalls: calls, Oracles: oracleSnapshots, Cancellation: servicetest.BrowserConversationCancellationEvidence{Interrupted: true, Requested: true, InvocationID: pending.InvocationID, FinalState: webmcp.InvocationCanceled, Reason: "customer stop", InterruptedStepID: "interrupt", CancelStepID: "cancel", OverlappingAudioSent: true, ExplicitCancelAudioSent: true}, Lifecycle: lifecycle}
	result.Corrections = servicetest.DeriveBrowserConversationCorrections(scenario, result)
	result.Recovery = servicetest.DeriveBrowserConversationRecovery(scenario, result)
	result.InputJSONValidity = servicetest.ComputeBrowserConversationInputJSONValidity(result.BrokerCalls)
	return result, result.Validate()
}

func expectedStepTextForStep(scenario servicetest.BrowserConversationScenario, stepID string) string {
	for _, step := range scenario.Steps {
		if step.ID == stepID {
			return step.Utterance
		}
	}
	return ""
}

func conversationalCustomerLogStepIDs(scenario servicetest.BrowserConversationScenario, logs []conversationalCustomerSessionLogEntry) []string {
	stepIDs := make([]string, len(logs))
	nextStep := 0
	lastStep := ""
	for index, entry := range logs {
		text := strings.TrimSpace(entry.Input.Text)
		if text == "" {
			stepIDs[index] = lastStep
			continue
		}
		matched := -1
		for candidate := nextStep; candidate < len(scenario.Steps); candidate++ {
			if strings.EqualFold(strings.TrimSpace(scenario.Steps[candidate].Utterance), text) {
				matched = candidate
				break
			}
		}
		if matched >= 0 {
			nextStep = matched + 1
			lastStep = scenario.Steps[matched].ID
			stepIDs[index] = lastStep
			continue
		}
		// The canonical run sends the interruption utterance once to start the
		// pending tool and may send the same audio again as overlap. Preserve
		// that duplicate under the declared interruption step rather than
		// inventing a seventh scenario step.
		for candidate := 0; candidate < nextStep && candidate < len(scenario.Steps); candidate++ {
			if scenario.Steps[candidate].Interrupt != nil && strings.EqualFold(strings.TrimSpace(scenario.Steps[candidate].Utterance), text) {
				lastStep = scenario.Steps[candidate].ID
				stepIDs[index] = lastStep
				matched = candidate
				break
			}
		}
		if matched >= 0 {
			continue
		}
		if nextStep < len(scenario.Steps) {
			lastStep = scenario.Steps[nextStep].ID
			nextStep++
			stepIDs[index] = lastStep
		}
	}
	return stepIDs
}

func assignConversationalCustomerTurnSequences(turns []servicetest.BrowserConversationTurn, calls []servicetest.BrowserConversationBrokerCall) {
	type bounds struct{ first, last uint64 }
	byStep := make(map[string]bounds)
	for _, call := range calls {
		if call.StepID == "" || call.Sequence == 0 {
			continue
		}
		current := byStep[call.StepID]
		if current.first == 0 || call.Sequence < current.first {
			current.first = call.Sequence
		}
		if call.Sequence > current.last {
			current.last = call.Sequence
		}
		byStep[call.StepID] = current
	}
	next := uint64(len(calls) + 1)
	for index := range turns {
		current, ok := byStep[turns[index].StepID]
		if !ok {
			turns[index].Sequence = next
			next++
			continue
		}
		if turns[index].Direction == servicetest.BrowserConversationCustomerTurn {
			turns[index].Sequence = current.first
		} else {
			turns[index].Sequence = current.last + 1
		}
	}
}

func conversationalCustomerInvocationState(event webmcp.BrowserEvent) webmcp.InvocationState {
	switch strings.ToLower(strings.TrimSpace(event.Status)) {
	case "completed":
		return webmcp.InvocationCompleted
	case "canceled", "cancelled":
		return webmcp.InvocationCanceled
	case "timed_out", "timeout", "timedout":
		return webmcp.InvocationTimedOut
	default:
		return webmcp.InvocationError
	}
}

func conversationalCustomerJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	if json.Valid(value) {
		return append(json.RawMessage(nil), value...)
	}
	encoded, _ := json.Marshal(string(value))
	return encoded
}

func conversationalCustomerCurrentToolRefs(
	names map[webmcp.ToolRef]string,
	refsByGeneration map[uint64]map[webmcp.ToolRef]struct{},
	stepID string,
	navigationByStep map[string]webmcp.BrowserEvent,
	firstGeneration uint64,
) ([]webmcp.ToolRef, uint64) {
	generation := firstGeneration
	if navigation, ok := navigationByStep[stepID]; ok && navigation.Generation != 0 {
		generation = navigation.Generation
	}
	set := refsByGeneration[generation]
	refs := make([]webmcp.ToolRef, 0, len(set))
	for ref := range set {
		if names[ref] != "" {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		for ref, name := range names {
			if name == "" {
				continue
			}
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left] < refs[right] })
	return refs, generation
}

func sanitizedValidatorEnvironment() []string {
	result := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || key == "AGENT_MODEL__OPENAI__API_KEY" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func postConversationalCustomerLaneFinding(ctx context.Context, report string) error {
	path := filepath.Join(os.TempDir(), "webmcp-conversational-customer-lane-i-report.md")
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		return err
	}
	defer os.Remove(path)
	command := exec.CommandContext(ctx, "gh", "pr", "comment", conversationalCustomerLaneNumber, "--body-file", path)
	command.Stdin = nil
	if _, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("gh comment failed: %w", err)
	}
	return nil
}
