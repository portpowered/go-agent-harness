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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("Gate I2 uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	apiKey, keySource, err := loadGateI2APIKey()
	if errors.Is(err, errGateI2MissingAPIKey) {
		t.Skip("OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed Gate I2 measurement")
	}
	if err != nil {
		t.Fatalf("load OpenAI API key: %v", err)
	}

	message, err := randomGateI2Message()
	if err != nil {
		t.Fatalf("generate randomized fixture message: %v", err)
	}
	request := gateI2Request(message)

	artifactRoot := gateI2ArtifactRoot(t)
	configDir := filepath.Join(artifactRoot, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("create Gate I2 config directory: %v", err)
	}
	inputPath := gateI2SpokenInput(t, artifactRoot, request)
	systemPromptPath := filepath.Join(artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(gateI2SystemPrompt), 0o600); err != nil {
		t.Fatalf("write Gate I2 system prompt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()

	workDir := filepath.Join(artifactRoot, "chrome")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create Gate I2 Chrome work directory: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}

	fixture := newFixtureServer()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	assertFixtureHeaders(t, ctx, fixtureURL)

	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("Gate I2 Chrome cleanup: %v", closeErr)
			}
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read qualified Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(ctx, baseURL, fixtureURL)
	if err != nil {
		t.Fatalf("discover exact fixture target: %v", err)
	}
	browserID, targetID, err := gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID)
	if err != nil {
		t.Fatalf("derive opaque browser and target IDs: %v", err)
	}
	beforeOracle, err := waitForFixtureOracle(ctx, fixture.StateURL(), func(oracle fixtureOracle) bool {
		return oracle.Ready && oracle.Value == "initial" && oracle.VisibleText == "initial" && !oracle.Pending
	})
	if err != nil {
		t.Fatalf("read initial independent page oracle: %v", err)
	}

	cdpURL := strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeGateI2Config(configDir, cdpURL, fixture.server.URL, browserID, targetID); err != nil {
		t.Fatalf("write Gate I2 browser config: %v", err)
	}

	capturePath := filepath.Join(artifactRoot, "provider.json")
	recordDir := filepath.Join(artifactRoot, "recording")
	audioPath := filepath.Join(artifactRoot, "assistant.wav")

	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	binaryPath := filepath.Join(artifactRoot, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	sessionArgs := []string{
		"session",
		"--provider", "openai",
		"--model", gateI2Model,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-browser", browserID,
		"--browser-tab", targetID,
		"--browser-origin", fixture.server.URL,
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "always",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--audio-in", inputPath,
		"--audio-out", audioPath,
		"--system-prompt", systemPromptPath,
		"--max-duration", gateI2Timeout.String(),
	}

	runContext, cancelRun := context.WithTimeout(ctx, gateI2Timeout+25*time.Second)
	process, err := startGateCommandWithEnvironment(runContext, binaryPath, configDir, []string{"AGENT_MODEL__OPENAI__API_KEY=" + apiKey}, sessionArgs...)
	if err != nil {
		cancelRun()
		t.Fatalf("start production agent CLI: %v", err)
	}
	sessionResult, waitErr := process.wait(runContext)
	cancelRun()
	var runErr error
	if waitErr != nil {
		runErr = waitErr
	} else if sessionResult.Err != nil || sessionResult.ExitCode != 0 {
		runErr = fmt.Errorf("agent session exit=%d err=%v", sessionResult.ExitCode, sessionResult.Err)
	}
	if runErr != nil {
		t.Logf("Gate I2 session returned an error (capture validation remains authoritative): %v", runErr)
	}

	capture, captureErr := gwtesting.LoadSessionCapture(capturePath)
	var observation gateI2Observation
	var inspectErr error
	if captureErr == nil {
		observation, inspectErr = inspectGateI2Capture(capture)
	} else {
		inspectErr = captureErr
	}

	afterOracle := readGateI2Oracle(ctx, fixture.StateURL())
	postVersion, postVersionErr := readDevToolsVersion(ctx, baseURL)
	postTarget, postTargetErr := readGateI2FixtureTarget(ctx, baseURL, rawTarget.ID, fixtureURL)
	independentState := inspectedPageState{}
	independentErr := error(nil)
	if postVersionErr == nil && postTargetErr == nil {
		independentState, independentErr = inspectExternalTarget(ctx, browser.endpoint(), rawTarget.ID)
	}
	audioFileBytes := gateI2FileSize(audioPath)

	validationErr := inspectErr
	var validation gateI2Validation
	if validationErr == nil {
		validation, validationErr = validateGateI2Observation(observation, fixtureURL, browserID, targetID, message, audioFileBytes)
	}
	if validationErr == nil && runErr != nil {
		validationErr = fmt.Errorf("live session returned an error: %v", runErr)
	}
	if validationErr == nil {
		if afterOracle.Value != "completed:"+message || afterOracle.VisibleText != "completed:"+message || afterOracle.Pending || !hasFixtureInvocation(afterOracle, completeToolName+":"+message) {
			validationErr = fmt.Errorf("after oracle=%+v, want completed fixture mutation", afterOracle)
		}
	}
	if validationErr == nil && (postVersionErr != nil || postTargetErr != nil) {
		validationErr = fmt.Errorf("Chrome post-session liveness failed: version=%v target=%v", postVersionErr, postTargetErr)
	}
	if validationErr == nil && independentErr != nil {
		validationErr = fmt.Errorf("independent DOM oracle failed: %v", independentErr)
	}
	if validationErr == nil && !gateI2StateMatchesOracle(independentState, afterOracle) {
		validationErr = fmt.Errorf("independent DOM state=%+v disagrees with HTTP oracle=%+v", independentState, afterOracle)
	}

	evidence := gateI2Evidence{
		Schema:                 "webmcp.gate-i2.evidence.v1",
		ObservedAtUTC:          time.Now().UTC().Format(time.RFC3339Nano),
		Pins:                   gateI2PinsFromLock(pinned.Lock, version.Browser),
		Provider:               observation.Provider,
		Model:                  observation.Model,
		APIKeySource:           keySource,
		SpokenRequest:          request,
		ExpectedMessage:        message,
		RequestInput:           "audio-only",
		AdvertisedTools:        append([]string(nil), observation.AdvertisedTools...),
		SessionUpdateCount:     observation.SessionUpdateCount,
		Calls:                  gateI2EvidenceCalls(observation.Calls),
		ToolOutputs:            gateI2EvidenceOutputs(observation.Outputs),
		SpokenTranscript:       observation.SpokenTranscript,
		TerminalStatus:         observation.TerminalStatus,
		OutputAudioBytes:       observation.AudioBytesAfterInvoke,
		AudioFileBytes:         audioFileBytes,
		BeforeOracle:           beforeOracle,
		AfterOracle:            afterOracle,
		IndependentDOM:         independentState,
		ChromeAliveAfterRun:    postVersionErr == nil,
		TargetPresentAfterRun:  postTargetErr == nil,
		BrowserVersionAfterRun: postVersion.Browser,
		TargetURLAfterRun:      postTarget.URL,
		CapturePath:            capturePath,
		RecordDir:              recordDir,
		AudioPath:              audioPath,
		ValidationError:        gateI2ErrorString(validationErr),
		Pass:                   validationErr == nil,
	}
	if validationErr == nil {
		evidence.ListToolRef = validation.ListToolRef
		evidence.InvokeToolRef = validation.InvokeToolRef
		evidence.RawInputJSON = validation.RawInputJSON
		evidence.Reason = validation.Reason
		evidence.GroundedFinalState = true
	}
	evidencePath := filepath.Join(artifactRoot, "acceptance-report.json")
	if err := writeGateI2Evidence(evidencePath, evidence); err != nil {
		t.Fatalf("write Gate I2 evidence: %v", err)
	}
	t.Logf("Gate I2 evidence: %s", evidencePath)

	if validationErr != nil {
		t.Fatalf("Gate I2 measurement failed: %v; evidence=%s", validationErr, evidencePath)
	}

	closeErr := browser.Close()
	closed = true
	if closeErr != nil {
		t.Logf("Chrome process cleanup returned: %v", closeErr)
	}
	t.Logf("WEBMCP_GATE_I2_PASS chrome=%s revision=%s browser=%s target=%s list_tools_ref=%s invoke_ref=%s input_json=%q transcript=%q output_audio_bytes=%d capture=%s record_dir=%s", lockedChromeVersion, lockedChromeRevision, browserID, targetID, validation.ListToolRef, validation.InvokeToolRef, validation.RawInputJSON, observation.SpokenTranscript, observation.AudioBytesAfterInvoke, capturePath, recordDir)
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

type gateI2Observation struct {
	Provider              string
	Model                 string
	Instructions          string
	AdvertisedTools       []string
	SessionUpdateCount    int
	SessionUpdateIndex    int
	FirstInputIndex       int
	Calls                 []gateI2Call
	Outputs               []gateI2Output
	ResponseCreates       []int
	AudioDeltas           []gateI2AudioDelta
	SpokenTranscript      string
	SpokenTranscriptIndex int
	AudioBytesAfterInvoke int
	TerminalStatus        string
	TerminalIndex         int
	ProviderErrors        int
}

type gateI2Call struct {
	Index          int
	ArgumentsIndex int
	Name           string
	CallID         string
	Arguments      string
}

type gateI2Output struct {
	Index  int
	CallID string
	Output string
}

type gateI2AudioDelta struct {
	Index int
	Bytes int
}

type gateI2Validation struct {
	ListToolRef   string
	InvokeToolRef string
	RawInputJSON  string
	Reason        string
}

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

func inspectGateI2Capture(capture gwtesting.SessionCapture) (gateI2Observation, error) {
	observation := gateI2Observation{
		Provider:              capture.Provider.Name,
		Model:                 capture.Provider.Model,
		SessionUpdateIndex:    -1,
		FirstInputIndex:       -1,
		SpokenTranscriptIndex: -1,
		TerminalIndex:         -1,
	}
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			return observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}
		if record.Direction == gwtesting.DirectionClientToServer {
			switch record.Type {
			case "session.update":
				observation.SessionUpdateCount++
				var event struct {
					Session struct {
						Instructions string `json:"instructions"`
						Tools        []struct {
							Name string `json:"name"`
						} `json:"tools"`
					} `json:"session"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode session.update: %w", err)
				}
				if observation.SessionUpdateIndex < 0 {
					observation.SessionUpdateIndex = index
				}
				observation.Instructions = event.Session.Instructions
				observation.AdvertisedTools = observation.AdvertisedTools[:0]
				for _, tool := range event.Session.Tools {
					observation.AdvertisedTools = append(observation.AdvertisedTools, tool.Name)
				}
			case "input_audio_buffer.append":
				if observation.FirstInputIndex < 0 {
					observation.FirstInputIndex = index
				}
			case "conversation.item.create":
				var event struct {
					Item struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
						Output string `json:"output"`
					} `json:"item"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode conversation.item.create: %w", err)
				}
				if event.Item.Type == "function_call_output" {
					observation.Outputs = append(observation.Outputs, gateI2Output{Index: index, CallID: event.Item.CallID, Output: event.Item.Output})
				}
			case "response.create":
				observation.ResponseCreates = append(observation.ResponseCreates, index)
			}
			continue
		}
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_item.added":
			var event struct {
				Item struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					CallID string `json:"call_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode response.output_item.added: %w", err)
			}
			if event.Item.Type == "function_call" {
				observation.Calls = append(observation.Calls, gateI2Call{Index: index, ArgumentsIndex: -1, Name: event.Item.Name, CallID: event.Item.CallID})
			}
		case "response.function_call_arguments.done":
			var event struct {
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode response.function_call_arguments.done: %w", err)
			}
			callIndex := -1
			for candidate := len(observation.Calls) - 1; candidate >= 0; candidate-- {
				if observation.Calls[candidate].ArgumentsIndex >= 0 {
					continue
				}
				if event.CallID == "" || observation.Calls[candidate].CallID == event.CallID {
					callIndex = candidate
					break
				}
			}
			if callIndex < 0 {
				return observation, fmt.Errorf("function-call arguments have no correlating output item call_id=%q", event.CallID)
			}
			observation.Calls[callIndex].ArgumentsIndex = index
			observation.Calls[callIndex].Arguments = event.Arguments
			if observation.Calls[callIndex].CallID == "" {
				observation.Calls[callIndex].CallID = event.CallID
			}
			if observation.Calls[callIndex].Name == "" {
				observation.Calls[callIndex].Name = event.Name
			}
		case "response.output_audio_transcript.done":
			var event struct {
				Transcript string `json:"transcript"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode output transcript: %w", err)
			}
			if strings.TrimSpace(event.Transcript) != "" {
				if observation.SpokenTranscriptIndex < 0 {
					observation.SpokenTranscriptIndex = index
				}
				if observation.SpokenTranscript != "" {
					observation.SpokenTranscript += " "
				}
				observation.SpokenTranscript += strings.TrimSpace(event.Transcript)
			}
		case "response.output_audio.delta", "response.audio.delta":
			var event struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode output audio delta: %w", err)
			}
			if event.Delta != "" {
				decoded, err := base64.StdEncoding.DecodeString(event.Delta)
				if err != nil {
					return observation, fmt.Errorf("decode output audio delta: %w", err)
				}
				observation.AudioDeltas = append(observation.AudioDeltas, gateI2AudioDelta{Index: index, Bytes: len(decoded)})
			}
		case "response.done":
			var event struct {
				Status   string `json:"status"`
				Response struct {
					Status string `json:"status"`
				} `json:"response"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode response.done: %w", err)
			}
			status := event.Response.Status
			if status == "" {
				status = event.Status
			}
			if observation.TerminalIndex < 0 && observation.SpokenTranscriptIndex >= 0 && index > observation.SpokenTranscriptIndex {
				observation.TerminalIndex = index
				observation.TerminalStatus = status
			}
		case "error":
			observation.ProviderErrors++
		}
	}
	observation.AudioBytesAfterInvoke = gateI2AudioBytesAfterInvoke(observation)
	return observation, nil
}

func gateI2AudioBytesAfterInvoke(observation gateI2Observation) int {
	invokeOutputIndex := -1
	for _, call := range observation.Calls {
		if call.Name != webmcp.InvokeToolName || call.CallID == "" {
			continue
		}
		for _, output := range observation.Outputs {
			if output.CallID == call.CallID && output.Index > invokeOutputIndex {
				invokeOutputIndex = output.Index
			}
		}
	}
	if invokeOutputIndex < 0 {
		return 0
	}

	bytes := 0
	for _, delta := range observation.AudioDeltas {
		if delta.Index <= invokeOutputIndex || (observation.TerminalIndex >= 0 && delta.Index >= observation.TerminalIndex) {
			continue
		}
		bytes += delta.Bytes
	}
	return bytes
}

func validateGateI2Observation(observation gateI2Observation, fixtureURL, expectedBrowserID, expectedTargetID, expectedMessage string, audioFileBytes int64) (gateI2Validation, error) {
	if observation.Provider != "openai" || observation.Model != gateI2Model {
		return gateI2Validation{}, fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, gateI2Model)
	}
	if observation.ProviderErrors != 0 {
		return gateI2Validation{}, fmt.Errorf("provider emitted %d error event(s)", observation.ProviderErrors)
	}
	if observation.SessionUpdateCount != 1 || observation.SessionUpdateIndex < 0 || observation.FirstInputIndex < 0 || observation.SessionUpdateIndex >= observation.FirstInputIndex {
		return gateI2Validation{}, fmt.Errorf("session.update/input ordering count=%d update=%d first_audio=%d, want exactly one update before spoken audio", observation.SessionUpdateCount, observation.SessionUpdateIndex, observation.FirstInputIndex)
	}
	if !strings.Contains(observation.Instructions, "input_json") || !strings.Contains(observation.Instructions, "webmcp_list_tools") {
		return gateI2Validation{}, errors.New("session instructions omit the Gate I2 discovery and JSON-in-string contract")
	}
	if !gateI2SameStrings(observation.AdvertisedTools, webmcp.StableToolNames()) {
		return gateI2Validation{}, fmt.Errorf("advertised tools=%v, want exactly the stable broker surface", observation.AdvertisedTools)
	}
	if len(observation.Calls) != 4 || len(observation.Outputs) != 4 {
		return gateI2Validation{}, fmt.Errorf("provider trace has %d calls and %d textual outputs, want one list_tabs/select_tab/list_tools/invoke sequence", len(observation.Calls), len(observation.Outputs))
	}

	wantedNames := []string{webmcp.ListTabsToolName, webmcp.SelectTabToolName, webmcp.ListToolsToolName, webmcp.InvokeToolName}
	outputsByCall := make(map[string]gateI2Output, len(observation.Outputs))
	for _, output := range observation.Outputs {
		if output.CallID == "" {
			return gateI2Validation{}, fmt.Errorf("function_call_output at index %d has an empty call_id", output.Index)
		}
		if _, exists := outputsByCall[output.CallID]; exists {
			return gateI2Validation{}, fmt.Errorf("function_call_output call_id=%q occurred more than once", output.CallID)
		}
		outputsByCall[output.CallID] = output
	}
	for index, call := range observation.Calls {
		if call.Name != wantedNames[index] {
			return gateI2Validation{}, fmt.Errorf("call %d name=%q, want %q", index, call.Name, wantedNames[index])
		}
		if call.CallID == "" || call.ArgumentsIndex <= call.Index {
			return gateI2Validation{}, fmt.Errorf("call %d correlation/order invalid: %+v", index, call)
		}
		output, ok := outputsByCall[call.CallID]
		if !ok || output.Index <= call.ArgumentsIndex {
			return gateI2Validation{}, fmt.Errorf("call %s has no terminal textual output after arguments", call.CallID)
		}
		if _, err := webmcp.UnmarshalToolResult([]byte(output.Output)); err != nil {
			return gateI2Validation{}, fmt.Errorf("call %s output is not a validated textual WebMCP envelope: %w; envelope=%s", call.CallID, err, output.Output)
		}
	}

	listTabsOutput := outputsByCall[observation.Calls[0].CallID]
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
	if err := decodeGateI2EnvelopeData(listTabsOutput.Output, &tabs); err != nil {
		return gateI2Validation{}, fmt.Errorf("decode webmcp_list_tabs envelope: %w", err)
	}
	matches := 0
	for _, target := range tabs.Targets {
		if target.BrowserID == expectedBrowserID && target.TargetID == expectedTargetID && target.Type == "page" && target.URL == fixtureURL && target.Origin == strings.TrimRight(fixtureURL, "/") && target.Eligible {
			matches++
		}
	}
	if matches != 1 {
		return gateI2Validation{}, fmt.Errorf("webmcp_list_tabs returned %d exact eligible fixture target rows, want one", matches)
	}

	selectArgs, err := decodeGateI2Object(observation.Calls[1].Arguments)
	if err != nil {
		return gateI2Validation{}, fmt.Errorf("decode webmcp_select_tab arguments: %w", err)
	}
	if gateI2StringValue(selectArgs, "browser_id") != expectedBrowserID || gateI2StringValue(selectArgs, "target_id") != expectedTargetID {
		return gateI2Validation{}, fmt.Errorf("webmcp_select_tab did not reuse list_tabs IDs: browser=%q target=%q", gateI2StringValue(selectArgs, "browser_id"), gateI2StringValue(selectArgs, "target_id"))
	}

	listToolsOutput := outputsByCall[observation.Calls[2].CallID]
	var catalog struct {
		Tools []struct {
			Ref  string `json:"ref"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := decodeGateI2EnvelopeData(listToolsOutput.Output, &catalog); err != nil {
		return gateI2Validation{}, fmt.Errorf("decode webmcp_list_tools envelope: %w", err)
	}
	listToolRef := ""
	for _, tool := range catalog.Tools {
		if tool.Name == completeToolName {
			if listToolRef != "" {
				return gateI2Validation{}, fmt.Errorf("webmcp_list_tools returned duplicate %s descriptors", completeToolName)
			}
			listToolRef = tool.Ref
		}
	}
	if !webmcp.IsValidToolRef(webmcp.ToolRef(listToolRef)) {
		return gateI2Validation{}, fmt.Errorf("webmcp_list_tools returned invalid %s ref %q", completeToolName, listToolRef)
	}

	invokeArgs, err := decodeGateI2Object(observation.Calls[3].Arguments)
	if err != nil {
		return gateI2Validation{}, fmt.Errorf("Gate I2 measurement: decode webmcp_invoke arguments: %w; raw arguments=%s; raw-schema acceleration fallback is required if the provider cannot reliably produce input_json", err, observation.Calls[3].Arguments)
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

	invokeOutput := outputsByCall[observation.Calls[3].CallID]
	var invokeEnvelope struct {
		InvocationID string          `json:"invocation_id"`
		ToolRef      string          `json:"tool_ref"`
		Status       string          `json:"status"`
		Output       json.RawMessage `json:"output"`
	}
	if err := decodeGateI2EnvelopeData(invokeOutput.Output, &invokeEnvelope); err != nil {
		return gateI2Validation{}, fmt.Errorf("decode webmcp_invoke envelope: %w", err)
	}
	if invokeEnvelope.InvocationID == "" || invokeEnvelope.ToolRef != listToolRef || invokeEnvelope.Status != string(webmcp.InvocationCompleted) {
		return gateI2Validation{}, fmt.Errorf("webmcp_invoke result=%+v, want completed correlated invocation", invokeEnvelope)
	}
	var pageOutput struct {
		Greeting string `json:"greeting"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(invokeEnvelope.Output, &pageOutput); err != nil || pageOutput.Greeting != "hello" || pageOutput.Message != expectedMessage {
		return gateI2Validation{}, fmt.Errorf("webmcp_invoke page output=%s, want greeting hello and message %q", invokeEnvelope.Output, expectedMessage)
	}

	responseCreatesAfterInvoke := 0
	for _, index := range observation.ResponseCreates {
		if index > invokeOutput.Index {
			responseCreatesAfterInvoke++
		}
	}
	if responseCreatesAfterInvoke == 0 {
		return gateI2Validation{}, errors.New("no response.create followed the delivered webmcp_invoke result")
	}
	if observation.SpokenTranscriptIndex <= invokeOutput.Index || !strings.Contains(strings.ToLower(observation.SpokenTranscript), strings.ToLower(expectedMessage)) {
		return gateI2Validation{}, fmt.Errorf("spoken transcript=%q is absent, precedes the invoke result, or omits the final message", observation.SpokenTranscript)
	}
	if observation.TerminalIndex <= observation.SpokenTranscriptIndex || observation.TerminalStatus == "failed" || observation.TerminalStatus == "cancelled" || observation.TerminalStatus == "incomplete" {
		return gateI2Validation{}, fmt.Errorf("terminal response status=%q index=%d, spoken index=%d", observation.TerminalStatus, observation.TerminalIndex, observation.SpokenTranscriptIndex)
	}
	if observation.AudioBytesAfterInvoke == 0 || audioFileBytes <= 44 {
		return gateI2Validation{}, fmt.Errorf("spoken output audio is missing: provider_delta_bytes=%d file_bytes=%d", observation.AudioBytesAfterInvoke, audioFileBytes)
	}
	return gateI2Validation{ListToolRef: listToolRef, InvokeToolRef: invokeToolRef, RawInputJSON: rawInputJSON, Reason: reason}, nil
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

func loadGateI2APIKey() (string, string, error) {
	path := strings.TrimSpace(os.Getenv(gateI2KeyFileEnv))
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return "", gateI2KeyFileEnv, err
		}
		defer file.Close()
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

func gateI2SpokenInput(t *testing.T, artifactRoot, request string) string {
	t.Helper()
	for _, command := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("Gate I2 spoken input requires %s", command)
		}
	}
	aiffPath := filepath.Join(artifactRoot, "request.aiff")
	wavPath := filepath.Join(artifactRoot, "request.wav")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		if target.ID == rawTargetID && target.Type == "page" && target.URL == fixtureURL {
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
		gateI2SameStrings(state.Invocations, oracle.Invocations)
}

func gateI2FileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
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

func gateI2SameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
