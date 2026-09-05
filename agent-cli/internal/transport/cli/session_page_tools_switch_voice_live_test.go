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
	"sort"
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
	sessionPageToolsSwitchVoiceModel           = "gpt-realtime-2.1-mini"
	sessionPageToolsSwitchVoiceMaxDuration     = 30 * time.Second
	sessionPageToolsSwitchVoiceRunGrace        = 25 * time.Second
	sessionPageToolsSwitchVoiceChromeVersion   = "152.0.7977.64"
	sessionPageToolsSwitchVoiceArtifactMode    = 0o700
	sessionPageToolsSwitchVoiceEvidenceMode    = 0o600
	sessionPageToolsSwitchVoiceCaptureFilename = "provider.json"
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
	assertLiveChromeStartupShape(t, ctx, cdpURL)
	openLiveMarginTab(t, ctx, cdpURL)
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
	systemPromptPath := filepath.Join(artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(sessionPageToolsSwitchVoiceSystemPrompt), sessionPageToolsSwitchVoiceEvidenceMode); err != nil {
		t.Fatalf("write voice system prompt: %v", err)
	}

	agentBinary := buildLiveAgentCLI(t, ctx)
	beforeCatalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, cubeTarget, []string{"get_cube_state", "queue_cube_moves"})
	beforeCubeState := directLiveInvoke(t, ctx, agentBinary, cdpURL, cubeTarget, findDirectToolRef(t, beforeCatalog, "get_cube_state"), map[string]any{})
	requireLiveSuccess(t, beforeCubeState, "direct CLI Cubecade state before voice run")

	capturePath := filepath.Join(artifactRoot, sessionPageToolsSwitchVoiceCaptureFilename)
	recordDir := filepath.Join(artifactRoot, "recording")
	audioOutPath := filepath.Join(artifactRoot, "assistant.wav")
	args := []string{
		"-C", filepath.Join(artifactRoot, "config"),
		"session",
		"--provider", "openai",
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
		"--record", capturePath,
		"--record-dir", recordDir,
		"--audio-out", audioOutPath,
		"--system-prompt", systemPromptPath,
		"--max-duration", sessionPageToolsSwitchVoiceMaxDuration.String(),
	}
	for _, audioPath := range audioPaths {
		args = append(args, "--audio-in-turn", audioPath)
	}

	processCtx, cancelProcess := context.WithTimeout(ctx, sessionPageToolsSwitchVoiceMaxDuration+sessionPageToolsSwitchVoiceRunGrace)
	process := exec.CommandContext(processCtx, agentBinary, args...)
	process.Env = append(os.Environ(), "AGENT_MODEL__OPENAI__API_KEY="+apiKey)
	var stdout, stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	runErr := process.Run()
	cancelProcess()
	if runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) {
		t.Logf("voice process returned %v; stdout=%s stderr=%s", runErr, truncateLiveText(stdout.Bytes(), 1200), truncateLiveText(stderr.Bytes(), 1200))
	}

	afterCatalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, cubeTarget, []string{"get_cube_state", "queue_cube_moves"})
	afterCubeState := directLiveInvoke(t, ctx, agentBinary, cdpURL, cubeTarget, findDirectToolRef(t, afterCatalog, "get_cube_state"), map[string]any{})
	requireLiveSuccess(t, afterCubeState, "direct CLI Cubecade state after voice run")

	marginCatalog := directLiveCatalog(t, ctx, agentBinary, cdpURL, marginTarget, []string{
		"add_comment",
		"create_document",
		"get_document",
		"list_comments",
		"list_documents",
		"open_document",
		"reopen_comment",
		"reply_to_comment",
		"resolve_comment",
		"update_document",
	})

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

	directDocument := directLiveInvoke(t, ctx, agentBinary, cdpURL, marginTarget, findDirectToolRef(t, marginCatalog, "get_document"), map[string]any{"document_id": documentID})
	requireLiveSuccess(t, directDocument, "direct CLI Margin get_document after voice run")
	if !sessionPageToolsSwitchVoiceDocumentMatches(directDocument.Data, title, content) {
		t.Fatalf("direct CLI Margin document did not preserve exact title/content: %s", truncateLiveJSON(directDocument.Data, 2000))
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
	assertLiveChromeStillRunning(t, ctx, cdpURL)
	t.Logf("sanitized transcript: user=%s assistant=%s", sessionPageToolsSwitchVoiceJSONStrings(observation.UserTranscripts), sessionPageToolsSwitchVoiceJSONStrings(observation.AssistantTranscripts))
	t.Logf("oracle Cubecade before: %s", truncateLiveJSON(beforeCubeState.Data, 1200))
	t.Logf("oracle Margin get_document: %s", truncateLiveJSON(directDocument.Data, 1600))
	t.Logf("oracle Cubecade after: %s", truncateLiveJSON(afterCubeState.Data, 1200))
	t.Logf("voice evidence: model=%s max_duration=%s key_source=%s browser_auto_select=single pinned_browser_tab=<none> origin_filter=<none> provider_connections=%d definition_transitions=Cubecade(2)->Margin(10)->Cubecade(2) title=%q content=%q recording=<artifact>/recording capture=<artifact>/%s", sessionPageToolsSwitchVoiceModel, sessionPageToolsSwitchVoiceMaxDuration, keySource, observation.SessionCreated, title, content, sessionPageToolsSwitchVoiceCaptureFilename)
	t.Logf("voice artifacts retained outside source control: %s", artifactRoot)
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
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Tools.Backend = config.BrowserToolsBackendWebMCP
	browser.Connection.CDPURL = cdpURL
	browser.Selection.Origin = sessionPageToolsLiveCubecadeOrigin
	browser.Selection.AutoSelect = config.BrowserAutoSelectSingle
	browser.Selection.Persist = false
	browser.Policy.AllowedOrigins = []string{sessionPageToolsLiveCubecadeOrigin, sessionPageToolsLiveMarginOrigin}
	cfg := &config.Config{Browser: browser, ConfigDir: t.TempDir()}
	for _, id := range config.DefaultToolIDs {
		cfg.Tools.List = append(cfg.Tools.List, config.ToolEntry{ID: id, Enabled: id == "exec"})
	}

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
	requireLivePageSurface(t, definitions, messages.CanonicalToolDefinitions(capabilities.Definitions), []string{"get_cube_state", "queue_cube_moves"}, "voice Cubecade bootstrap")
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

type sessionPageToolsSwitchVoiceTool struct {
	Name string
	Raw  json.RawMessage
}

type sessionPageToolsSwitchVoiceSurface struct {
	Index int
	Tools []sessionPageToolsSwitchVoiceTool
}

type sessionPageToolsSwitchVoiceCall struct {
	Index       int
	Name        string
	CallID      string
	Arguments   string
	ArgumentsAt int
}

type sessionPageToolsSwitchVoiceOutput struct {
	Index    int
	CallID   string
	Envelope webmcp.ToolResultEnvelope
}

type sessionPageToolsSwitchVoiceObservation struct {
	Provider             string
	Model                string
	SessionCreated       int
	Surfaces             []sessionPageToolsSwitchVoiceSurface
	Calls                []sessionPageToolsSwitchVoiceCall
	Outputs              []sessionPageToolsSwitchVoiceOutput
	UserTranscripts      []string
	AssistantTranscripts []string
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
		if record.Direction == gwtesting.DirectionClientToServer {
			switch record.Type {
			case "session.update":
				surface, err := sessionPageToolsSwitchVoiceSurfaceFromUpdate(index, payload)
				if err != nil {
					return observation, err
				}
				if len(surface.Tools) > 0 {
					observation.Surfaces = append(observation.Surfaces, surface)
				}
			case "conversation.item.create":
				output, ok, err := sessionPageToolsSwitchVoiceOutputFromItem(index, payload)
				if err != nil {
					return observation, err
				}
				if ok {
					observation.Outputs = append(observation.Outputs, output)
				}
			}
			continue
		}
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "session.created":
			observation.SessionCreated++
		case "response.output_item.added":
			var event struct {
				Item struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					CallID string `json:"call_id"`
					ID     string `json:"id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode function call at record %d: %w", index, err)
			}
			if event.Item.Type == "function_call" {
				callID := event.Item.CallID
				if callID == "" {
					callID = event.Item.ID
				}
				observation.Calls = append(observation.Calls, sessionPageToolsSwitchVoiceCall{Index: index, Name: event.Item.Name, CallID: callID, ArgumentsAt: -1})
			}
		case "response.function_call_arguments.done":
			var event struct {
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode function arguments at record %d: %w", index, err)
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
				return observation, fmt.Errorf("function arguments at record %d have no matching call_id=%q", index, event.CallID)
			}
			observation.Calls[callIndex].Arguments = event.Arguments
			observation.Calls[callIndex].ArgumentsAt = index
			if observation.Calls[callIndex].Name == "" {
				observation.Calls[callIndex].Name = event.Name
			}
			if observation.Calls[callIndex].CallID == "" {
				observation.Calls[callIndex].CallID = event.CallID
			}
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
	}
	return observation, nil
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

func validateSessionPageToolsSwitchVoiceObservation(observation sessionPageToolsSwitchVoiceObservation, cubeTarget, marginTarget sessionPageToolsLiveTarget, title, content string) (string, error) {
	if observation.Provider != "openai" || observation.Model != sessionPageToolsSwitchVoiceModel {
		return "", fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.Provider, observation.Model, sessionPageToolsSwitchVoiceModel)
	}
	if observation.SessionCreated != 1 {
		return "", fmt.Errorf("provider session.created count=%d, want one persistent connection", observation.SessionCreated)
	}
	if len(observation.UserTranscripts) < 5 {
		return "", fmt.Errorf("user transcript turns=%d, want five spoken turns", len(observation.UserTranscripts))
	}
	if len(observation.AssistantTranscripts) == 0 {
		return "", errors.New("voice capture has no assistant transcript")
	}
	transcript := strings.ToLower(strings.Join(observation.UserTranscripts, " "))
	for _, phrase := range []string{"cube", "document editor", "exact title", "switch back", "goodbye"} {
		if !strings.Contains(transcript, phrase) {
			return "", fmt.Errorf("spoken transcript %q is missing %q", transcript, phrase)
		}
	}

	pageNames := map[string]struct{}{
		"get_cube_state": {}, "queue_cube_moves": {},
		"add_comment": {}, "create_document": {}, "get_document": {}, "list_comments": {}, "list_documents": {}, "open_document": {}, "reopen_comment": {}, "reply_to_comment": {}, "resolve_comment": {}, "update_document": {},
	}
	baseNames := make([]string, 0)
	baseDefinitions := map[string]string{}
	stableDefinitions := map[string]string{}
	for _, surface := range observation.Surfaces {
		seen := map[string]bool{}
		for _, tool := range surface.Tools {
			if seen[tool.Name] {
				return "", fmt.Errorf("session.update at record %d repeats tool %q", surface.Index, tool.Name)
			}
			seen[tool.Name] = true
			if _, isPage := pageNames[tool.Name]; !isPage {
				if len(baseNames) == 0 || !containsSessionPageToolsSwitchVoice(baseNames, tool.Name) {
					baseNames = appendUniqueSessionPageToolsSwitchVoice(baseNames, tool.Name)
				}
			}
		}
	}
	sort.Strings(baseNames)
	if len(baseNames) == 0 {
		return "", errors.New("provider definitions contain no static or stable base tools")
	}
	wantStable := webmcp.StableToolNames()
	for _, stable := range wantStable {
		if !containsSessionPageToolsSwitchVoice(baseNames, stable) {
			return "", fmt.Errorf("provider definition base omitted stable broker tool %q", stable)
		}
	}

	wantCube := []string{"get_cube_state", "queue_cube_moves"}
	wantMargin := []string{"add_comment", "create_document", "get_document", "list_comments", "list_documents", "open_document", "reopen_comment", "reply_to_comment", "resolve_comment", "update_document"}
	for _, names := range [][]string{wantCube, wantMargin} {
		sort.Strings(names)
	}
	cubeAt, marginAt, returnedCubeAt := -1, -1, -1
	for index, surface := range observation.Surfaces {
		currentNames := make([]string, 0, len(surface.Tools))
		currentByName := map[string]string{}
		for _, tool := range surface.Tools {
			currentNames = append(currentNames, tool.Name)
			canonical, err := sessionPageToolsSwitchVoiceCanonicalJSON(tool.Raw)
			if err != nil {
				return "", fmt.Errorf("canonicalize provider definition %q: %w", tool.Name, err)
			}
			currentByName[tool.Name] = canonical
		}
		page := make([]string, 0)
		for _, name := range currentNames {
			if _, isPage := pageNames[name]; isPage {
				page = append(page, name)
			}
		}
		sort.Strings(currentNames)
		sort.Strings(page)
		for _, name := range baseNames {
			if !containsSessionPageToolsSwitchVoice(currentNames, name) {
				return "", fmt.Errorf("session.update at record %d omitted base tool %q", surface.Index, name)
			}
			if previous, ok := baseDefinitions[name]; ok && previous != currentByName[name] {
				return "", fmt.Errorf("static definition %q changed across session.update replacements", name)
			}
			baseDefinitions[name] = currentByName[name]
		}
		for _, name := range wantStable {
			if previous, ok := stableDefinitions[name]; ok && previous != currentByName[name] {
				return "", fmt.Errorf("stable definition %q changed across session.update replacements", name)
			}
			stableDefinitions[name] = currentByName[name]
		}
		wantNames := append(append([]string(nil), baseNames...), page...)
		sort.Strings(wantNames)
		if !sameSessionPageToolsSwitchVoiceStrings(currentNames, wantNames) {
			return "", fmt.Errorf("session.update at record %d has unexpected ordered surface: got=%v want=%v", surface.Index, currentNames, wantNames)
		}
		switch {
		case sameSessionPageToolsSwitchVoiceStrings(page, wantCube):
			if cubeAt < 0 {
				cubeAt = index
			} else if marginAt >= 0 && returnedCubeAt < 0 {
				returnedCubeAt = index
			}
		case sameSessionPageToolsSwitchVoiceStrings(page, wantMargin):
			if cubeAt < 0 {
				return "", fmt.Errorf("Margin surface at record %d preceded Cubecade surface", surface.Index)
			}
			if marginAt < 0 {
				marginAt = index
			}
		default:
			if len(page) != 0 {
				return "", fmt.Errorf("session.update at record %d advertised unexpected page tools %v", surface.Index, page)
			}
		}
	}
	if cubeAt < 0 || marginAt < 0 || returnedCubeAt < 0 || !(cubeAt < marginAt && marginAt < returnedCubeAt) {
		return "", fmt.Errorf("definition transitions did not prove Cubecade->Margin->Cubecade: cube=%d margin=%d returned_cube=%d surfaces=%d", cubeAt, marginAt, returnedCubeAt, len(observation.Surfaces))
	}

	outputs := map[string]sessionPageToolsSwitchVoiceOutput{}
	for _, output := range observation.Outputs {
		if output.CallID == "" {
			return "", fmt.Errorf("tool output at record %d omitted call_id", output.Index)
		}
		if _, exists := outputs[output.CallID]; exists {
			return "", fmt.Errorf("tool output call_id=%q occurred more than once", output.CallID)
		}
		outputs[output.CallID] = output
		if !output.Envelope.OK {
			return "", fmt.Errorf("voice tool %q failed: %+v", output.CallID, output.Envelope.Error)
		}
	}
	initialCubeSelected, marginSelected, cubeSelected := false, false, false
	cubeReadsBefore, cubeReadsAfter := 0, 0
	pageCallCount := map[string]int{}
	var documentID string
	for _, call := range observation.Calls {
		if call.CallID == "" || call.ArgumentsAt <= call.Index {
			return "", fmt.Errorf("uncorrelated voice call: %+v", call)
		}
		if _, ok := outputs[call.CallID]; !ok {
			return "", fmt.Errorf("voice call %q has no tool result", call.CallID)
		}
		switch call.Name {
		case webmcp.SelectTabToolName:
			var args struct {
				BrowserID string `json:"browser_id"`
				TargetID  string `json:"target_id"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				return "", fmt.Errorf("decode %s arguments: %w", call.Name, err)
			}
			switch {
			case args.BrowserID == marginTarget.BrowserID && args.TargetID == marginTarget.TargetID && !marginSelected:
				marginSelected = true
			case args.BrowserID == cubeTarget.BrowserID && args.TargetID == cubeTarget.TargetID && !marginSelected && !initialCubeSelected:
				initialCubeSelected = true
			case args.BrowserID == cubeTarget.BrowserID && args.TargetID == cubeTarget.TargetID && marginSelected && !cubeSelected:
				cubeSelected = true
			default:
				return "", fmt.Errorf("unexpected selection call arguments: browser=%q target=%q", args.BrowserID, args.TargetID)
			}
		case "get_cube_state", "queue_cube_moves", "add_comment", "create_document", "get_document", "list_comments", "list_documents", "open_document", "reopen_comment", "reply_to_comment", "resolve_comment", "update_document":
			pageCallCount[call.Name]++
			if call.Name == "get_cube_state" || call.Name == "queue_cube_moves" {
				if marginSelected && !cubeSelected {
					return "", fmt.Errorf("Cubecade page tool %q was called while Margin was selected", call.Name)
				}
				if call.Name == "queue_cube_moves" {
					return "", errors.New("voice scenario unexpectedly attempted to move the cube")
				}
				if cubeSelected {
					cubeReadsAfter++
				} else {
					cubeReadsBefore++
				}
			} else if !marginSelected || cubeSelected {
				return "", fmt.Errorf("Margin page tool %q was called outside the selected Margin interval", call.Name)
			}
			if call.Name == "create_document" {
				var args struct {
					Title   string `json:"title"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					return "", fmt.Errorf("decode create_document arguments: %w", err)
				}
				if args.Title != title || args.Content != content {
					return "", fmt.Errorf("create_document arguments=(%q,%q), want exact=(%q,%q)", args.Title, args.Content, title, content)
				}
				documentID = liveDocumentID(outputs[call.CallID].Envelope.Data)
				if documentID == "" {
					return "", errors.New("create_document result omitted document ID")
				}
			}
			if call.Name == "get_document" {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					return "", fmt.Errorf("decode get_document arguments: %w", err)
				}
				if got, _ := args["document_id"].(string); got != documentID {
					return "", fmt.Errorf("get_document document_id=%q, want created document %q", got, documentID)
				}
			}
		default:
			if !containsSessionPageToolsSwitchVoice(webmcp.StableToolNames(), call.Name) {
				return "", fmt.Errorf("unexpected provider tool call %q", call.Name)
			}
		}
	}
	if !initialCubeSelected || !marginSelected || !cubeSelected {
		return "", fmt.Errorf("selection calls initial_cube=%t margin=%t return_cube=%t, want exact startup selection and both directions", initialCubeSelected, marginSelected, cubeSelected)
	}
	if cubeReadsBefore == 0 || cubeReadsAfter == 0 || pageCallCount["create_document"] != 1 || pageCallCount["get_document"] < 1 {
		return "", fmt.Errorf("page call counts=%v cube_reads_before=%d cube_reads_after=%d, want cube reads on both sides and one create/get", pageCallCount, cubeReadsBefore, cubeReadsAfter)
	}
	if documentID == "" {
		return "", errors.New("voice trace did not produce a document ID")
	}
	return documentID, nil
}

func sessionPageToolsSwitchVoiceCanonicalJSON(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
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

func containsSessionPageToolsSwitchVoice(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func appendUniqueSessionPageToolsSwitchVoice(values []string, value string) []string {
	if containsSessionPageToolsSwitchVoice(values, value) {
		return values
	}
	return append(values, value)
}

func sameSessionPageToolsSwitchVoiceStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
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
