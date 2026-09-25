//go:build live

package chrome

// This is an explicitly opted-in, credentialed Gate I2 measurement. It joins
// the qualified local WebMCP fixture to one real OpenAI Realtime audio
// session, then validates the raw provider trace and two independent page
// oracles. It is intentionally a test rather than a production command: a
// live measurement must never become a normal CI side effect.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	gateI2OptIn       = "WEBMCP_GATE_I2"
	gateI2ArtifactEnv = "WEBMCP_GATE_I2_ARTIFACT_DIR"
	gateI2KeyFileEnv  = "OPENAI_API_KEY_FILE"
	gateI2Model       = "gpt-realtime-2.1-mini"
	gateI2Timeout     = 90 * time.Second

	gateI2RequestPrefix = "Please use the available browser capability to set the fixture message to "
)

var errGateI2MissingAPIKey = errors.New("OpenAI API key is not configured")

// TestPinnedChromeOpenAIRealtimeWebMCPGateI2 is the release-facing spoken
// JSON-in-string measurement. The only user request is synthesized into the
// audio input; browser IDs, tool refs, and encoded page arguments are not
// passed through a prompt, flag, or fixture-side shortcut.
func TestPinnedChromeOpenAIRealtimeWebMCPGateI2(t *testing.T) {
	// Keep this guard first. Normal tests must not inspect credentials, read the
	// Chrome lock, make network requests, create a fixture, or start Chrome.
	if os.Getenv(gateI2OptIn) != "1" {
		t.Skipf("set %s=1 to run the credentialed OpenAI Realtime Gate I2 measurement", gateI2OptIn)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Skipf("Gate I2 uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}
	apiKey, keySource := requireLiveOpenAIKey(t, "OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed Gate I2 measurement")

	run := prepareGateI2Run(t)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	run.startChrome(t, ctx)
	binaryPath := filepath.Join(run.artifactRoot, "agent")
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	runErr := run.runSession(t, ctx, binaryPath, apiKey)

	outcome := run.observe(ctx, runErr)
	evidence := run.evidence(keySource, outcome)
	evidencePath := filepath.Join(run.artifactRoot, "acceptance-report.json")
	if err := writeGateI2Evidence(evidencePath, evidence); err != nil {
		t.Fatalf("write Gate I2 evidence: %v", err)
	}
	t.Logf("Gate I2 evidence: %s", evidencePath)
	if outcome.validationErr != nil {
		t.Fatalf("Gate I2 measurement failed: %v; evidence=%s", outcome.validationErr, evidencePath)
	}

	if closeErr := run.browser.close(); closeErr != nil {
		t.Logf("Chrome process cleanup returned: %v", closeErr)
	}
	t.Logf("WEBMCP_GATE_I2_PASS chrome=%s revision=%s browser=%s target=%s list_tools_ref=%s invoke_ref=%s input_json=%q transcript=%q output_audio_bytes=%d capture=%s record_dir=%s", lockedChromeVersion, lockedChromeRevision, run.browserID, run.targetID, outcome.validation.ListToolRef, outcome.validation.InvokeToolRef, outcome.validation.RawInputJSON, outcome.observation.SpokenTranscript, outcome.observation.AudioBytesAfterInvoke, run.capturePath, run.recordDir)
}

// gateI2Run holds one Gate I2 measurement's inputs, pinned browser, and
// artifact paths.
type gateI2Run struct {
	message          string
	request          string
	artifactRoot     string
	configDir        string
	inputPath        string
	systemPromptPath string
	capturePath      string
	recordDir        string
	audioPath        string

	pinned       pinnedChrome
	fixture      *fixtureServer
	fixtureURL   string
	browser      *liveBrowserOwner
	baseURL      string
	version      devToolsVersion
	rawTargetID  string
	browserID    string
	targetID     string
	cdpURL       string
	beforeOracle fixtureOracle
}

// gateI2Outcome is everything observed after the session ends.
type gateI2Outcome struct {
	observation      gateI2Observation
	validation       gateI2Validation
	validationErr    error
	afterOracle      fixtureOracle
	postVersion      devToolsVersion
	postVersionErr   error
	postTarget       devToolsTarget
	postTargetErr    error
	independentState inspectedPageState
	audioFileBytes   int64
}

func prepareGateI2Run(t *testing.T) *gateI2Run {
	t.Helper()
	message, err := randomGateI2Message()
	if err != nil {
		t.Fatalf("generate randomized fixture message: %v", err)
	}
	run := &gateI2Run{message: message, request: gateI2Request(message), artifactRoot: gateI2ArtifactRoot(t)}
	run.configDir = filepath.Join(run.artifactRoot, "config")
	if err := os.Mkdir(run.configDir, 0o700); err != nil {
		t.Fatalf("create Gate I2 config directory: %v", err)
	}
	run.inputPath = gateI2SpokenInput(t.Context(), t, run.artifactRoot, run.request)
	run.systemPromptPath = filepath.Join(run.artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(run.systemPromptPath, []byte(gateI2SystemPrompt), 0o600); err != nil {
		t.Fatalf("write Gate I2 system prompt: %v", err)
	}
	run.capturePath = filepath.Join(run.artifactRoot, "provider.json")
	run.recordDir = filepath.Join(run.artifactRoot, "recording")
	run.audioPath = filepath.Join(run.artifactRoot, "assistant.wav")
	return run
}

// startChrome launches the qualified Chrome on the local fixture, records the
// initial independent oracle, and writes the exact-target browser config.
func (r *gateI2Run) startChrome(t *testing.T, ctx context.Context) {
	t.Helper()
	workDir := filepath.Join(r.artifactRoot, "chrome")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create Gate I2 Chrome work directory: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}
	r.pinned = pinned
	r.fixture = newFixtureServer()
	t.Cleanup(r.fixture.Close)
	r.fixtureURL = r.fixture.URL()
	assertFixtureHeaders(t, ctx, r.fixtureURL)

	browser, err := launchPinnedChrome(ctx, pinned, r.fixtureURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	r.browser = ownLiveBrowser(t, browser, "Gate I2 Chrome cleanup")
	r.baseURL = browserHTTPURL(browser.endpoint())
	if r.version, err = waitForDevToolsVersion(ctx, r.baseURL, lockedChromeVersion); err != nil {
		t.Fatalf("read qualified Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(ctx, r.baseURL, r.fixtureURL)
	if err != nil {
		t.Fatalf("discover exact fixture target: %v", err)
	}
	r.rawTargetID = rawTarget.ID
	if r.browserID, r.targetID, err = gateI2PublicIDs(r.version.WebSocketDebuggerURL, rawTarget.ID); err != nil {
		t.Fatalf("derive opaque browser and target IDs: %v", err)
	}
	r.beforeOracle, err = waitForFixtureOracle(ctx, r.fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == fixtureOracleInitial && oracle.VisibleText == fixtureOracleInitial && !oracle.Pending
	})
	if err != nil {
		t.Fatalf("read initial independent page oracle: %v", err)
	}
	r.cdpURL = strings.TrimRight(r.baseURL, "/") + "/json/version"
	if err := writeGateI2Config(r.configDir, r.cdpURL, r.fixture.server.URL, r.browserID, r.targetID); err != nil {
		t.Fatalf("write Gate I2 browser config: %v", err)
	}
}

func (r *gateI2Run) sessionArgs() []string {
	return []string{
		"session",
		"--provider", liveProviderOpenAI,
		"--model", gateI2Model,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", r.cdpURL,
		"--browser-browser", r.browserID,
		"--browser-tab", r.targetID,
		"--browser-origin", r.fixture.server.URL,
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "always",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", r.capturePath,
		"--record-dir", r.recordDir,
		"--audio-in", r.inputPath,
		"--audio-out", r.audioPath,
		"--system-prompt", r.systemPromptPath,
		"--max-duration", gateI2Timeout.String(),
	}
}

// runSession runs the production CLI. A session error is returned rather than
// fatal because capture validation remains authoritative.
func (r *gateI2Run) runSession(t *testing.T, ctx context.Context, binaryPath, apiKey string) error {
	t.Helper()
	sessionResult, err := runLiveAgentSession(ctx, gateI2Timeout+25*time.Second, binaryPath, r.configDir, apiKey, r.sessionArgs())
	var startErr liveSessionStartError
	if errors.As(err, &startErr) {
		t.Fatalf("start production agent CLI: %v", startErr.cause)
	}
	runErr := err
	if runErr == nil && (sessionResult.Err != nil || sessionResult.ExitCode != 0) {
		runErr = fmt.Errorf("agent session exit=%d err=%v", sessionResult.ExitCode, sessionResult.Err)
	}
	if runErr != nil {
		t.Logf("Gate I2 session returned an error (capture validation remains authoritative): %v", runErr)
	}
	return runErr
}

// observe reads the provider capture and both independent page oracles, then
// validates them together. The first failure becomes the validation error.
func (r *gateI2Run) observe(ctx context.Context, runErr error) gateI2Outcome {
	var outcome gateI2Outcome
	capture, inspectErr := gwtesting.LoadSessionCapture(r.capturePath)
	if inspectErr == nil {
		outcome.observation, inspectErr = inspectGateI2Capture(capture)
	}
	outcome.afterOracle = readGateI2Oracle(ctx, r.fixture.StateURL())
	outcome.postVersion, outcome.postVersionErr = readDevToolsVersion(ctx, r.baseURL)
	outcome.postTarget, outcome.postTargetErr = readGateI2FixtureTarget(ctx, r.baseURL, r.rawTargetID, r.fixtureURL)
	var independentErr error
	if outcome.postVersionErr == nil && outcome.postTargetErr == nil {
		outcome.independentState, independentErr = inspectExternalTarget(ctx, r.browser.browser.endpoint(), r.rawTargetID)
	}
	outcome.audioFileBytes = fileSize(r.audioPath)

	outcome.validationErr = inspectErr
	if outcome.validationErr == nil {
		outcome.validation, outcome.validationErr = validateGateI2Observation(outcome.observation, r.fixtureURL, r.browserID, r.targetID, r.message, outcome.audioFileBytes)
	}
	if outcome.validationErr == nil && runErr != nil {
		outcome.validationErr = fmt.Errorf("live session returned an error: %w", runErr)
	}
	if outcome.validationErr == nil {
		outcome.validationErr = r.validatePostSession(outcome, independentErr)
	}
	return outcome
}

func (r *gateI2Run) validatePostSession(outcome gateI2Outcome, independentErr error) error {
	after := outcome.afterOracle
	if after.Value != "completed:"+r.message || after.VisibleText != "completed:"+r.message || after.Pending || !hasFixtureInvocation(after, completeToolName+":"+r.message) {
		return fmt.Errorf("after oracle=%+v, want completed fixture mutation", after)
	}
	if outcome.postVersionErr != nil || outcome.postTargetErr != nil {
		return fmt.Errorf("Chrome post-session liveness failed: version=%w target=%w", outcome.postVersionErr, outcome.postTargetErr)
	}
	if independentErr != nil {
		return fmt.Errorf("independent DOM oracle failed: %w", independentErr)
	}
	if !gateI2StateMatchesOracle(outcome.independentState, after) {
		return fmt.Errorf("independent DOM state=%+v disagrees with HTTP oracle=%+v", outcome.independentState, after)
	}
	return nil
}

func (r *gateI2Run) evidence(keySource string, outcome gateI2Outcome) gateI2Evidence {
	observation := outcome.observation
	evidence := gateI2Evidence{
		Schema:                 "webmcp.gate-i2.evidence.v1",
		ObservedAtUTC:          time.Now().UTC().Format(time.RFC3339Nano),
		Pins:                   gateI2PinsFromLock(r.pinned.Lock, r.version.Browser),
		Provider:               observation.Provider,
		Model:                  observation.Model,
		APIKeySource:           keySource,
		SpokenRequest:          r.request,
		ExpectedMessage:        r.message,
		RequestInput:           "audio-only",
		AdvertisedTools:        append([]string(nil), observation.AdvertisedTools...),
		SessionUpdateCount:     observation.SessionUpdateCount,
		Calls:                  gateI2EvidenceCalls(observation.Calls),
		ToolOutputs:            gateI2EvidenceOutputs(observation.Outputs),
		SpokenTranscript:       observation.SpokenTranscript,
		TerminalStatus:         observation.TerminalStatus,
		OutputAudioBytes:       observation.AudioBytesAfterInvoke,
		AudioFileBytes:         outcome.audioFileBytes,
		BeforeOracle:           r.beforeOracle,
		AfterOracle:            outcome.afterOracle,
		IndependentDOM:         outcome.independentState,
		ChromeAliveAfterRun:    outcome.postVersionErr == nil,
		TargetPresentAfterRun:  outcome.postTargetErr == nil,
		BrowserVersionAfterRun: outcome.postVersion.Browser,
		TargetURLAfterRun:      outcome.postTarget.URL,
		CapturePath:            r.capturePath,
		RecordDir:              r.recordDir,
		AudioPath:              r.audioPath,
		ValidationError:        gateI2ErrorString(outcome.validationErr),
		Pass:                   outcome.validationErr == nil,
	}
	if outcome.validationErr == nil {
		evidence.ListToolRef = outcome.validation.ListToolRef
		evidence.InvokeToolRef = outcome.validation.InvokeToolRef
		evidence.RawInputJSON = outcome.validation.RawInputJSON
		evidence.Reason = outcome.validation.Reason
		evidence.GroundedFinalState = true
	}
	return evidence
}

var gateI2SystemPrompt = strings.Join([]string{
	"You are measuring a real WebMCP page through the browser capability. The user's spoken request is authoritative. Follow this exact protocol:",
	"- First call webmcp_list_tabs and find the one eligible page exposed by the browser.",
	"- Call webmcp_select_tab with the browser_id and target_id returned by webmcp_list_tabs.",
	"- Call webmcp_list_tools and find webmcp_lane_d_complete.",
	"- Call webmcp_invoke using the exact tool_ref returned by webmcp_list_tools. The input_json field must be one syntactically valid JSON object encoded as a JSON string, not prose or a flattened argument. Put the spoken message exactly in that JSON object. The reason field must be a concise user-facing explanation of the requested action.",
	"- Do not invent or rewrite a tool_ref, silently coerce malformed JSON, retry an invocation, or claim that the page changed before the terminal tool result.",
	"- After the terminal tool result, speak one concise confirmation grounded in its returned message and the final page state. Do not put tool refs or encoded arguments in the spoken request or final confirmation.",
}, "\n")

type gateI2Pins struct {
	Channel             string   `json:"channel"`
	Platform            string   `json:"platform"`
	Version             string   `json:"version"`
	Revision            string   `json:"revision"`
	ManifestURL         string   `json:"manifest_url"`
	ManifestRetrievedAt string   `json:"manifest_retrieved_at"`
	DownloadURL         string   `json:"download_url"`
	ArchiveSHA256       string   `json:"archive_sha256"`
	ExecutableVersion   string   `json:"executable_version"`
	LaunchFlags         []string `json:"launch_flags"`
}

type gateI2Evidence struct {
	Schema                 string                 `json:"schema"`
	ObservedAtUTC          string                 `json:"observed_at_utc"`
	Pins                   gateI2Pins             `json:"pins"`
	Provider               string                 `json:"provider"`
	Model                  string                 `json:"model"`
	APIKeySource           string                 `json:"api_key_source"`
	SpokenRequest          string                 `json:"spoken_request"`
	ExpectedMessage        string                 `json:"expected_message"`
	RequestInput           string                 `json:"request_input"`
	AdvertisedTools        []string               `json:"advertised_tools"`
	SessionUpdateCount     int                    `json:"session_update_count"`
	Calls                  []gateI2EvidenceCall   `json:"calls"`
	ToolOutputs            []gateI2EvidenceOutput `json:"tool_outputs"`
	ListToolRef            string                 `json:"list_tools_ref,omitempty"`
	InvokeToolRef          string                 `json:"invoke_tool_ref,omitempty"`
	RawInputJSON           string                 `json:"raw_input_json,omitempty"`
	Reason                 string                 `json:"reason,omitempty"`
	SpokenTranscript       string                 `json:"spoken_transcript"`
	GroundedFinalState     bool                   `json:"grounded_final_state"`
	TerminalStatus         string                 `json:"terminal_status"`
	OutputAudioBytes       int                    `json:"output_audio_bytes"`
	AudioFileBytes         int64                  `json:"audio_file_bytes"`
	BeforeOracle           fixtureOracle          `json:"page_state_before"`
	AfterOracle            fixtureOracle          `json:"page_state_after"`
	IndependentDOM         inspectedPageState     `json:"independent_dom_oracle"`
	ChromeAliveAfterRun    bool                   `json:"chrome_alive_after_run"`
	TargetPresentAfterRun  bool                   `json:"target_present_after_run"`
	BrowserVersionAfterRun string                 `json:"browser_version_after_run,omitempty"`
	TargetURLAfterRun      string                 `json:"target_url_after_run,omitempty"`
	CapturePath            string                 `json:"provider_capture_path"`
	RecordDir              string                 `json:"record_dir"`
	AudioPath              string                 `json:"assistant_audio_path"`
	ValidationError        string                 `json:"validation_error,omitempty"`
	Pass                   bool                   `json:"pass"`
}

type gateI2EvidenceCall struct {
	Index          int    `json:"index"`
	ArgumentsIndex int    `json:"arguments_index"`
	Name           string `json:"name"`
	CallID         string `json:"call_id"`
	Arguments      string `json:"arguments"`
}

type gateI2EvidenceOutput struct {
	Index  int    `json:"index"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

// gateI2Expectation is the independently known ground truth a Gate I2
// capture is validated against.
type gateI2Expectation struct {
	fixtureURL     string
	browserID      string
	targetID       string
	message        string
	audioFileBytes int64
}

func validateGateI2Observation(observation gateI2Observation, fixtureURL, expectedBrowserID, expectedTargetID, expectedMessage string, audioFileBytes int64) (gateI2Validation, error) {
	want := gateI2Expectation{fixtureURL: fixtureURL, browserID: expectedBrowserID, targetID: expectedTargetID, message: expectedMessage, audioFileBytes: audioFileBytes}
	if err := validateGateI2Session(observation); err != nil {
		return gateI2Validation{}, err
	}
	outputsByCall, err := validateGateI2CallSequence(observation)
	if err != nil {
		return gateI2Validation{}, err
	}
	if err := validateGateI2TabSelection(observation, outputsByCall, want); err != nil {
		return gateI2Validation{}, err
	}
	listToolRef, err := gateI2CatalogRef(outputsByCall[observation.Calls[2].CallID])
	if err != nil {
		return gateI2Validation{}, err
	}
	validation, err := validateGateI2InvokeArguments(observation.Calls[3].Arguments, listToolRef, want.message)
	if err != nil {
		return gateI2Validation{}, err
	}
	invokeOutput := outputsByCall[observation.Calls[3].CallID]
	if err := validateGateI2InvokeResult(invokeOutput, listToolRef, want.message); err != nil {
		return gateI2Validation{}, err
	}
	if err := validateGateI2SpokenConfirmation(observation, invokeOutput.Index, want); err != nil {
		return gateI2Validation{}, err
	}
	return validation, nil
}

func validateGateI2Session(observation gateI2Observation) error {
	if observation.Provider != liveProviderOpenAI || observation.Model != gateI2Model {
		return fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, gateI2Model)
	}
	if observation.ProviderErrors != 0 {
		return fmt.Errorf("provider emitted %d error event(s)", observation.ProviderErrors)
	}
	if observation.SessionUpdateCount != 1 || observation.SessionUpdateIndex < 0 || observation.FirstInputIndex < 0 || observation.SessionUpdateIndex >= observation.FirstInputIndex {
		return fmt.Errorf("session.update/input ordering count=%d update=%d first_audio=%d, want exactly one update before spoken audio", observation.SessionUpdateCount, observation.SessionUpdateIndex, observation.FirstInputIndex)
	}
	if !strings.Contains(observation.Instructions, "input_json") || !strings.Contains(observation.Instructions, "webmcp_list_tools") {
		return errors.New("session instructions omit the Gate I2 discovery and JSON-in-string contract")
	}
	if !slices.Equal(observation.AdvertisedTools, webmcp.StableToolNames()) {
		return fmt.Errorf("advertised tools=%v, want exactly the stable broker surface", observation.AdvertisedTools)
	}
	if len(observation.Calls) != 4 || len(observation.Outputs) != 4 {
		return fmt.Errorf("provider trace has %d calls and %d textual outputs, want one list_tabs/select_tab/list_tools/invoke sequence", len(observation.Calls), len(observation.Outputs))
	}
	return nil
}

// validateGateI2CallSequence checks the exact four-call broker sequence and
// returns each call's single textual output keyed by call_id.
func validateGateI2CallSequence(observation gateI2Observation) (map[string]gateI2Output, error) {
	wantedNames := []string{webmcp.ListTabsToolName, webmcp.SelectTabToolName, webmcp.ListToolsToolName, webmcp.InvokeToolName}
	outputsByCall := make(map[string]gateI2Output, len(observation.Outputs))
	for _, output := range observation.Outputs {
		if output.CallID == "" {
			return nil, fmt.Errorf("function_call_output at index %d has an empty call_id", output.Index)
		}
		if _, exists := outputsByCall[output.CallID]; exists {
			return nil, fmt.Errorf("function_call_output call_id=%q occurred more than once", output.CallID)
		}
		outputsByCall[output.CallID] = output
	}
	for index, call := range observation.Calls {
		if call.Name != wantedNames[index] {
			return nil, fmt.Errorf("call %d name=%q, want %q", index, call.Name, wantedNames[index])
		}
		if call.CallID == "" || call.ArgumentsIndex <= call.Index {
			return nil, fmt.Errorf("call %d correlation/order invalid: %+v", index, call)
		}
		output, ok := outputsByCall[call.CallID]
		if !ok || output.Index <= call.ArgumentsIndex {
			return nil, fmt.Errorf("call %s has no terminal textual output after arguments", call.CallID)
		}
		if _, err := webmcp.UnmarshalToolResult([]byte(output.Output)); err != nil {
			return nil, fmt.Errorf("call %s output is not a validated textual WebMCP envelope: %w; envelope=%s", call.CallID, err, output.Output)
		}
	}
	return outputsByCall, nil
}

func validateGateI2TabSelection(observation gateI2Observation, outputsByCall map[string]gateI2Output, want gateI2Expectation) error {
	var tabs struct {
		Targets []struct {
			BrowserID string `json:"browser_id"`
			TargetID  string `json:"target_id"`
			Type      string `json:"type"`
			URL       string `json:"url"`
			Origin    string `json:"origin"`
			Eligible  bool   `json:"eligible"`
		} `json:"targets"`
	}
	if err := decodeGateI2EnvelopeData(outputsByCall[observation.Calls[0].CallID].Output, &tabs); err != nil {
		return fmt.Errorf("decode webmcp_list_tabs envelope: %w", err)
	}
	matches := 0
	for _, target := range tabs.Targets {
		if target.BrowserID == want.browserID && target.TargetID == want.targetID && target.Type == pageTargetType && target.URL == want.fixtureURL && target.Origin == strings.TrimRight(want.fixtureURL, "/") && target.Eligible {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("webmcp_list_tabs returned %d exact eligible fixture target rows, want one", matches)
	}

	selectArgs, err := decodeGateI2Object(observation.Calls[1].Arguments)
	if err != nil {
		return fmt.Errorf("decode webmcp_select_tab arguments: %w", err)
	}
	if gateI2StringValue(selectArgs, "browser_id") != want.browserID || gateI2StringValue(selectArgs, "target_id") != want.targetID {
		return fmt.Errorf("webmcp_select_tab did not reuse list_tabs IDs: browser=%q target=%q", gateI2StringValue(selectArgs, "browser_id"), gateI2StringValue(selectArgs, "target_id"))
	}
	return nil
}

// gateI2CatalogRef returns the one valid ref webmcp_list_tools exposed for
// the fixture's completion tool.
func gateI2CatalogRef(listToolsOutput gateI2Output) (string, error) {
	var catalog struct {
		Tools []struct {
			Ref  string `json:"ref"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := decodeGateI2EnvelopeData(listToolsOutput.Output, &catalog); err != nil {
		return "", fmt.Errorf("decode webmcp_list_tools envelope: %w", err)
	}
	listToolRef := ""
	for _, tool := range catalog.Tools {
		if tool.Name == completeToolName {
			if listToolRef != "" {
				return "", fmt.Errorf("webmcp_list_tools returned duplicate %s descriptors", completeToolName)
			}
			listToolRef = tool.Ref
		}
	}
	if !webmcp.IsValidToolRef(webmcp.ToolRef(listToolRef)) {
		return "", fmt.Errorf("webmcp_list_tools returned invalid %s ref %q", completeToolName, listToolRef)
	}
	return listToolRef, nil
}

func validateGateI2InvokeArguments(rawArguments, listToolRef, expectedMessage string) (gateI2Validation, error) {
	invokeArgs, err := decodeGateI2Object(rawArguments)
	if err != nil {
		return gateI2Validation{}, fmt.Errorf("Gate I2 measurement: decode webmcp_invoke arguments: %w; raw arguments=%s; raw-schema acceleration fallback is required if the provider cannot reliably produce input_json", err, rawArguments)
	}
	invokeToolRef := gateI2StringValue(invokeArgs, "tool_ref")
	if invokeToolRef != listToolRef {
		return gateI2Validation{}, fmt.Errorf("Gate I2 measurement: webmcp_invoke tool_ref=%q did not reuse webmcp_list_tools ref=%q", invokeToolRef, listToolRef)
	}
	rawInputJSON := gateI2StringValue(invokeArgs, "input_json")
	if strings.TrimSpace(rawInputJSON) == "" || !json.Valid([]byte(rawInputJSON)) || !strings.HasPrefix(strings.TrimSpace(rawInputJSON), "{") {
		return gateI2Validation{}, fmt.Errorf("Gate I2 measurement: input_json is not one syntactically valid JSON object string: raw=%q; no coercion or retry was performed; use raw-schema acceleration fallback", rawInputJSON)
	}
	inputObject, err := decodeGateI2Object(rawInputJSON)
	if err != nil {
		return gateI2Validation{}, fmt.Errorf("Gate I2 measurement: input_json object validation failed: %w; raw=%q; no coercion or retry was performed; use raw-schema acceleration fallback", err, rawInputJSON)
	}
	if len(inputObject) != 1 || gateI2StringValue(inputObject, "message") != expectedMessage {
		return gateI2Validation{}, fmt.Errorf("Gate I2 measurement: input_json message=%q, want exactly %q with no extra fields; raw=%q", gateI2StringValue(inputObject, "message"), expectedMessage, rawInputJSON)
	}
	reason := gateI2StringValue(invokeArgs, "reason")
	if strings.TrimSpace(reason) == "" {
		return gateI2Validation{}, errors.New("Gate I2 measurement: webmcp_invoke reason is empty")
	}
	return gateI2Validation{ListToolRef: listToolRef, InvokeToolRef: invokeToolRef, RawInputJSON: rawInputJSON, Reason: reason}, nil
}

func validateGateI2InvokeResult(invokeOutput gateI2Output, listToolRef, expectedMessage string) error {
	var invokeEnvelope struct {
		InvocationID string          `json:"invocation_id"`
		ToolRef      string          `json:"tool_ref"`
		Status       string          `json:"status"`
		Output       json.RawMessage `json:"output"`
	}
	if err := decodeGateI2EnvelopeData(invokeOutput.Output, &invokeEnvelope); err != nil {
		return fmt.Errorf("decode webmcp_invoke envelope: %w", err)
	}
	if invokeEnvelope.InvocationID == "" || invokeEnvelope.ToolRef != listToolRef || invokeEnvelope.Status != string(webmcp.InvocationCompleted) {
		return fmt.Errorf("webmcp_invoke result=%+v, want completed correlated invocation", invokeEnvelope)
	}
	var pageOutput struct {
		Greeting string `json:"greeting"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(invokeEnvelope.Output, &pageOutput); err != nil || pageOutput.Greeting != "hello" || pageOutput.Message != expectedMessage {
		return fmt.Errorf("webmcp_invoke page output=%s, want greeting hello and message %q", invokeEnvelope.Output, expectedMessage)
	}
	return nil
}

func validateGateI2SpokenConfirmation(observation gateI2Observation, invokeOutputIndex int, want gateI2Expectation) error {
	responseCreatesAfterInvoke := 0
	for _, index := range observation.ResponseCreates {
		if index > invokeOutputIndex {
			responseCreatesAfterInvoke++
		}
	}
	if responseCreatesAfterInvoke == 0 {
		return errors.New("no response.create followed the delivered webmcp_invoke result")
	}
	if observation.SpokenTranscriptIndex <= invokeOutputIndex || !strings.Contains(strings.ToLower(observation.SpokenTranscript), strings.ToLower(want.message)) {
		return fmt.Errorf("spoken transcript=%q is absent, precedes the invoke result, or omits the final message", observation.SpokenTranscript)
	}
	if observation.TerminalIndex <= observation.SpokenTranscriptIndex || observation.TerminalStatus == "failed" || observation.TerminalStatus == liveStatusCancelled || observation.TerminalStatus == "incomplete" {
		return fmt.Errorf("terminal response status=%q index=%d, spoken index=%d", observation.TerminalStatus, observation.TerminalIndex, observation.SpokenTranscriptIndex)
	}
	if observation.AudioBytesAfterInvoke == 0 || want.audioFileBytes <= 44 {
		return fmt.Errorf("spoken output audio is missing: provider_delta_bytes=%d file_bytes=%d", observation.AudioBytesAfterInvoke, want.audioFileBytes)
	}
	return nil
}

func decodeGateI2EnvelopeData(output string, destination any) error {
	envelope, err := webmcp.UnmarshalToolResult([]byte(output))
	if err != nil {
		return err
	}
	if !envelope.OK {
		return fmt.Errorf("envelope failed with %s", envelope.Error.Code)
	}
	return json.Unmarshal(envelope.Data, destination)
}

func decodeGateI2Object(raw string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("JSON object required")
	}
	return object, nil
}

func gateI2StringValue(object map[string]json.RawMessage, key string) string {
	var value string
	if err := json.Unmarshal(object[key], &value); err != nil {
		return ""
	}
	return value
}

func gateI2PublicIDs(endpoint, rawTargetID string) (string, string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" || parsed.Port() == "" || parsed.Path == "" {
		return "", "", errors.New("invalid browser websocket endpoint")
	}
	identity := discovery.BrowserIdentity{
		Scheme: parsed.Scheme,
		Host:   parsed.Hostname(),
		Port:   parsed.Port(),
		Path:   parsed.EscapedPath(),
	}
	browserID := discovery.HashIDMapper{}.BrowserID(identity)
	targetID := discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: rawTargetID})
	return browserID, targetID, nil
}

func writeGateI2Config(configDir, cdpURL, origin, browserID, targetID string) error {
	var builder strings.Builder
	builder.WriteString("tools:\n  list:\n")
	for _, id := range config.DefaultToolIDs {
		fmt.Fprintf(&builder, "    - id: %q\n      enabled: false\n", id)
	}
	builder.WriteString("browser:\n")
	builder.WriteString("  tools:\n    enabled: true\n    backend: webmcp\n")
	fmt.Fprintf(&builder, "  connection:\n    cdp_url: %q\n    allow_remote_cdp: false\n", cdpURL)
	fmt.Fprintf(&builder, "  selection:\n    browser: %q\n    tab: %q\n    auto_select: off\n    activate_tab: false\n    persist: false\n", browserID, targetID)
	fmt.Fprintf(&builder, "  policy:\n    allowed_origins:\n      - %q\n    approval: never\n    cancel_on_interrupt: always\n", origin)
	builder.WriteString("  limits:\n    invocation_timeout: 30s\n    max_input_bytes: 262144\n    max_result_bytes: 262144\n    serialize_per_target: true\n")
	builder.WriteString("  recording:\n    enabled: true\n    include_arguments: true\n    include_results: true\n")
	return os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(builder.String()), 0o600)
}

// liveBrowserConfig is the browser section a live proof writes to
// config.yaml. An empty browserID auto-selects the single eligible tab
// instead of naming an exact target.
type liveBrowserConfig struct {
	cdpURL            string
	origin            string
	browserID         string
	targetID          string
	cancelOnInterrupt string
	invocationTimeout string
}

func writeLiveBrowserConfig(configDir string, browser liveBrowserConfig) error {
	selection := "    auto_select: single\n"
	if browser.browserID != "" {
		selection = fmt.Sprintf("    browser: %q\n    tab: %q\n    auto_select: off\n", browser.browserID, browser.targetID)
	}
	contents := fmt.Sprintf(`browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
    allow_remote_cdp: false
  selection:
%s    activate_tab: false
    persist: false
  policy:
    allowed_origins:
      - %q
    approval: never
    cancel_on_interrupt: %s
  limits:
    invocation_timeout: %s
  recording:
    enabled: true
    include_arguments: true
    include_results: true
`, browser.cdpURL, selection, browser.origin, browser.cancelOnInterrupt, browser.invocationTimeout)
	return os.WriteFile(filepath.Join(configDir, liveConfigFileName), []byte(contents), 0o600)
}

func loadGateI2APIKey() (string, string, error) {
	path := strings.TrimSpace(os.Getenv(gateI2KeyFileEnv))
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return "", gateI2KeyFileEnv, err
		}
		defer discardSecondaryError(file.Close)
		// This is the documented operator protocol:
		// Run tr with CR/LF deletion, as in: OPENAI_API_KEY="$(tr -d '\r\n' < "$OPENAI_API_KEY_FILE")"
		command := exec.Command("tr", "-d", "\\r\\n")
		command.Stdin = file
		var output bytes.Buffer
		command.Stdout = &output
		if err := command.Run(); err != nil {
			return "", gateI2KeyFileEnv, fmt.Errorf("run tr -d CR/LF: %w", err)
		}
		key := output.String()
		if key == "" {
			return "", gateI2KeyFileEnv, errGateI2MissingAPIKey
		}
		return key, gateI2KeyFileEnv + " (tr -d CR/LF)", nil
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return "", "", errGateI2MissingAPIKey
	}
	return key, "OPENAI_API_KEY", nil
}

func randomGateI2Message() (string, error) {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return "lane-i-" + hex.EncodeToString(token[:]), nil
}

func gateI2Request(message string) string {
	return gateI2RequestPrefix + fmt.Sprintf("%q", message) + ". First discover the available page tools, then perform that exact action, verify the resulting page state, and tell me the value you observed."
}

func gateI2SpokenInput(parent context.Context, t *testing.T, artifactRoot, request string) string {
	t.Helper()
	for _, command := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("Gate I2 spoken input requires %s", command)
		}
	}
	aiffPath := filepath.Join(artifactRoot, "request.aiff")
	wavPath := filepath.Join(artifactRoot, "request.wav")
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "say", "-o", aiffPath, request).CombinedOutput(); err != nil {
		t.Fatalf("generate Gate I2 spoken request: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.CommandContext(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", aiffPath, wavPath).CombinedOutput(); err != nil {
		t.Fatalf("convert Gate I2 spoken request to WAV: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return wavPath
}

func gateI2ArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(gateI2ArtifactEnv))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create Gate I2 artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "webmcp-gate-i2-")
	if err != nil {
		t.Fatalf("create Gate I2 artifact directory: %v", err)
	}
	return root
}

func readGateI2Oracle(ctx context.Context, endpoint string) fixtureOracle {
	oracle, err := readFixtureOracle(ctx, endpoint)
	if err != nil {
		return fixtureOracle{}
	}
	return oracle
}

func readGateI2FixtureTarget(ctx context.Context, baseURL, rawTargetID, fixtureURL string) (devToolsTarget, error) {
	targets, err := readDevToolsTargets(ctx, baseURL)
	if err != nil {
		return devToolsTarget{}, err
	}
	for _, target := range targets {
		if target.ID == rawTargetID && target.Type == pageTargetType && target.URL == fixtureURL {
			return target, nil
		}
	}
	return devToolsTarget{}, errors.New("fixture target is absent")
}

func gateI2StateMatchesOracle(state inspectedPageState, oracle fixtureOracle) bool {
	return state.Ready == oracle.Ready &&
		state.Value == oracle.Value &&
		state.VisibleText == oracle.VisibleText &&
		state.Pending == oracle.Pending &&
		slices.Equal(state.Invocations, oracle.Invocations)
}

func gateI2PinsFromLock(lock chromeForTestingLock, executableVersion string) gateI2Pins {
	return gateI2Pins{
		Channel:             lock.Channel,
		Platform:            lock.Platform,
		Version:             lock.Version,
		Revision:            lock.Revision,
		ManifestURL:         lock.ManifestURL,
		ManifestRetrievedAt: lock.ManifestRetrievedAt,
		DownloadURL:         lock.DownloadURL,
		ArchiveSHA256:       lock.ArchiveSHA256,
		ExecutableVersion:   executableVersion,
		LaunchFlags: []string{
			"--headless=new",
			"--disable-gpu",
			"--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
			"--remote-debugging-address=127.0.0.1",
			"--remote-debugging-port=0",
			"--user-data-dir=<temporary profile>",
		},
	}
}

func gateI2EvidenceCalls(calls []gateI2Call) []gateI2EvidenceCall {
	result := make([]gateI2EvidenceCall, 0, len(calls))
	for _, call := range calls {
		result = append(result, gateI2EvidenceCall(call))
	}
	return result
}

func gateI2EvidenceOutputs(outputs []gateI2Output) []gateI2EvidenceOutput {
	result := make([]gateI2EvidenceOutput, 0, len(outputs))
	for _, output := range outputs {
		result = append(result, gateI2EvidenceOutput(output))
	}
	return result
}

func writeGateI2Evidence(path string, evidence gateI2Evidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func gateI2ErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// requireLiveOpenAIKey loads the operator's OpenAI key, skipping the proof
// with skipMessage when none is configured.
func requireLiveOpenAIKey(t *testing.T, skipMessage string) (string, string) {
	t.Helper()
	apiKey, keySource, err := loadGateI2APIKey()
	if errors.Is(err, errGateI2MissingAPIKey) {
		t.Skip(skipMessage)
	}
	if err != nil {
		t.Fatalf("load OpenAI API key: %v", err)
	}
	return apiKey, keySource
}

// liveBrowserOwner closes a test-owned Chrome exactly once: explicitly on the
// success path, or from test cleanup when the proof stops early.
type liveBrowserOwner struct {
	browser *runningChrome
	closed  bool
}

func ownLiveBrowser(t *testing.T, browser *runningChrome, cleanupLabel string) *liveBrowserOwner {
	t.Helper()
	owner := &liveBrowserOwner{browser: browser}
	t.Cleanup(func() {
		if !owner.closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("%s: %v", cleanupLabel, closeErr)
			}
		}
	})
	return owner
}

func (o *liveBrowserOwner) close() error {
	o.closed = true
	return o.browser.Close()
}

// liveSessionStartError marks a production CLI that never started, as
// opposed to one that started and then failed to finish within its budget.
type liveSessionStartError struct {
	cause error
}

func (e liveSessionStartError) Error() string {
	return "start production agent CLI: " + e.cause.Error()
}

func (e liveSessionStartError) Unwrap() error {
	return e.cause
}

// runLiveAgentSession runs the production CLI with the OpenAI key and waits
// for it within budget. A start failure is a liveSessionStartError.
func runLiveAgentSession(parent context.Context, budget time.Duration, binaryPath, configDir, apiKey string, args []string) (gateCLIResult, error) {
	runContext, cancelRun := context.WithTimeout(parent, budget)
	defer cancelRun()
	process, err := startGateCommandWithEnvironment(runContext, binaryPath, configDir, []string{liveOpenAIKeyEnvironment + apiKey}, args...)
	if err != nil {
		return gateCLIResult{}, liveSessionStartError{cause: err}
	}
	return process.wait(runContext)
}
