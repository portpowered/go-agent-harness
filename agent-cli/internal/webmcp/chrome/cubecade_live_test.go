//go:build live

package chrome

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	cdpTarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	cubecadeLiveEnv                  = "WEBMCP_CUBECADE_LIVE"
	cubecadeScreenshotIntegrationEnv = "WEBMCP_CUBECADE_SCREENSHOT_INTEGRATION"
	cubecadeArtifactEnv              = "WEBMCP_CUBECADE_ARTIFACT_DIR"
	cubecadeURL                      = "https://cubecade.openai.chatgpt.site/"
	cubecadeOrigin                   = "https://cubecade.openai.chatgpt.site"
	cubecadeModel                    = "gpt-realtime-2.1-mini"
	cubecadeMaxDuration              = 30 * time.Second
	cubecadeScreenshotBudget         = 5 * time.Second
	cubecadeScreenshotTestTimeout    = 2 * time.Minute
	cubecadeLaunchDelay              = 4 * time.Second
	cubecadeRunGrace                 = 20 * time.Second
	cubecadeArtifactMode             = 0o700
	cubecadeEvidenceMode             = 0o600
	cubecadeScreenshotCallID         = "cubecade-screenshot-call"
	cubecadeQueueLogEntry            = "$ queue_cube_moves [6]"
)

// The fixture is served by the test-owned HTTP server so the screenshot proof
// never depends on credentials or a mutable remote deployment. The image is
// still produced by the real pinned Chrome page at capture time.
//
//go:embed testdata/cubecade_screenshot.html
var cubecadeScreenshotFixtureHTML []byte

// TestPinnedChromeCubecadeProductionSessionRecoversLateCatalog is the
// release-facing production-session proof. It is deliberately credentialed
// and opt-in: ordinary test runs never inspect credentials, download Chrome,
// contact the remote page, or call the provider.
func TestPinnedChromeCubecadeProductionSessionRecoversLateCatalog(t *testing.T) {
	if os.Getenv(cubecadeLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the credentialed Cubecade production-session proof", cubecadeLiveEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Skipf("Cubecade proof uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}
	apiKey, keySource := requireLiveOpenAIKey(t, "OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed Cubecade proof")

	artifactRoot := cubecadeArtifactRoot(t)
	configDir, systemPromptPath := prepareCubecadeProductionInputs(t, artifactRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pinned, binaryPath := prepareCubecadeProductionBinaries(t, ctx, artifactRoot)

	var timing cubecadeProductionTiming
	timing.launchStartedAt = time.Now()
	browser, err := launchPinnedChrome(ctx, pinned, cubecadeURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	owner := ownLiveBrowser(t, browser, "Cubecade Chrome cleanup")
	timing.devToolsReadyAt = time.Now()
	baseURL := browserHTTPURL(browser.endpoint())
	cdpURL := strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeCubecadeConfig(configDir, cdpURL); err != nil {
		t.Fatalf("write Cubecade browser config: %v", err)
	}
	if err := waitUntilCubecade(ctx, timing.launchStartedAt.Add(cubecadeLaunchDelay)); err != nil {
		t.Fatalf("wait for four-second Chrome launch offset: %v", err)
	}

	capturePath := filepath.Join(artifactRoot, "provider.json")
	sessionArgs := cubecadeProductionSessionArgs(cdpURL, capturePath, filepath.Join(artifactRoot, "recording"), systemPromptPath)
	timing.sessionStartedAt = time.Now()
	sessionResult, runErr := runLiveAgentSession(ctx, cubecadeMaxDuration+cubecadeRunGrace, binaryPath, configDir, apiKey, sessionArgs)
	var startErr liveSessionStartError
	if errors.As(runErr, &startErr) {
		t.Fatalf("start production agent CLI: %v", startErr.cause)
	}
	if runErr == nil && (sessionResult.Err != nil || sessionResult.ExitCode != 0) {
		runErr = fmt.Errorf("agent session exit=%d err=%w", sessionResult.ExitCode, sessionResult.Err)
	}

	outcome := observeCubecadeProduction(ctx, browser.endpoint(), baseURL, capturePath, runErr)
	evidence := cubecadeProductionEvidence(pinned, keySource, sessionArgs, timing, outcome)
	evidencePath := filepath.Join(artifactRoot, "evidence.json")
	if err := writeCubecadeEvidence(evidencePath, evidence); err != nil {
		t.Logf("write Cubecade evidence: %v", err)
	}

	if closeErr := owner.close(); closeErr != nil {
		t.Logf("Cubecade Chrome cleanup returned: %v", closeErr)
	}
	if outcome.validationErr != nil {
		t.Fatalf("Cubecade production late-catalog proof failed: %v; evidence=%s capture=%s", outcome.validationErr, evidencePath, capturePath)
	}
	validation := outcome.validation
	t.Logf("WEBMCP_CUBECADE_PASS chrome=%s revision=%s browser=%s target=%s retryable_list_errors=%d successful_list_calls=%d first_list_ms=%d recovered_list_ms=%d broker_invocations=%d queue_invocations=%d capture=%s evidence=%s", pinned.Lock.Version, pinned.Lock.Revision, validation.BrowserID, validation.TargetID, validation.RetryableListErrors, validation.SuccessfulListCalls, validation.FirstListCallMS, validation.RecoveredListResultMS, validation.BrokerInvocations, validation.QueueInvocations, capturePath, evidencePath)
}

type cubecadeProductionTiming struct {
	launchStartedAt  time.Time
	devToolsReadyAt  time.Time
	sessionStartedAt time.Time
}

type cubecadeProductionOutcome struct {
	observation   cubecadeCaptureObservation
	oracle        cubecadePageOracle
	validation    cubecadeValidation
	validationErr error
}

func prepareCubecadeProductionInputs(t *testing.T, artifactRoot string) (string, string) {
	t.Helper()
	configDir := filepath.Join(artifactRoot, "config")
	if err := os.MkdirAll(configDir, cubecadeArtifactMode); err != nil {
		t.Fatalf("create Cubecade config directory: %v", err)
	}
	systemPromptPath := filepath.Join(artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(cubecadeSystemPrompt), cubecadeEvidenceMode); err != nil {
		t.Fatalf("write Cubecade system prompt: %v", err)
	}
	return configDir, systemPromptPath
}

func prepareCubecadeProductionBinaries(t *testing.T, ctx context.Context, artifactRoot string) (pinnedChrome, string) {
	t.Helper()
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	workDir := filepath.Join(artifactRoot, "chrome")
	if err := os.MkdirAll(workDir, cubecadeArtifactMode); err != nil {
		t.Fatalf("create Cubecade Chrome directory: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}
	binaryPath := filepath.Join(artifactRoot, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	return pinned, binaryPath
}

func cubecadeProductionSessionArgs(cdpURL, capturePath, recordDir, systemPromptPath string) []string {
	return []string{
		"session",
		"--provider", liveProviderOpenAI,
		"--model", cubecadeModel,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-auto-select", "single",
		"--browser-origin", cubecadeOrigin,
		"--browser-approval", "never",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--system-prompt", systemPromptPath,
		"--prompt", "Scramble the cube and then solve it. Confirm in five words or fewer.",
		"--max-duration", cubecadeMaxDuration.String(),
	}
}

// observeCubecadeProduction reads Chrome, the independent DOM oracle, and the
// provider capture after the session, then validates them in order. The
// first failure becomes the validation error.
func observeCubecadeProduction(ctx context.Context, endpoint, baseURL, capturePath string, runErr error) cubecadeProductionOutcome {
	observationContext, cancelObservation := context.WithTimeout(ctx, 20*time.Second)
	defer cancelObservation()
	var outcome cubecadeProductionOutcome
	version, versionErr := readDevToolsVersion(observationContext, baseURL)
	rawTarget, targetErr := waitForCubecadePageTarget(observationContext, baseURL)
	var oracleErr error
	if targetErr == nil {
		outcome.oracle, oracleErr = inspectCubecadeTarget(observationContext, endpoint, rawTarget.ID)
	}
	capture, captureErr := gwtesting.LoadSessionCapture(capturePath)
	var observationErr error
	if captureErr == nil {
		outcome.observation, observationErr = inspectCubecadeCapture(capture)
	}
	var expectedBrowserID, expectedTargetID string
	if versionErr == nil && targetErr == nil {
		var err error
		expectedBrowserID, expectedTargetID, err = gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID)
		if err != nil {
			versionErr = fmt.Errorf("derive public browser/target IDs: %w", err)
		}
	}
	outcome.validationErr = firstLiveError(
		runErr,
		wrapLiveError("Chrome post-session version check", versionErr),
		wrapLiveError("Chrome post-session target check", targetErr),
		wrapLiveError("load provider capture", captureErr),
		wrapLiveError("inspect provider capture", observationErr),
	)
	if outcome.validationErr == nil {
		outcome.validation, outcome.validationErr = validateCubecadeCapture(outcome.observation, expectedBrowserID, expectedTargetID)
	}
	if outcome.validationErr == nil {
		outcome.validationErr = wrapLiveError("inspect independent Cubecade DOM oracle", oracleErr)
	}
	if outcome.validationErr == nil {
		outcome.validationErr = validateCubecadeOracle(outcome.oracle)
	}
	outcome.validation.QueueInvocations = strings.Count(outcome.oracle.Terminal, cubecadeQueueLogEntry)
	return outcome
}

func cubecadeProductionEvidence(pinned pinnedChrome, keySource string, sessionArgs []string, timing cubecadeProductionTiming, outcome cubecadeProductionOutcome) cubecadeEvidence {
	validation := outcome.validation
	evidence := cubecadeEvidence{
		Schema:                "webmcp.cubecade.production.evidence.v1",
		ObservedAtUTC:         time.Now().UTC().Format(time.RFC3339Nano),
		ChromeChannel:         pinned.Lock.Channel,
		ChromePlatform:        pinned.Lock.Platform,
		ChromeVersion:         pinned.Lock.Version,
		ChromeRevision:        pinned.Lock.Revision,
		ChromeFlags:           cubecadeEvidenceFlags(pinned, cubecadeURL),
		Provider:              outcome.observation.Provider,
		Model:                 outcome.observation.Model,
		APIKeySource:          keySource,
		Command:               cubecadeEvidenceCommand(sessionArgs),
		ChromeLaunchAtUTC:     timing.launchStartedAt.UTC().Format(time.RFC3339Nano),
		DevToolsReadyAtUTC:    timing.devToolsReadyAt.UTC().Format(time.RFC3339Nano),
		SessionStartedAtUTC:   timing.sessionStartedAt.UTC().Format(time.RFC3339Nano),
		SessionStartDelayMS:   timing.sessionStartedAt.Sub(timing.launchStartedAt).Milliseconds(),
		FirstListCallMS:       validation.FirstListCallMS,
		FirstListResultMS:     validation.FirstListResultMS,
		RecoveredListResultMS: validation.RecoveredListResultMS,
		RetryableListErrors:   validation.RetryableListErrors,
		SuccessfulListCalls:   validation.SuccessfulListCalls,
		BrowserID:             validation.BrowserID,
		TargetID:              validation.TargetID,
		PageTools:             validation.PageTools,
		BrokerInvocations:     validation.BrokerInvocations,
		QueueInvocations:      validation.QueueInvocations,
		Solved:                outcome.oracle.Solved,
		DOMStatus:             outcome.oracle.Status,
		DOMTerminal:           outcome.oracle.Terminal,
		CapturePath:           "<artifact>/provider.json",
		RecordDir:             "<artifact>/recording",
		Pass:                  outcome.validationErr == nil,
	}
	if outcome.validationErr != nil {
		evidence.ValidationError = outcome.validationErr.Error()
	}
	return evidence
}

// firstLiveError returns the first non-nil error, in order.
func firstLiveError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// wrapLiveError adds context to a non-nil error and passes nil through.
func wrapLiveError(context string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", context, err)
}

// TestPinnedChromeCubecadeSelectedPageScreenshot is the credential-free
// selected-page sight proof. It starts the same pinned headless Chrome used by
// the production gate, attaches the real Chrome adapter and broker to the
// exact Cubecade target, and invokes the model-facing show_page executor. The
// page oracle is independent of the returned screenshot: it supplies the
// rendered solved-status marker whose pixels must be present in the capture.
func TestPinnedChromeCubecadeSelectedPageScreenshot(t *testing.T) {
	if os.Getenv(cubecadeScreenshotIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the credential-free Cubecade screenshot proof", cubecadeScreenshotIntegrationEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Fatalf("the locked Chrome artifact is for %s, observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cubecadeScreenshotTestTimeout)
	defer cancel()
	sight := startCubecadeScreenshotChrome(t, ctx)
	wire := &wireTraceRecorder{}
	broker := selectCubecadeScreenshotTarget(t, ctx, sight, wire)
	defer discardSecondaryError(broker.Close)
	oracle := assertCubecadeSightOracle(t, ctx, sight)

	toolSet := webmcpTools.NewBrokerToolSet(broker)
	started := time.Now()
	captureContext, cancelCapture := context.WithTimeout(ctx, cubecadeScreenshotBudget)
	response, executeErr := toolSet.Executor().Execute(captureContext, messages.ToolCall{
		ID:        cubecadeScreenshotCallID,
		Name:      webmcp.ShowPageToolName,
		Arguments: `{}`,
	})
	cancelCapture()
	elapsed := time.Since(started)
	if executeErr != nil {
		t.Fatalf("execute show_page: %v", executeErr)
	}
	if elapsed >= cubecadeScreenshotBudget {
		t.Fatalf("show_page elapsed = %s, want less than %s", elapsed, cubecadeScreenshotBudget)
	}
	result := assertCubecadeShowPageEnvelope(t, response, sight)
	decoded := assertCubecadeShowPageImage(t, response, result)
	markerPixels := assertCubecadeScreenshotMarker(t, decoded, oracle)
	assertCubecadeScreenshotWireTrace(t, wire.snapshot(), sight)

	if err := sight.browser.close(); err != nil {
		t.Logf("close test-owned Chrome returned: %v", err)
	}
	t.Logf("WEBMCP_CUBECADE_SCREENSHOT_PASS chrome=%s revision=%s browser=%s target=%s mime=%s bytes=%d dimensions=%dx%d sha256=%s marker_pixels=%d elapsed=%s source=browser_page fixture=true credentials=false", sight.pinned.Lock.Version, sight.pinned.Lock.Revision, result.BrowserID, result.TargetID, result.MIMEType, result.ByteLength, result.Width, result.Height, result.SHA256, markerPixels, elapsed)
}

// cubecadeScreenshotSight is the pinned Chrome showing the local Cubecade
// fixture, with its exact page target.
type cubecadeScreenshotSight struct {
	pinned     pinnedChrome
	browser    *liveBrowserOwner
	fixtureURL string
	baseURL    string
	version    devToolsVersion
	rawTarget  devToolsTarget
	candidate  webmcp.BrowserCandidate
}

func startCubecadeScreenshotChrome(t *testing.T, ctx context.Context) *cubecadeScreenshotSight {
	t.Helper()
	pinned, err := acquirePinnedChrome(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("acquire locked Chrome for Testing: %v", err)
	}
	fixture := newCubecadeScreenshotFixture()
	t.Cleanup(fixture.Close)
	sight := &cubecadeScreenshotSight{pinned: pinned, fixtureURL: fixture.URL()}
	browser, err := launchPinnedChrome(ctx, pinned, sight.fixtureURL)
	if err != nil {
		t.Fatalf("launch locked Chrome for Testing: %v", err)
	}
	sight.browser = ownLiveBrowser(t, browser, "Cubecade screenshot Chrome cleanup")
	sight.baseURL = browserHTTPURL(browser.endpoint())
	if sight.version, err = waitForDevToolsVersion(ctx, sight.baseURL, lockedChromeVersion); err != nil {
		t.Fatalf("read pinned Chrome DevTools version: %v", err)
	}
	if sight.rawTarget, err = waitForFixturePageTarget(ctx, sight.baseURL, sight.fixtureURL); err != nil {
		t.Fatalf("discover exact Cubecade target: %v", err)
	}
	sight.candidate = webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID("chrome-cft-" + lockedChromeVersion),
		Source:       webmcp.DiscoverySourceExplicit,
		Product:      sight.version.Browser,
		Protocol:     sight.version.ProtocolVersion,
		HTTPURL:      sight.baseURL,
		BrowserWSURL: sight.version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
	}
	return sight
}

// selectCubecadeScreenshotTarget attaches the real Chrome adapter and broker
// to the exact fixture target, recording CDP wire traces.
func selectCubecadeScreenshotTarget(t *testing.T, ctx context.Context, sight *cubecadeScreenshotSight, wire *wireTraceRecorder) *webmcp.StatefulBroker {
	t.Helper()
	adapter := NewRuntime(
		WithEventBuffer(128),
		WithCommandTimeout(10*time.Second),
		WithWireTraceSink(wire),
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:            adapter,
		Discoverer:         pinnedCatalogDiscoverer{candidate: sight.candidate},
		CatalogWait:        10 * time.Second,
		LoadingCatalogWait: 10 * time.Second,
	})
	targetID := webmcp.TargetID(sight.rawTarget.ID)
	selected, err := broker.Select(ctx, webmcp.TargetSelector{BrowserID: sight.candidate.ID, TargetID: targetID})
	if err != nil {
		discardSecondaryError(broker.Close)
		t.Fatalf("select exact Cubecade target: %v", err)
	}
	if selected.Key.BrowserID != sight.candidate.ID || selected.Key.TargetID != targetID || !selected.Connected || !selected.Ready {
		discardSecondaryError(broker.Close)
		t.Fatalf("selected Cubecade page = %+v, want connected ready exact target", selected)
	}
	return broker
}

func assertCubecadeSightOracle(t *testing.T, ctx context.Context, sight *cubecadeScreenshotSight) cubecadeSightOracle {
	t.Helper()
	oracle, err := inspectCubecadeSightOracle(ctx, sight.browser.browser.endpoint(), sight.rawTarget.ID)
	if err != nil {
		t.Fatalf("inspect independent Cubecade sight oracle: %v", err)
	}
	if oracle.URL != sight.fixtureURL {
		t.Fatalf("sight oracle URL = %q, want exact Cubecade fixture URL %q", oracle.URL, sight.fixtureURL)
	}
	if !oracle.Solved || !strings.Contains(strings.ToUpper(oracle.StatusText), "SOLVED") {
		t.Fatalf("sight oracle page=%q title=%q ready_state=%q body=%q main=%q children=%d solved state=(%t,%q), want the rendered solved marker", oracle.URL, oracle.Title, oracle.ReadyState, oracle.BodyText, oracle.MainText, oracle.BodyChildren, oracle.Solved, oracle.StatusText)
	}
	return oracle
}

// assertCubecadeShowPageEnvelope checks the textual show_page envelope and
// returns its metadata.
func assertCubecadeShowPageEnvelope(t *testing.T, response messages.ToolCallResponse, sight *cubecadeScreenshotSight) webmcpTools.ShowPageResult {
	t.Helper()
	if response.ToolCallID != cubecadeScreenshotCallID || response.Name != webmcp.ShowPageToolName || response.Content == "" {
		t.Fatalf("show_page response correlation = (%q,%q) content=%q", response.ToolCallID, response.Name, response.Content)
	}
	if strings.Contains(response.Content, "base64") || strings.Contains(response.Content, "data:image/") {
		t.Fatalf("show_page metadata envelope contains encoded pixels: %s", response.Content)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode show_page envelope: %v; content=%s", err, response.Content)
	}
	if !envelope.OK {
		t.Fatalf("show_page returned failure: %+v", envelope.Error)
	}
	var result webmcpTools.ShowPageResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatalf("decode show_page metadata: %v; data=%s", err, envelope.Data)
	}
	if result.Version != webmcpTools.ShowPageResultVersion || result.Status != webmcpTools.ShowPageResultStatusSuccess || result.Source != "browser_page" || result.MIMEType != liveMIMETypePNG || result.BrowserID != string(sight.candidate.ID) || result.TargetID != sight.rawTarget.ID || result.TypedProjection != webmcpTools.ShowPageResultTypedProjectionInputImage {
		t.Fatalf("show_page metadata = %+v, want successful exact-target browser-page result", result)
	}
	return result
}

// assertCubecadeShowPageImage checks the one inline PNG projection against
// the metadata and returns the decoded image.
func assertCubecadeShowPageImage(t *testing.T, response messages.ToolCallResponse, result webmcpTools.ShowPageResult) image.Image {
	t.Helper()
	if len(response.ContentParts) != 2 {
		t.Fatalf("show_page content parts = %#v, want one text envelope and one image", response.ContentParts)
	}
	textPart, ok := response.ContentParts[0].(messages.TextPart)
	if !ok || textPart.Text != response.Content {
		t.Fatalf("show_page first content part = %#v, want the textual envelope", response.ContentParts[0])
	}
	imagePart, ok := response.ContentParts[1].(messages.ImagePart)
	if !ok || imagePart.URL != "" || imagePart.MediaType != result.MIMEType || len(imagePart.Bytes) == 0 {
		t.Fatalf("show_page image part = %#v, want one inline PNG projection", response.ContentParts[1])
	}
	if result.ByteLength != len(imagePart.Bytes) || result.ByteLength <= 4096 {
		t.Fatalf("show_page byte length = metadata:%d image:%d, want matching non-trivial capture", result.ByteLength, len(imagePart.Bytes))
	}
	digest := sha256.Sum256(imagePart.Bytes)
	if result.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("show_page SHA-256 = %q, want digest of projected bytes", result.SHA256)
	}
	decoded, format, err := image.Decode(bytes.NewReader(imagePart.Bytes))
	if err != nil {
		t.Fatalf("decode returned show_page image: %v", err)
	}
	if format != "png" || decoded.Bounds().Dx() <= 200 || decoded.Bounds().Dy() <= 200 || result.Width != decoded.Bounds().Dx() || result.Height != decoded.Bounds().Dy() {
		t.Fatalf("show_page image format/dimensions = %s/%dx%d metadata=%dx%d, want non-trivial PNG with matching dimensions", format, decoded.Bounds().Dx(), decoded.Bounds().Dy(), result.Width, result.Height)
	}
	return decoded
}

func assertCubecadeScreenshotWireTrace(t *testing.T, traces []webmcp.WebMCPWireTrace, sight *cubecadeScreenshotSight) {
	t.Helper()
	var screenshotTraces []webmcp.WebMCPWireTrace
	for _, trace := range traces {
		if trace.Method == webmcp.PageCaptureScreenshotMethod {
			screenshotTraces = append(screenshotTraces, trace)
		}
	}
	if len(screenshotTraces) != 1 {
		t.Fatalf("screenshot wire traces = %#v, want exactly one Page.captureScreenshot", screenshotTraces)
	}
	trace := screenshotTraces[0]
	if trace.BrowserID != sight.candidate.ID || trace.TargetID != webmcp.TargetID(sight.rawTarget.ID) || trace.TargetSessionID == "" || trace.Phase != webmcp.WebMCPWirePhaseBeforeDispatch || !trace.ListenerReady {
		t.Fatalf("screenshot wire trace = %+v, want exact listener-ready target", trace)
	}
}

type cubecadeScreenshotFixture struct {
	server *httptest.Server
}

func newCubecadeScreenshotFixture() *cubecadeScreenshotFixture {
	fixture := &cubecadeScreenshotFixture{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" || request.Method != http.MethodGet {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Origin-Agent-Cluster", "?1")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		writeFixtureBody(writer, cubecadeScreenshotFixtureHTML)
	}))
	return fixture
}

func (f *cubecadeScreenshotFixture) URL() string {
	if f == nil || f.server == nil {
		return ""
	}
	return f.server.URL + "/"
}

func (f *cubecadeScreenshotFixture) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

type cubecadeSightOracle struct {
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	ReadyState       string            `json:"ready_state"`
	BodyText         string            `json:"body_text"`
	MainText         string            `json:"main_text"`
	BodyChildren     int               `json:"body_children"`
	StatusText       string            `json:"status_text"`
	Solved           bool              `json:"solved"`
	MarkerBackground string            `json:"marker_background"`
	MarkerRect       cubecadeSightRect `json:"marker_rect"`
	ViewportWidth    float64           `json:"viewport_width"`
	ViewportHeight   float64           `json:"viewport_height"`
}

type cubecadeSightRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func inspectCubecadeSightOracle(ctx context.Context, endpoint, targetID string) (oracle cubecadeSightOracle, err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 15*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		cancelAllocator()
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.WaitReady("body")); err != nil {
		return oracle, fmt.Errorf("wait for Cubecade document: %w", err)
	}
	if err := chromedp.Run(targetContext, chromedp.Evaluate(cubecadeSightOracleExpression(), &oracle)); err != nil {
		return oracle, fmt.Errorf("read Cubecade sight marker: %w", err)
	}
	return oracle, nil
}

func cubecadeSightOracleExpression() string {
	return `(() => {
  const body = document.body;
  const solved = document.querySelector(".solved");
  const marker = solved ? solved.querySelector("i") : null;
  const rect = marker ? marker.getBoundingClientRect() : null;
  const style = marker ? getComputedStyle(marker) : null;
  return {
    url: location.href,
    title: document.title,
    ready_state: document.readyState,
    body_text: body ? String(body.innerText || body.textContent || "").slice(0, 500) : "",
    main_text: document.querySelector("main") ? String(document.querySelector("main").innerText || "").slice(0, 500) : "",
    body_children: body ? body.children.length : 0,
    status_text: solved ? String(solved.textContent || "") : "",
    solved: Boolean(solved && solved.classList.contains("yes")),
    marker_background: style ? String(style.backgroundColor || "") : "",
    marker_rect: rect ? {x: rect.x, y: rect.y, width: rect.width, height: rect.height} : {x: 0, y: 0, width: 0, height: 0},
    viewport_width: Number(window.innerWidth || 0),
    viewport_height: Number(window.innerHeight || 0)
  };
})()`
}

const cubecadeSystemPrompt = `You are the production WebMCP late-catalog verification operator. Use only the already selected page; never attach, reconnect, select another tab, or use a hidden shortcut.

Follow this exact protocol:
- Immediately call webmcp_list_tools before any other browser tool. If it returns browser_protocol_invalid with retryable=true, details.reason_code=page_tools_unverified, and details.reason=deadline_exceeded, wait briefly and call webmcp_list_tools again. Do not reattach or select again.
- After a successful list, call webmcp_get_context once and retain its browser_id and target_id. The identity must remain unchanged.
- Find get_cube_state and queue_cube_moves in the page catalog. Use webmcp_invoke with the exact opaque refs returned by webmcp_list_tools.
- Call queue_cube_moves exactly twice. First use the six-move scramble ["R","U","F","L'","D","B2"]. Then, after get_cube_state shows the queue is empty, use the inverse ["B2","D'","L","F'","U'","R'"] exactly once.
- Call get_cube_state as needed to wait for the queue to empty and then confirm solved=true. Do not invoke either page tool more than needed and never duplicate a queue request.
- After the independent page state is solved, reply with five words or fewer.`

type cubecadeValidation struct {
	BrowserID             string
	TargetID              string
	PageTools             []string
	FirstListCallMS       int64
	FirstListResultMS     int64
	RecoveredListResultMS int64
	RetryableListErrors   int
	SuccessfulListCalls   int
	BrokerInvocations     int
	QueueInvocations      int
}

type cubecadePageOracle struct {
	URL                   string `json:"url"`
	ModelContextAvailable bool   `json:"modelContextAvailable"`
	Solved                bool   `json:"solved"`
	Status                string `json:"status"`
	Terminal              string `json:"terminal"`
	State                 string `json:"state"`
}

type cubecadeEvidence struct {
	Schema                string   `json:"schema"`
	ObservedAtUTC         string   `json:"observed_at_utc"`
	ChromeChannel         string   `json:"chrome_channel"`
	ChromePlatform        string   `json:"chrome_platform"`
	ChromeVersion         string   `json:"chrome_version"`
	ChromeRevision        string   `json:"chrome_revision"`
	ChromeFlags           []string `json:"chrome_flags"`
	Provider              string   `json:"provider"`
	Model                 string   `json:"model"`
	APIKeySource          string   `json:"api_key_source"`
	Command               string   `json:"command"`
	ChromeLaunchAtUTC     string   `json:"chrome_launch_at_utc"`
	DevToolsReadyAtUTC    string   `json:"devtools_ready_at_utc"`
	SessionStartedAtUTC   string   `json:"session_started_at_utc"`
	SessionStartDelayMS   int64    `json:"session_start_delay_ms"`
	FirstListCallMS       int64    `json:"first_list_call_ms"`
	FirstListResultMS     int64    `json:"first_list_result_ms"`
	RecoveredListResultMS int64    `json:"recovered_list_result_ms"`
	RetryableListErrors   int      `json:"retryable_list_errors"`
	SuccessfulListCalls   int      `json:"successful_list_calls"`
	BrokerInvocations     int      `json:"broker_invocations"`
	BrowserID             string   `json:"browser_id"`
	TargetID              string   `json:"target_id"`
	PageTools             []string `json:"page_tools"`
	QueueInvocations      int      `json:"queue_invocations"`
	Solved                bool     `json:"solved"`
	DOMStatus             string   `json:"dom_status"`
	DOMTerminal           string   `json:"dom_terminal"`
	CapturePath           string   `json:"capture_path"`
	RecordDir             string   `json:"record_dir"`
	Pass                  bool     `json:"pass"`
	ValidationError       string   `json:"validation_error,omitempty"`
}

// cubecadeListProgress tracks the late-catalog recovery sequence across
// webmcp_list_tools results.
type cubecadeListProgress struct {
	firstListSeen     bool
	listSuccessSeen   bool
	retryableListSeen bool
	recoveredListSeen bool
}

func validateCubecadeCapture(observation cubecadeCaptureObservation, expectedBrowserID, expectedTargetID string) (cubecadeValidation, error) {
	validation := cubecadeValidation{}
	if observation.Provider != liveProviderOpenAI || observation.Model != cubecadeModel {
		return validation, fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, cubecadeModel)
	}
	if observation.SessionUpdates != 1 || !slices.Equal(observation.AdvertisedTools, webmcp.StableToolNames()) {
		return validation, fmt.Errorf("session update count/tools=(%d,%v), want one update with stable broker tools", observation.SessionUpdates, observation.AdvertisedTools)
	}
	if len(observation.Calls) == 0 || observation.Calls[0].Name != webmcp.ListToolsToolName {
		return validation, fmt.Errorf("first model browser call = %#v, want %s", observation.Calls, webmcp.ListToolsToolName)
	}
	outputs, err := indexCubecadeOutputs(observation.Outputs)
	if err != nil {
		return validation, err
	}
	var progress cubecadeListProgress
	for _, call := range observation.Calls {
		if call.CallID == "" || call.ArgumentsIndex <= call.Index {
			return validation, fmt.Errorf("call order/correlation invalid: %+v", call)
		}
		output, ok := outputs[call.CallID]
		if !ok || output.Index <= call.ArgumentsIndex {
			return validation, fmt.Errorf("call %q has no terminal result", call.CallID)
		}
		if err := validation.observeCall(call, output, &progress, expectedBrowserID, expectedTargetID); err != nil {
			return validation, err
		}
	}
	err = validation.complete(observation)
	return validation, err
}

func indexCubecadeOutputs(observed []cubecadeOutput) (map[string]cubecadeOutput, error) {
	outputs := make(map[string]cubecadeOutput, len(observed))
	for _, output := range observed {
		if output.CallID == "" {
			return nil, fmt.Errorf("tool result at record %d has no call_id", output.Index)
		}
		if _, exists := outputs[output.CallID]; exists {
			return nil, fmt.Errorf("tool result call_id=%q occurred more than once", output.CallID)
		}
		outputs[output.CallID] = output
	}
	return outputs, nil
}

func (v *cubecadeValidation) observeCall(call cubecadeCall, output cubecadeOutput, progress *cubecadeListProgress, expectedBrowserID, expectedTargetID string) error {
	switch call.Name {
	case webmcp.ListToolsToolName:
		return v.observeListCall(call, output, progress)
	case webmcp.GetContextToolName:
		return v.observeContextCall(output, expectedBrowserID, expectedTargetID)
	case webmcp.InvokeToolName:
		if !output.Envelope.OK {
			return fmt.Errorf("page invocation failed: %+v", output.Envelope.Error)
		}
	}
	return nil
}

func (v *cubecadeValidation) observeListCall(call cubecadeCall, output cubecadeOutput, progress *cubecadeListProgress) error {
	if !progress.firstListSeen {
		v.FirstListCallMS = call.TimestampMS
		progress.firstListSeen = true
	}
	if output.Envelope.OK {
		return v.observeListSuccess(output, progress)
	}
	failure := output.Envelope.Error
	if failure == nil {
		return errors.New("failed list_tools result has no error")
	}
	if failure.Details["reason_code"] != "page_tools_unverified" {
		return fmt.Errorf("list_tools failed with unexpected error: %+v", failure)
	}
	if progress.listSuccessSeen {
		return errors.New("catalog evidence became unverified after a successful list")
	}
	if failure.Code != string(webmcp.ErrorBrowserProtocol) || !failure.Retryable || failure.Details["reason"] != liveReasonDeadlineExceeded {
		return fmt.Errorf("catalog evidence failure is not the retryable deadline contract: %+v", failure)
	}
	v.RetryableListErrors++
	progress.retryableListSeen = true
	return nil
}

func (v *cubecadeValidation) observeListSuccess(output cubecadeOutput, progress *cubecadeListProgress) error {
	v.SuccessfulListCalls++
	if v.FirstListResultMS == 0 {
		v.FirstListResultMS = output.TimestampMS
	}
	if progress.retryableListSeen && !progress.recoveredListSeen {
		v.RecoveredListResultMS = output.TimestampMS
		progress.recoveredListSeen = true
	}
	var data struct {
		Generation uint64 `json:"generation"`
		Tools      []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(output.Envelope.Data, &data); err != nil {
		return fmt.Errorf("decode successful catalog: %w", err)
	}
	if data.Generation == 0 {
		return errors.New("successful catalog has no generation")
	}
	v.PageTools = v.PageTools[:0]
	for _, tool := range data.Tools {
		v.PageTools = append(v.PageTools, tool.Name)
	}
	progress.listSuccessSeen = true
	return nil
}

func (v *cubecadeValidation) observeContextCall(output cubecadeOutput, expectedBrowserID, expectedTargetID string) error {
	if !output.Envelope.OK {
		return fmt.Errorf("get_context failed: %+v", output.Envelope.Error)
	}
	var data struct {
		BrowserID string `json:"browser_id"`
		TargetID  string `json:"target_id"`
		Origin    string `json:"origin"`
		URL       string `json:"url"`
		Connected bool   `json:"connected"`
	}
	if err := json.Unmarshal(output.Envelope.Data, &data); err != nil {
		return fmt.Errorf("decode selected context: %w", err)
	}
	if data.BrowserID != expectedBrowserID || data.TargetID != expectedTargetID || data.Origin != cubecadeOrigin || !strings.HasPrefix(data.URL, cubecadeOrigin) || !data.Connected {
		return fmt.Errorf("selected context=(%+v), want exact connected Cubecade identity", data)
	}
	v.BrowserID, v.TargetID = data.BrowserID, data.TargetID
	return nil
}

// complete checks the catalog and identity facts that only the whole call
// sequence can establish, and counts broker invocations.
func (v *cubecadeValidation) complete(observation cubecadeCaptureObservation) error {
	if v.SuccessfulListCalls == 0 || len(v.PageTools) == 0 {
		return errors.New("no successful page catalog was observed")
	}
	if !slices.Contains(v.PageTools, cubecadeSharedBrowserStateTool) || !slices.Contains(v.PageTools, cubecadeSharedBrowserQueueTool) {
		return fmt.Errorf("page catalog=%v, want get_cube_state and queue_cube_moves", v.PageTools)
	}
	if v.RetryableListErrors > 0 && v.RecoveredListResultMS == 0 {
		return errors.New("retryable catalog failure was not followed by a successful list")
	}
	for _, call := range observation.Calls {
		if call.Name == webmcp.InvokeToolName {
			v.BrokerInvocations++
		}
	}
	if v.BrowserID == "" || v.TargetID == "" {
		return errors.New("capture did not include a connected exact selected context")
	}
	return nil
}

func validateCubecadeOracle(oracle cubecadePageOracle) error {
	if oracle.URL == "" || !strings.HasPrefix(oracle.URL, cubecadeOrigin) {
		return fmt.Errorf("DOM URL=%q, want Cubecade origin", oracle.URL)
	}
	if !oracle.ModelContextAvailable {
		return errors.New("DOM oracle could not observe the WebMCP modelContext producer")
	}
	if !oracle.Solved || !strings.Contains(strings.ToUpper(oracle.Status), "SOLVED") {
		return fmt.Errorf("DOM solved state=(%t,%q), want solved", oracle.Solved, oracle.Status)
	}
	if count := strings.Count(oracle.Terminal, cubecadeQueueLogEntry); count != 2 {
		return fmt.Errorf("DOM queue invocation log count=%d, want exactly two", count)
	}
	if !strings.Contains(oracle.Terminal, "$ get_cube_state") || !strings.Contains(oracle.State, "solved: true") {
		return fmt.Errorf("DOM terminal/state=%q/%q, want final get_cube_state solved=true", oracle.Terminal, oracle.State)
	}
	return nil
}

func inspectCubecadeTarget(ctx context.Context, endpoint, targetID string) (oracle cubecadePageOracle, err error) {
	rootContext, cancelRoot := context.WithTimeout(ctx, 20*time.Second)
	defer cancelRoot()
	allocatorContext, cancelAllocator := chromedp.NewRemoteAllocator(rootContext, endpoint, chromedp.NoModifyURL)
	targetContext, cancelTarget := chromedp.NewContext(allocatorContext, chromedp.WithTargetID(cdpTarget.ID(targetID)))
	defer func() {
		cleanupErr := detachExternalIntegrationTarget(targetContext, cancelTarget)
		cancelAllocator()
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()
	if err := chromedp.Run(targetContext, chromedp.WaitReady("main")); err != nil {
		return oracle, fmt.Errorf("wait for Cubecade DOM: %w", err)
	}
	if err := chromedp.Run(targetContext, chromedp.Evaluate(cubecadeOracleExpression(), &oracle)); err != nil {
		return oracle, fmt.Errorf("read Cubecade DOM oracle: %w", err)
	}
	return oracle, nil
}

func cubecadeOracleExpression() string {
	return `(() => {
  const context = document.modelContext || navigator.modelContext;
  const solved = document.querySelector(".solved");
  const terminal = document.querySelector(".term-lines");
  const state = document.querySelector("code");
  return {
    url: location.href,
    modelContextAvailable: Boolean(context && typeof context.registerTool === "function"),
    solved: Boolean(solved && solved.classList.contains("yes")),
    status: solved ? String(solved.textContent || "") : "",
    terminal: terminal ? String(terminal.textContent || "") : "",
    state: state ? String(state.textContent || "") : ""
  };
})()`
}

func waitForCubecadePageTarget(ctx context.Context, baseURL string) (devToolsTarget, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		targets, err := readDevToolsTargets(ctx, baseURL)
		if err == nil {
			for _, target := range targets {
				parsed, parseErr := url.Parse(target.URL)
				if target.Type == pageTargetType && parseErr == nil && parsed.Scheme == "https" && parsed.Host == "cubecade.openai.chatgpt.site" {
					return target, nil
				}
			}
			lastErr = errors.New("Cubecade page target is not present")
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return devToolsTarget{}, fmt.Errorf("wait for Cubecade page target: %w (last error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func writeCubecadeConfig(configDir, cdpURL string) error {
	return writeLiveBrowserConfig(configDir, liveBrowserConfig{cdpURL: cdpURL, origin: cubecadeOrigin, cancelOnInterrupt: "read-only", invocationTimeout: "20s"})
}

func cubecadeArtifactRoot(t *testing.T) string {
	t.Helper()
	if root := strings.TrimSpace(os.Getenv(cubecadeArtifactEnv)); root != "" {
		if err := os.MkdirAll(root, cubecadeArtifactMode); err != nil {
			t.Fatalf("create Cubecade artifact directory: %v", err)
		}
		return root
	}
	return t.TempDir()
}

func waitUntilCubecade(ctx context.Context, deadline time.Time) error {
	delay := time.Until(deadline)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cubecadeEvidenceFlags(pinned pinnedChrome, pageURL string) []string {
	flags := pinnedChromeLaunchFlags(filepath.Join(pinned.WorkDir, "profile"), pageURL, 0)
	for index, flag := range flags {
		if strings.HasPrefix(flag, "--user-data-dir=") {
			flags[index] = "--user-data-dir=<temp>/profile"
		}
	}
	return flags
}

func cubecadeEvidenceCommand(args []string) string {
	return strings.Join(append([]string{"<temp>/agent", "--config-dir", "<temp>/config"}, args...), " ")
}

func writeCubecadeEvidence(path string, evidence cubecadeEvidence) error {
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, cubecadeEvidenceMode)
}
