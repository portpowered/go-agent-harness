package chrome

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	browserconversation "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
	browserconversationWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation/wire"
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
	conversationalCustomerLaneMerged    = "MERGED"

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
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for darwin/arm64, observed %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	live := launchConversationalCustomerLive(t, ctx)
	live.startObserver(t, ctx)
	live.startSession(t, ctx)

	// The event boundaries below are the customer-navigation clock. Each
	// navigation is issued after the preceding browser terminal event, before
	// the scheduler can release the next turn, and then admitted only after the
	// independent target reports the new page.
	var observed conversationalCustomerObserved
	live.observeInitialActions(t, ctx, &observed)
	live.observeStaleRecovery(t, ctx, &observed)
	live.observeCorrection(t, ctx, &observed)
	live.cancelPendingInvocation(t, ctx, &observed)

	// The explicit customer cancel turn is the final scheduled audio input. It
	// is released only after the external browser cancellation unblocks the
	// pending invocation; let the shared scheduled-audio close path deliver
	// that turn and terminate the production session. Cancelling the process
	// here would drop the very customer turn this acceptance run must prove.
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cleanupCancel()
	live.finishSession(t, ctx, cleanupContext, &observed)

	result, mechanical, verdict, validatorErr := live.evaluate(t, observed)
	report, metadata := live.renderReport(t, result)
	live.writeRequestedReport(t, result, metadata)
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
	if os.Getenv(conversationalCustomerPostLaneEnv) == "1" && live.lane.State != conversationalCustomerLaneMerged {
		if err := postConversationalCustomerLaneFinding(ctx, report); err != nil {
			t.Fatalf("post sanitized Lane I finding: %v", err)
		}
	}
}

// conversationalCustomerLive carries the browser, binary, and observer state
// shared by the phases of the credentialed conversational acceptance run.
type conversationalCustomerLive struct {
	lane             conversationalCustomerLaneStatus
	audioPaths       []string
	validatorCommand []string
	workDir          string
	fixture          *conversationalCustomerFixtureServer
	homeURL          string
	settingsURL      string
	browser          *runningChrome
	baseURL          string
	version          devToolsVersion
	binaryPath       string
	configDir        string
	browserID        string
	targetID         webmcp.TargetID
	collector        *conversationalCustomerEventCollector
	observerClose    func() error
	scenario         browserconversation.BrowserConversationScenario
	browserService   browserconversation.Service
	initialOracle    conversationalCustomerOracle
	recordDir        string
	session          *gateCLIProcess
}

// conversationalCustomerObserved holds the browser events and independent
// oracle readings captured at each customer-navigation boundary.
type conversationalCustomerObserved struct {
	labelAfter         conversationalCustomerOracle
	themeAfter         conversationalCustomerOracle
	settingsNavigation webmcp.BrowserEvent
	settingsBefore     conversationalCustomerOracle
	settingsAfter      conversationalCustomerOracle
	homeNavigation     webmcp.BrowserEvent
	correctionBefore   conversationalCustomerOracle
	correctionAfter    conversationalCustomerOracle
	pending            webmcp.BrowserEvent
	cancelResult       conversationalCustomerCancelResult
	postProbe          conversationalCustomerProbe
	postOracle         conversationalCustomerOracle
}

func launchConversationalCustomerLive(t *testing.T, ctx context.Context) *conversationalCustomerLive {
	t.Helper()
	live := &conversationalCustomerLive{}
	apiKey := requiredNewlineFreeEnv(t, conversationalCustomerAPIKeyEnv)
	// The production config loader consumes this scoped environment variable.
	// It is intentionally never passed as a process argument or report field.
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", apiKey)
	live.audioPaths = conversationalCustomerAudioPaths(t)
	live.validatorCommand = conversationalCustomerValidatorCommand(t)
	live.lane = readConversationalCustomerLaneStatus(t, ctx)
	sourceRoot := conversationalCustomerSourceRoot(t, live.lane)

	live.workDir = t.TempDir()
	pinned, err := acquirePinnedChrome(ctx, live.workDir)
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}

	live.fixture = newConversationalCustomerFixtureServer()
	t.Cleanup(live.fixture.Close)
	live.homeURL = live.fixture.URL(conversationalCustomerHomePage)
	live.settingsURL = live.fixture.URL(conversationalCustomerSettingsPage)

	live.browser, err = launchPinnedChrome(ctx, pinned, live.homeURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := live.browser.Close(); closeErr != nil {
			t.Logf("Chrome cleanup: %v", closeErr)
		}
	})

	live.baseURL = browserHTTPURL(live.browser.endpoint())
	live.version, err = waitForDevToolsVersion(ctx, live.baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	target, err := waitForFixturePageTarget(ctx, live.baseURL, live.homeURL)
	if err != nil {
		t.Fatalf("discover exact conversational fixture target: %v", err)
	}
	live.selectTarget(t, ctx, sourceRoot, target)
	return live
}

func (l *conversationalCustomerLive) selectTarget(t *testing.T, ctx context.Context, sourceRoot string, target devToolsTarget) {
	t.Helper()
	l.binaryPath = filepath.Join(l.workDir, "agent")
	if err := buildGateBinary(ctx, sourceRoot, l.binaryPath); err != nil {
		t.Fatalf("build production agent binary from %s: %v", sourceRoot, err)
	}
	l.configDir = filepath.Join(l.workDir, "config")
	if err := os.Mkdir(l.configDir, 0o700); err != nil {
		t.Fatalf("create live config directory: %v", err)
	}
	if err := writeGateConfig(l.configDir, l.baseURL, l.fixture.Origin()); err != nil {
		t.Fatalf("write live browser config: %v", err)
	}
	var err error
	l.browserID, l.targetID, err = selectConversationalCustomerTarget(ctx, l.binaryPath, l.configDir, l.homeURL)
	if err != nil {
		t.Fatalf("select exact conversational fixture target: %v", err)
	}
	if target.ID != string(l.targetID) {
		t.Fatalf("selected target = %s, discovered target = %s", l.targetID, target.ID)
	}
}

func (l *conversationalCustomerLive) startObserver(t *testing.T, ctx context.Context) {
	t.Helper()
	observer, observerClose, err := openConversationalCustomerObserver(ctx, l.browserID, l.targetID, l.version)
	if err != nil {
		t.Fatalf("open independent browser event observer: %v", err)
	}
	l.observerClose = observerClose
	l.collector = newConversationalCustomerEventCollector()
	go l.collector.consume(observer)
	t.Cleanup(func() {
		if closeErr := observerClose(); closeErr != nil {
			t.Logf("observer cleanup: %v", closeErr)
		}
	})

	if _, err := waitForConversationalCustomerOracle(ctx, l.fixture.StateURL(), func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerHomePage
	}); err != nil {
		t.Fatalf("wait for initial independent fixture oracle: %v", err)
	}

	l.scenario = newConversationalCustomerScenario(l.homeURL, l.settingsURL)
	l.browserService = browserconversationWire.NewService()
	if _, err := l.browserService.ValidateScenario(l.scenario); err != nil {
		t.Fatalf("canonical scenario validation: %v", err)
	}
	l.initialOracle, err = readConversationalCustomerOracle(ctx, l.fixture.StateURL())
	if err != nil {
		t.Fatalf("read initial independent oracle: %v", err)
	}
}

func (l *conversationalCustomerLive) startSession(t *testing.T, ctx context.Context) {
	t.Helper()
	promptPath := filepath.Join(l.workDir, "system-prompt.txt")
	if err := os.WriteFile(promptPath, []byte(conversationalCustomerSystemPrompt()), 0o600); err != nil {
		t.Fatalf("write fixed live system prompt: %v", err)
	}
	l.recordDir = filepath.Join(l.workDir, "recording")
	sessionArgs := []string{
		"session",
		"--browser-tools=webmcp",
		"--browser-cdp-url", l.baseURL,
		"--browser-browser", l.browserID,
		"--browser-tab", string(l.targetID),
		"--browser-allowed-origin", l.fixture.Origin(),
		"--browser-cancel-on-interrupt", "always",
		"--provider", "openai",
		"--model", conversationalCustomerModel(),
		"--system-prompt", promptPath,
		"--record-dir", l.recordDir,
		"--wait-for-close",
		"--max-duration", "8m",
		"--audio-interrupt", l.audioPaths[4],
		"--audio-interrupt-on-tool", "webmcp_customer_pending",
	}
	for _, path := range l.audioPaths {
		sessionArgs = append(sessionArgs, "--audio-in-turn", path)
	}
	session, err := startGateCommand(ctx, l.binaryPath, l.configDir, sessionArgs...)
	if err != nil {
		t.Fatalf("start production conversational session: %v", err)
	}
	l.session = session
}

// waitTerminal waits for a responded event from the named page tool that also
// satisfies the optional extra predicate.
func (l *conversationalCustomerLive) waitTerminal(ctx context.Context, start int, toolName string, extra func(webmcp.BrowserEvent) bool) error {
	_, err := l.collector.wait(ctx, start, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.ToolName == toolName && event.Status != "" && (extra == nil || extra(event))
	})
	return err
}

func (l *conversationalCustomerLive) waitOracle(ctx context.Context, match func(conversationalCustomerOracle) bool) (conversationalCustomerOracle, error) {
	return waitForConversationalCustomerOracle(ctx, l.fixture.StateURL(), match)
}

// navigate performs a customer navigation and returns the event index before
// it plus the observed page or frame navigation event that followed it.
func (l *conversationalCustomerLive) navigate(t *testing.T, ctx context.Context, pageURL, navigateFailure, observeFailure string) (int, webmcp.BrowserEvent) {
	t.Helper()
	start := l.collector.len()
	if err := navigateConversationalCustomerTarget(ctx, l.browser.endpoint(), l.targetID, pageURL); err != nil {
		t.Fatalf("%s: %v", navigateFailure, err)
	}
	event, err := l.collector.wait(ctx, start, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventPageNavigated || event.Type == webmcp.EventFrameNavigated
	})
	if err != nil {
		t.Fatalf("%s: %v", observeFailure, err)
	}
	return start, event
}

func (l *conversationalCustomerLive) observeInitialActions(t *testing.T, ctx context.Context, observed *conversationalCustomerObserved) {
	t.Helper()
	var err error
	if err := l.waitTerminal(ctx, 0, "webmcp_customer_set_label", nil); err != nil {
		t.Fatalf("wait for initial label terminal event: %v", err)
	}
	observed.labelAfter, err = l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Label == conversationalCustomerLabel
	})
	if err != nil {
		t.Fatalf("wait for initial label oracle: %v", err)
	}
	if err := l.waitTerminal(ctx, 0, "webmcp_customer_set_theme", nil); err != nil {
		t.Fatalf("wait for second theme terminal event: %v", err)
	}
	observed.themeAfter, err = l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Theme == conversationalCustomerTheme
	})
	if err != nil {
		t.Fatalf("wait for second theme oracle: %v", err)
	}
}

func (l *conversationalCustomerLive) observeStaleRecovery(t *testing.T, ctx context.Context, observed *conversationalCustomerObserved) {
	t.Helper()
	start, navigation := l.navigate(t, ctx, l.settingsURL, "customer navigate to settings", "observe settings navigation")
	observed.settingsNavigation = navigation
	var err error
	observed.settingsBefore, err = l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerSettingsPage
	})
	if err != nil {
		t.Fatalf("wait for settings oracle: %v", err)
	}
	if err := l.waitTerminal(ctx, start, "webmcp_customer_set_priority", nil); err != nil {
		t.Fatalf("wait for fresh settings priority terminal event: %v", err)
	}
	observed.settingsAfter, err = l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerSettingsPage && oracle.Priority == conversationalCustomerPriority
	})
	if err != nil {
		t.Fatalf("wait for settings priority oracle: %v", err)
	}
}

func (l *conversationalCustomerLive) observeCorrection(t *testing.T, ctx context.Context, observed *conversationalCustomerObserved) {
	t.Helper()
	start, navigation := l.navigate(t, ctx, l.homeURL, "customer navigate back to home", "observe home correction navigation")
	observed.homeNavigation = navigation
	var err error
	observed.correctionBefore, err = l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerHomePage && oracle.Label == conversationalCustomerLabel && oracle.Theme == conversationalCustomerTheme
	})
	if err != nil {
		t.Fatalf("wait for correction baseline oracle: %v", err)
	}
	if err := l.waitTerminal(ctx, start, "webmcp_customer_set_label", func(event webmcp.BrowserEvent) bool {
		return event.Sequence > navigation.Sequence
	}); err != nil {
		t.Fatalf("wait for correction terminal event: %v", err)
	}
	observed.correctionAfter, err = l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return oracle.Ready && oracle.Page == conversationalCustomerHomePage && oracle.Label == conversationalCustomerCorrected
	})
	if err != nil {
		t.Fatalf("wait for correction oracle: %v", err)
	}
	observed.pending, err = l.collector.wait(ctx, start, func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolInvoked && event.ToolName == "webmcp_customer_pending" && event.InvocationID != ""
	})
	if err != nil {
		t.Fatalf("wait for synchronized pending invocation: %v", err)
	}
}

func (l *conversationalCustomerLive) cancelPendingInvocation(t *testing.T, ctx context.Context, observed *conversationalCustomerObserved) {
	t.Helper()
	if _, err := l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool { return oracle.Pending }); err != nil {
		t.Fatalf("wait for pending oracle: %v", err)
	}
	cancelResult, err := cancelConversationalCustomerInvocation(ctx, l.binaryPath, l.configDir, observed.pending.InvocationID)
	if err != nil {
		t.Fatalf("cancel pending invocation through separate browser process: %v", err)
	}
	if cancelResult.Status != "cancel_requested" {
		t.Fatalf("cancel pending invocation status = %q", cancelResult.Status)
	}
	observed.cancelResult = cancelResult
	if _, err := l.waitOracle(ctx, func(oracle conversationalCustomerOracle) bool {
		return !oracle.Pending && conversationalCustomerOracleHasInvocation(oracle, "canceled:webmcp_customer_pending")
	}); err != nil {
		t.Fatalf("wait for pending cancellation oracle: %v", err)
	}
}

func (l *conversationalCustomerLive) finishSession(t *testing.T, ctx, cleanupContext context.Context, observed *conversationalCustomerObserved) {
	t.Helper()
	sessionResult, waitErr := l.session.wait(cleanupContext)
	if waitErr != nil {
		t.Fatalf("wait for production conversational session: %v", waitErr)
	}
	if sessionResult.Err != nil && sessionResult.ExitCode != 0 {
		t.Fatalf("production conversational session exited with code %d", sessionResult.ExitCode)
	}
	if closeErr := l.observerClose(); closeErr != nil {
		t.Fatalf("detach independent browser observer: %v", closeErr)
	}
	var err error
	observed.postProbe, err = inspectConversationalCustomerTarget(ctx, l.browser.endpoint(), l.browserID, l.targetID)
	if err != nil {
		t.Fatalf("independent post-detach tab probe: %v", err)
	}
	observed.postOracle, err = readConversationalCustomerOracle(ctx, l.fixture.StateURL())
	if err != nil {
		t.Fatalf("read post-session oracle: %v", err)
	}
}

func (l *conversationalCustomerLive) evaluate(t *testing.T, observed conversationalCustomerObserved) (browserconversation.BrowserConversationResult, browserconversation.BrowserConversationMechanicalEvaluation, browserconversation.BrowserConversationValidatorVerdict, error) {
	t.Helper()
	result, err := buildConversationalCustomerResult(
		l.scenario,
		l.collector.snapshot(),
		filepath.Join(l.recordDir, "session-log.jsonl"),
		[]conversationalCustomerNavigationObservation{
			{StepID: "stale_recovery", Event: observed.settingsNavigation},
			{StepID: "correction", Event: observed.homeNavigation},
		},
		conversationalCustomerOracleObservations(l.initialOracle, observed),
		l.browserID,
		l.targetID,
		observed.postProbe,
		observed.pending,
		observed.cancelResult,
	)
	if err != nil {
		t.Fatalf("build joined live evidence: %v", err)
	}
	mechanical, err := l.browserService.Evaluate(l.scenario, result, nil)
	if err != nil {
		t.Fatalf("evaluate joined live evidence: %v", err)
	}
	result.Mechanical = mechanical
	validator, err := l.browserService.NewCommandValidator(browserconversation.BrowserConversationValidatorCommand{Command: l.validatorCommand, Env: sanitizedValidatorEnvironment(), Timeout: 90 * time.Second})
	if err != nil {
		t.Fatalf("construct validator command: %v", err)
	}
	verdict, validatorErr := validator.ValidateBrowserConversation(result)
	if validatorErr != nil {
		result.Validator = browserconversation.BrowserConversationValidatorVerdict{
			Version: browserconversation.BrowserConversationValidatorVersion,
			Status:  browserconversation.BrowserConversationValidatorNotRun,
			Summary: "validator command failed before a structured verdict was returned",
		}
	} else {
		result.Validator = verdict
	}
	return result, mechanical, verdict, validatorErr
}

func conversationalCustomerOracleObservations(initial conversationalCustomerOracle, observed conversationalCustomerObserved) []conversationalCustomerOracleObservation {
	return []conversationalCustomerOracleObservation{
		{StepID: "initial_action", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: initial},
		{StepID: "initial_action", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: observed.labelAfter},
		{StepID: "second_action", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: observed.labelAfter},
		{StepID: "second_action", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: observed.themeAfter},
		{StepID: "stale_recovery", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: observed.settingsBefore},
		{StepID: "stale_recovery", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: observed.settingsAfter},
		{StepID: "correction", Phase: browserconversation.BrowserConversationOracleBefore, Oracle: observed.correctionBefore},
		{StepID: "correction", Phase: browserconversation.BrowserConversationOracleAfter, Oracle: observed.correctionAfter},
		{StepID: "", Phase: browserconversation.BrowserConversationOraclePostSession, Oracle: observed.postOracle},
	}
}

func (l *conversationalCustomerLive) renderReport(t *testing.T, result browserconversation.BrowserConversationResult) (string, browserconversation.BrowserConversationReportMetadata) {
	t.Helper()
	metadata := browserconversation.BrowserConversationReportMetadata{
		Command:       fmt.Sprintf("agent session --browser-tools=webmcp --provider openai --model %s --record-dir <recording> --audio-in-turn <six finite files> --audio-interrupt <finite file> --audio-interrupt-on-tool webmcp_customer_pending", conversationalCustomerModel()),
		Configuration: fmt.Sprintf("browser backend=webmcp cdp=loopback allowed_origin=%s browser=%s target=%s", l.fixture.Origin(), l.browserID, l.targetID),
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
		PR269Status:      l.lane.State,
		LaneIBranch:      l.lane.HeadRefName,
		LaneIPullRequest: "https://github.com/portpowered/go-agent-harness/pull/" + conversationalCustomerLaneNumber,
	}
	report, reportErr := l.browserService.RenderReport(result, metadata)
	if reportErr != nil {
		t.Fatalf("render sanitized live report: %v", reportErr)
	}
	return report, metadata
}

func (l *conversationalCustomerLive) writeRequestedReport(t *testing.T, result browserconversation.BrowserConversationResult, metadata browserconversation.BrowserConversationReportMetadata) {
	t.Helper()
	reportPath := strings.TrimSpace(os.Getenv(conversationalCustomerReportPathEnv))
	if reportPath == "" {
		return
	}
	file, openErr := os.OpenFile(reportPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if openErr != nil {
		t.Fatalf("open requested report path: %v", openErr)
	}
	writeErr := l.browserService.WriteReport(file, result, metadata)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write requested report path: %v", errors.Join(writeErr, closeErr))
	}
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
	if status.State != conversationalCustomerLaneMerged && status.State != "OPEN" {
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
	if lane.State == conversationalCustomerLaneMerged {
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
	Phase  browserconversation.BrowserConversationOraclePhase
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
	defer closeAfterRead(response.Body)
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
	defer removeBestEffort(os.Remove, path)
	command := exec.CommandContext(ctx, "gh", "pr", "comment", conversationalCustomerLaneNumber, "--body-file", path)
	command.Stdin = nil
	if _, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("gh comment failed: %w", err)
	}
	return nil
}
