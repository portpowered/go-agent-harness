//go:build live && darwin && arm64

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// TestSessionPageToolsSwitchVoiceAgainstLiveChrome is the one credentialed
// voice confirmation for the mid-session page-tool publication story. It is
// deliberately opt-in and runs one short scheduled-audio session against one
// externally owned pinned Chrome. The raw provider capture and record-dir
// bundle are kept under a private run-scoped artifact directory and are never
// source-controlled.
func TestSessionPageToolsSwitchVoiceAgainstLiveChrome(t *testing.T) {
	apiKey, keySource := sessionPageToolsSwitchVoiceAPIKey(t)
	cdpURL := strings.TrimSpace(os.Getenv("WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL"))
	if cdpURL == "" {
		t.Fatal("set WEBMCP_PAGETOOLS_SWITCH_LIVE_CDP_URL to the externally launched pinned Chrome /json/version endpoint")
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

const sessionPageToolsSwitchVoiceSystemPrompt = `You are a concise voice operator controlling two already-open WebMCP pages.

Follow this protocol exactly:
- Startup may have no selected page because two eligible pages are present. For the first request, use webmcp_list_tabs, identify Cubecade by its safe title/origin, and call webmcp_select_tab with its exact listed browser_id and target_id. Then read its current cube state with its directly advertised page tool. Do not move the cube.
- When the customer says to switch to the document editor, use webmcp_list_tabs, find the eligible Margin Editor page, and call webmcp_select_tab with its exact browser_id and target_id. Do not reconnect or use exec.
- After the switch, use the directly advertised Margin page tools. Preserve the exact title and exact content dictated by the customer, create one document, and read it back with get_document.
- When the customer says to switch back, use webmcp_list_tabs and webmcp_select_tab for the eligible Cubecade page, then read the cube state with its directly advertised page tool.
- Use only the selected page's directly advertised first-class tools for page work. Stable webmcp tools are available for discovery and selection. Never call a page tool after its page is no longer selected.
- Keep every spoken response to five words or fewer. After the final cube read, say goodbye.
`

func sessionPageToolsSwitchVoiceTokenWords() []string {
	return []string{
		"amber", "beacon", "cedar", "comet", "dawn", "ember", "fern", "harbor",
		"jade", "maple", "meadow", "mango", "orbit", "otter", "pebble", "quartz",
		"raven", "river", "saffron", "summit", "thistle", "violet", "willow", "zephyr",
	}
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

func sessionPageToolsSwitchVoiceArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(sessionPageToolsSwitchVoiceArtifactEnv))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, sessionPageToolsSwitchVoiceArtifactMode); err != nil {
		t.Fatalf("create voice artifact parent: %v", err)
	}
	root := filepath.Join(parent, "webmcp-switch-voice-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Mkdir(root, 0o700); err != nil {
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

	capabilities, err := NewSessionToolCapabilitiesFactory(nil, nil)(ctx, cfg)
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
	vocabulary := sessionPageToolsSwitchVoiceTokenWords()
	words := make([]string, len(raw))
	for index, value := range raw {
		words[index] = vocabulary[int(value)%len(vocabulary)]
	}
	return strings.Join(words, " ")
}

func sessionPageToolsSwitchVoiceAudio(t *testing.T, root string, turns []string) []string {
	t.Helper()
	for _, command := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("voice confirmation requires %s: %v", command, err)
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
