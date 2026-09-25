//go:build live

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	sessionPageToolsSwitchVoiceLiveEnv         = "WEBMCP_PAGETOOLS_SWITCH_VOICE_LIVE"
	sessionPageToolsSwitchVoiceArtifactEnv     = "WEBMCP_PAGETOOLS_SWITCH_VOICE_ARTIFACT_DIR"
	sessionPageToolsSwitchVoiceKeyFileEnv      = "OPENAI_API_KEY_FILE"
	sessionPageToolsSwitchVoiceMaxDuration     = 30 * time.Second
	sessionPageToolsSwitchVoiceRunGrace        = 25 * time.Second
	sessionPageToolsSwitchVoiceChromeVersion   = "152.0.7977.64"
	sessionPageToolsSwitchVoiceArtifactMode    = 0o700
	sessionPageToolsSwitchVoiceEvidenceMode    = 0o600
	sessionPageToolsSwitchVoiceCaptureFilename = "provider.json"
	sessionPageToolsSwitchVoiceSessionUpdate   = "session.update"
)

var sessionPageToolsSwitchVoiceSystemPrompt = `You are a concise voice operator controlling two already-open WebMCP pages.

Follow this protocol exactly:
- Startup may have no selected page because two eligible pages are present. For the first request, use webmcp_list_tabs, identify Cubecade by its safe title/origin, and call webmcp_select_tab with its exact listed browser_id and target_id. Then read its current cube state with its directly advertised page tool. Do not move the cube.
- When the customer says to switch to the document editor, use webmcp_list_tabs, find the eligible Margin Editor page, and call webmcp_select_tab with its exact browser_id and target_id. Do not reconnect or use exec.
- After the switch, use the directly advertised Margin page tools. Preserve the exact title and exact content dictated by the customer, create one document, and read it back with get_document.
- When the customer says to switch back, use webmcp_list_tabs and webmcp_select_tab for the eligible Cubecade page, then read the cube state with its directly advertised page tool.
- Use only the selected page's directly advertised first-class tools for page work. Stable webmcp tools are available for discovery and selection. Never call a page tool after its page is no longer selected.
- Keep every spoken response to five words or fewer. After the final cube read, say goodbye.
`

var sessionPageToolsSwitchVoiceTokenWords = []string{
	"amber", "beacon", "cedar", "comet", "dawn", "ember", "fern", "harbor",
	"jade", "maple", "meadow", "mango", "orbit", "otter", "pebble", "quartz",
	"raven", "river", "saffron", "summit", "thistle", "violet", "willow", "zephyr",
}

// TestSessionPageToolsSwitchVoiceAgainstLiveChrome is the one credentialed
// voice confirmation for the mid-session page-tool publication story. It is
// deliberately opt-in and runs one short scheduled-audio session against one
// externally owned pinned Chrome. The raw provider capture and record-dir
// bundle are kept under a private run-scoped artifact directory and are never
// source-controlled.
func TestSessionPageToolsSwitchVoiceAgainstLiveChrome(t *testing.T) {
	if os.Getenv(sessionPageToolsSwitchVoiceLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the single credentialed WebMCP voice confirmation", sessionPageToolsSwitchVoiceLiveEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("the voice confirmation uses the qualified %s Chrome; observed %s/%s", sessionPageToolsSwitchVoiceChromeVersion, runtime.GOOS, runtime.GOARCH)
	}

	apiKey, keySource := sessionPageToolsSwitchVoiceAPIKey(t)
	cdpURL := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL"))
	if cdpURL == "" {
		t.Skip("set WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL to the externally launched pinned Chrome /json/version endpoint")
	}

	artifactRoot := sessionPageToolsSwitchVoiceArtifactRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	chromeVersion := sessionPageToolsSwitchVoiceChromeVersionString(t, ctx, cdpURL)
	assertLiveChromeStartupShape(t, ctx, cdpURL, sessionPageToolsLiveCubecadeOrigin, "Cubecade")
	openLiveCDPTab(t, ctx, cdpURL, sessionPageToolsLiveMarginURL, "Margin")
	cubeTarget, marginTarget := sessionPageToolsSwitchVoiceTargets(t, ctx, cdpURL)
	if cubeTarget.BrowserID != marginTarget.BrowserID {
		t.Fatalf("voice targets use different browsers: Cubecade=%q Margin=%q", cubeTarget.BrowserID, marginTarget.BrowserID)
	}
	t.Logf("launch shape: external pinned Chrome %s, fresh profile, startup_urls=[%s], opened_after_startup=%s via PUT /json/new?<encoded-url>, endpoint_scope=loopback", chromeVersion, sessionPageToolsLiveCubecadeURL, sessionPageToolsLiveMarginURL)
	t.Logf("target identities: browser=%s Cubecade=%s (%s) Margin=%s (%s)", cubeTarget.BrowserID, cubeTarget.TargetID, cubeTarget.Origin, marginTarget.TargetID, marginTarget.Origin)

	token := sessionPageToolsSwitchVoiceToken(t)
	title := "voice switch " + token
	content := "dynamic session " + token
	turns := []string{
		"Choose the Cubecade page, then read the cube state.",
		"Now switch to the document editor tab.",
		fmt.Sprintf("Create a document with the exact title %s and the exact content %s, then read it back.", title, content),
		"Switch back to the cube.",
		"Read the cube state again and say goodbye.",
	}
	audioPaths := sessionPageToolsSwitchVoiceAudio(t, artifactRoot, turns)

	agentBinary := buildLiveAgentCLI(t, ctx)
	beforeCubeState := sessionPageToolsSwitchVoiceCubeState(t, ctx, agentBinary, cdpURL, cubeTarget, "before")

	capturePath := filepath.Join(artifactRoot, sessionPageToolsSwitchVoiceCaptureFilename)
	recordDir := filepath.Join(artifactRoot, "recording")
	audioOutPath := filepath.Join(artifactRoot, "assistant.wav")
	runErr := runSessionPageToolsSwitchVoiceProcess(t, ctx, agentBinary, apiKey, sessionPageToolsSwitchVoiceArgs(t, artifactRoot, cdpURL, audioPaths))

	afterCubeState := sessionPageToolsSwitchVoiceCubeState(t, ctx, agentBinary, cdpURL, cubeTarget, "after")
	marginCatalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, marginTarget, liveMarginPageTools())

	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load one-run voice provider capture: %v", err)
	}
	observation, err := inspectSessionPageToolsSwitchVoiceCapture(capture)
	if err != nil {
		t.Fatalf("inspect one-run voice provider capture: %v", err)
	}
	documentID, err := validateSessionPageToolsSwitchVoiceObservation(observation, cubeTarget, marginTarget, title, content)
	if err != nil {
		t.Fatalf("validate one-run voice provider trace: %v", err)
	}

	directDocument := directLiveInvoke(t, ctx, agentBinary, cdpURL, marginTarget, findDirectToolRef(t, marginCatalog, liveGetDocumentToolName), map[string]any{"document_id": documentID})
	requireLiveSuccess(t, directDocument, "direct CLI Margin get_document after voice run")
	if !sessionPageToolsSwitchVoiceDocumentMatches(directDocument.Data, title, content) {
		t.Fatalf("direct CLI Margin document did not preserve exact title/content: %s", truncateLiveText(directDocument.Data, 2000))
	}
	if err := validateSessionPageToolsSwitchVoiceRecordDir(recordDir); err != nil {
		t.Fatalf("validate one-run voice record-dir: %v", err)
	}
	if info, err := os.Stat(audioOutPath); err != nil || info.Size() <= 44 {
		t.Fatalf("assistant audio artifact = err:%v size:%d, want non-empty WAV", err, sessionPageToolsSwitchVoiceFileSize(audioOutPath))
	}

	if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("one-run voice session failed: %v", runErr)
	}
	assertLiveChromeStillHasOrigins(t, ctx, cdpURL, sessionPageToolsLiveCubecadeOrigin, sessionPageToolsLiveMarginOrigin)
	t.Logf("sanitized transcript: user=%s assistant=%s", sessionPageToolsSwitchVoiceJSONStrings(observation.UserTranscripts), sessionPageToolsSwitchVoiceJSONStrings(observation.AssistantTranscripts))
	t.Logf("oracle Cubecade before: %s", truncateLiveText(beforeCubeState.Data, 1200))
	t.Logf("oracle Margin get_document: %s", truncateLiveText(directDocument.Data, 1600))
	t.Logf("oracle Cubecade after: %s", truncateLiveText(afterCubeState.Data, 1200))
	t.Logf("voice evidence: model=%s max_duration=%s key_source=%s browser_auto_select=single pinned_browser_tab=<none> origin_filter=<none> provider_connections=%d definition_transitions=Cubecade(2)->Margin(10)->Cubecade(2) title=%q content=%q recording=<artifact>/recording capture=<artifact>/%s", sessionPageToolsSwitchVoiceModel, sessionPageToolsSwitchVoiceMaxDuration, keySource, observation.SessionCreated, title, content, sessionPageToolsSwitchVoiceCaptureFilename)
	t.Logf("voice artifacts retained outside source control: %s", artifactRoot)
}

// sessionPageToolsSwitchVoiceCubeState reads the Cubecade state through the
// direct CLI oracle.
func sessionPageToolsSwitchVoiceCubeState(t *testing.T, ctx context.Context, agentBinary, cdpURL string, cubeTarget sessionPageToolsLiveTarget, phase string) webmcp.ToolResultEnvelope {
	t.Helper()
	catalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, cubeTarget, liveCubecadePageTools())
	state := directLiveInvoke(t, ctx, agentBinary, cdpURL, cubeTarget, findDirectToolRef(t, catalog, ambiguousCubeStateTool), map[string]any{})
	requireLiveSuccess(t, state, "direct CLI Cubecade state "+phase+" voice run")
	return state
}

// sessionPageToolsSwitchVoiceArgs writes the system prompt and returns the
// production session command line for the one voice run.
func sessionPageToolsSwitchVoiceArgs(t *testing.T, artifactRoot, cdpURL string, audioPaths []string) []string {
	t.Helper()
	systemPromptPath := filepath.Join(artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(sessionPageToolsSwitchVoiceSystemPrompt), sessionPageToolsSwitchVoiceEvidenceMode); err != nil {
		t.Fatalf("write voice system prompt: %v", err)
	}
	args := []string{
		"-C", filepath.Join(artifactRoot, "config"),
		"session",
		"--provider", config.ProviderOpenAI,
		"--model", sessionPageToolsSwitchVoiceModel,
		"--voice", "marin",
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-auto-select", "single",
		"--browser-activate-tab", "false",
		"--browser-persist-selection", "false",
		"--browser-allowed-origin", sessionPageToolsLiveCubecadeOrigin,
		"--browser-allowed-origin", sessionPageToolsLiveMarginOrigin,
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "always",
		"--browser-invocation-timeout", "20s",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", filepath.Join(artifactRoot, sessionPageToolsSwitchVoiceCaptureFilename),
		"--record-dir", filepath.Join(artifactRoot, "recording"),
		"--audio-out", filepath.Join(artifactRoot, "assistant.wav"),
		"--system-prompt", systemPromptPath,
		"--max-duration", sessionPageToolsSwitchVoiceMaxDuration.String(),
	}
	for _, audioPath := range audioPaths {
		args = append(args, "--audio-in-turn", audioPath)
	}
	return args
}

// runSessionPageToolsSwitchVoiceProcess runs the voice session and returns its
// exit error; a deadline stop is expected for the bounded session.
func runSessionPageToolsSwitchVoiceProcess(t *testing.T, ctx context.Context, agentBinary, apiKey string, args []string) error {
	t.Helper()
	processCtx, cancelProcess := context.WithTimeout(ctx, sessionPageToolsSwitchVoiceMaxDuration+sessionPageToolsSwitchVoiceRunGrace)
	defer cancelProcess()
	process := exec.CommandContext(processCtx, agentBinary, args...)
	process.Env = append(os.Environ(), "AGENT_MODEL__OPENAI__API_KEY="+apiKey)
	var stdout, stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	runErr := process.Run()
	if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		t.Logf("voice process returned %v; stdout=%s stderr=%s", runErr, truncateLiveText(stdout.Bytes(), 1200), truncateLiveText(stderr.Bytes(), 1200))
	}
	return runErr
}

func sessionPageToolsSwitchVoiceAPIKey(t *testing.T) (string, string) {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv(sessionPageToolsSwitchVoiceKeyFileEnv)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read OpenAI API key file: %v", err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			t.Fatalf("OpenAI API key file is empty")
		}
		return key, sessionPageToolsSwitchVoiceKeyFileEnv
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return key, "OPENAI_API_KEY"
	}
	t.Skip("OPENAI_API_KEY_FILE or OPENAI_API_KEY is not set; skipping the credentialed WebMCP voice confirmation")
	return "", ""
}

func sessionPageToolsSwitchVoiceArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(sessionPageToolsSwitchVoiceArtifactEnv))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, sessionPageToolsSwitchVoiceArtifactMode); err != nil {
		t.Fatalf("create voice artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "webmcp-switch-voice-")
	if err != nil {
		t.Fatalf("create voice artifact directory: %v", err)
	}
	return root
}

func sessionPageToolsSwitchVoiceChromeVersionString(t *testing.T, ctx context.Context, cdpURL string) string {
	t.Helper()
	var version struct {
		Browser string `json:"Browser"`
	}
	if err := getLiveCDPJSON(ctx, cdpURL, "/json/version", &version); err != nil {
		t.Fatalf("inspect pinned Chrome version: %v", err)
	}
	if !strings.Contains(version.Browser, sessionPageToolsSwitchVoiceChromeVersion) {
		t.Fatalf("Chrome version = %q, want pinned %s", version.Browser, sessionPageToolsSwitchVoiceChromeVersion)
	}
	return version.Browser
}

func sessionPageToolsSwitchVoiceTargets(t *testing.T, ctx context.Context, cdpURL string) (sessionPageToolsLiveTarget, sessionPageToolsLiveTarget) {
	t.Helper()
	cfg := livePageToolsConfig(t, cdpURL)
	cfg.Browser.Selection.Origin = sessionPageToolsLiveCubecadeOrigin
	cfg.Browser.Selection.Persist = false
	cfg.Browser.Policy.AllowedOrigins = []string{sessionPageToolsLiveCubecadeOrigin, sessionPageToolsLiveMarginOrigin}

	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(cfg)
	if err != nil {
		t.Fatalf("voice target capability factory: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed && capabilities.Close != nil {
			if closeErr := capabilities.Close(); closeErr != nil {
				t.Logf("voice target capability cleanup: %v", closeErr)
			}
		}
	})
	if capabilities.Initialize == nil {
		t.Fatal("voice target capability did not expose initialization")
	}
	if err := capabilities.Initialize(ctx); err != nil {
		t.Fatalf("initialize Cubecade for voice target discovery: %v", err)
	}
	definitions, err := capabilities.RefreshDefinitionsWithError(ctx)
	if err != nil {
		t.Fatalf("refresh Cubecade voice target definitions: %v", err)
	}
	requireLivePageSurface(t, definitions, messages.CanonicalToolDefinitions(capabilities.Definitions), liveCubecadePageTools(), "voice Cubecade bootstrap")
	tabs := waitForLivePageTargets(t, ctx, capabilities.Executor)
	cube, margin := requireLivePageTargets(t, tabs)
	if capabilities.Close != nil {
		if err := capabilities.Close(); err != nil {
			t.Fatalf("close voice target discovery capability: %v", err)
		}
	}
	closed = true
	return cube, margin
}

func sessionPageToolsSwitchVoiceToken(t *testing.T) string {
	t.Helper()
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate voice run token: %v", err)
	}
	words := make([]string, len(raw))
	for index, value := range raw {
		words[index] = sessionPageToolsSwitchVoiceTokenWords[int(value)%len(sessionPageToolsSwitchVoiceTokenWords)]
	}
	return strings.Join(words, " ")
}

func sessionPageToolsSwitchVoiceAudio(t *testing.T, root string, turns []string) []string {
	t.Helper()
	for _, command := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("voice confirmation requires %s", command)
		}
	}
	audioDir := filepath.Join(root, "input")
	if err := os.MkdirAll(audioDir, sessionPageToolsSwitchVoiceArtifactMode); err != nil {
		t.Fatalf("create voice input directory: %v", err)
	}
	paths := make([]string, 0, len(turns))
	for index, turn := range turns {
		aiffPath := filepath.Join(audioDir, fmt.Sprintf("turn-%02d.aiff", index+1))
		wavPath := filepath.Join(audioDir, fmt.Sprintf("turn-%02d.wav", index+1))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		output, err := exec.CommandContext(ctx, "say", "-o", aiffPath, turn).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("generate voice turn %d: %v: %s", index+1, err, strings.TrimSpace(string(output)))
		}
		ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
		output, err = exec.CommandContext(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", aiffPath, wavPath).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("convert voice turn %d to WAV: %v: %s", index+1, err, strings.TrimSpace(string(output)))
		}
		paths = append(paths, wavPath)
	}
	return paths
}

func inspectSessionPageToolsSwitchVoiceCapture(capture gwtesting.SessionCapture) (sessionPageToolsSwitchVoiceObservation, error) {
	observation := sessionPageToolsSwitchVoiceObservation{
		Provider: capture.Provider.Name,
		Model:    capture.Provider.Model,
	}
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			continue
		}
		var err error
		switch record.Direction {
		case gwtesting.DirectionClientToServer:
			err = observation.addClientRecord(index, record.Type, payload)
		case gwtesting.DirectionServerToClient:
			err = observation.addServerRecord(index, record.Type, payload)
		}
		if err != nil {
			return observation, err
		}
	}
	return observation, nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addClientRecord(index int, recordType string, payload json.RawMessage) error {
	switch recordType {
	case sessionPageToolsSwitchVoiceSessionUpdate:
		surface, err := sessionPageToolsSwitchVoiceSurfaceFromUpdate(index, payload)
		if err != nil {
			return err
		}
		if len(surface.Tools) > 0 {
			observation.Surfaces = append(observation.Surfaces, surface)
		}
	case "conversation.item.create":
		output, ok, err := sessionPageToolsSwitchVoiceOutputFromItem(index, payload)
		if err != nil {
			return err
		}
		if ok {
			observation.Outputs = append(observation.Outputs, output)
		}
	}
	return nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addServerRecord(index int, recordType string, payload json.RawMessage) error {
	switch recordType {
	case "session.created":
		observation.SessionCreated++
	case "response.output_item.added":
		return observation.addFunctionCall(index, payload)
	case "response.function_call_arguments.done":
		return observation.addFunctionArguments(index, payload)
	case "conversation.item.input_audio_transcription.completed":
		if text := sessionPageToolsSwitchVoiceStringField(payload, "transcript"); text != "" {
			observation.UserTranscripts = append(observation.UserTranscripts, text)
		}
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		if text := sessionPageToolsSwitchVoiceStringField(payload, "transcript"); text != "" {
			observation.AssistantTranscripts = append(observation.AssistantTranscripts, text)
		}
	case "response.output_text.done":
		if text := sessionPageToolsSwitchVoiceStringField(payload, "text"); text != "" {
			observation.AssistantTranscripts = append(observation.AssistantTranscripts, text)
		}
	}
	return nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addFunctionCall(index int, payload json.RawMessage) error {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			CallID string `json:"call_id"`
			ID     string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode function call at record %d: %w", index, err)
	}
	if event.Item.Type != "function_call" {
		return nil
	}
	callID := event.Item.CallID
	if callID == "" {
		callID = event.Item.ID
	}
	observation.Calls = append(observation.Calls, sessionPageToolsSwitchVoiceCall{Index: index, Name: event.Item.Name, CallID: callID, ArgumentsAt: -1})
	return nil
}

func (observation *sessionPageToolsSwitchVoiceObservation) addFunctionArguments(index int, payload json.RawMessage) error {
	var event struct {
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode function arguments at record %d: %w", index, err)
	}
	callIndex := -1
	for candidate := len(observation.Calls) - 1; candidate >= 0; candidate-- {
		if observation.Calls[candidate].ArgumentsAt >= 0 {
			continue
		}
		if event.CallID == "" || observation.Calls[candidate].CallID == event.CallID {
			callIndex = candidate
			break
		}
	}
	if callIndex < 0 {
		return fmt.Errorf("function arguments at record %d have no matching call_id=%q", index, event.CallID)
	}
	call := &observation.Calls[callIndex]
	call.Arguments = event.Arguments
	call.ArgumentsAt = index
	if call.Name == "" {
		call.Name = event.Name
	}
	if call.CallID == "" {
		call.CallID = event.CallID
	}
	return nil
}

func sessionPageToolsSwitchVoiceSurfaceFromUpdate(index int, payload json.RawMessage) (sessionPageToolsSwitchVoiceSurface, error) {
	var event struct {
		Session struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return sessionPageToolsSwitchVoiceSurface{}, fmt.Errorf("decode session.update at record %d: %w", index, err)
	}
	surface := sessionPageToolsSwitchVoiceSurface{Index: index, Tools: make([]sessionPageToolsSwitchVoiceTool, 0, len(event.Session.Tools))}
	for _, raw := range event.Session.Tools {
		var name struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &name); err != nil {
			return sessionPageToolsSwitchVoiceSurface{}, fmt.Errorf("decode tool in session.update at record %d: %w", index, err)
		}
		if name.Name == "" {
			return sessionPageToolsSwitchVoiceSurface{}, fmt.Errorf("session.update at record %d contains an unnamed tool", index)
		}
		surface.Tools = append(surface.Tools, sessionPageToolsSwitchVoiceTool{Name: name.Name, Raw: append(json.RawMessage(nil), raw...)})
	}
	return surface, nil
}

func sessionPageToolsSwitchVoiceOutputFromItem(index int, payload json.RawMessage) (sessionPageToolsSwitchVoiceOutput, bool, error) {
	var event struct {
		Item struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return sessionPageToolsSwitchVoiceOutput{}, false, fmt.Errorf("decode tool result at record %d: %w", index, err)
	}
	if event.Item.Type != "function_call_output" {
		return sessionPageToolsSwitchVoiceOutput{}, false, nil
	}
	var output string
	if err := json.Unmarshal(event.Item.Output, &output); err != nil {
		output = string(event.Item.Output)
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(output))
	if err != nil {
		return sessionPageToolsSwitchVoiceOutput{}, false, fmt.Errorf("decode tool result at record %d: %w", index, err)
	}
	return sessionPageToolsSwitchVoiceOutput{Index: index, CallID: event.Item.CallID, Envelope: envelope}, true, nil
}

func sessionPageToolsSwitchVoiceStringField(payload json.RawMessage, field string) string {
	var object map[string]any
	if json.Unmarshal(payload, &object) != nil {
		return ""
	}
	value, ok := object[field].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func validateSessionPageToolsSwitchVoiceRecordDir(path string) error {
	for _, name := range []string{"manifest.json", "client.transcript.jsonl", "agent.transcript.jsonl"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			return fmt.Errorf("stat %s: %w", name, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("record-dir artifact %s is empty", name)
		}
	}
	return nil
}

func sessionPageToolsSwitchVoiceDocumentMatches(raw json.RawMessage, title, content string) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return sessionPageToolsSwitchVoiceFindDocument(value, title, content)
}

func sessionPageToolsSwitchVoiceFindDocument(value any, title, content string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if gotTitle, titleOK := typed["title"].(string); titleOK {
			if gotContent, contentOK := typed["content"].(string); contentOK && gotTitle == title && gotContent == content {
				return true
			}
		}
		for _, child := range typed {
			if sessionPageToolsSwitchVoiceFindDocument(child, title, content) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if sessionPageToolsSwitchVoiceFindDocument(child, title, content) {
				return true
			}
		}
	}
	return false
}

func sessionPageToolsSwitchVoiceJSONStrings(values []string) string {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func sessionPageToolsSwitchVoiceFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
