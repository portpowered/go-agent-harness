//go:build live

package chrome

// This is the one explicitly opted-in, credentialed story-006 proof. It uses
// the same pinned headless Chrome and deterministic Cubecade page as the
// credential-free screenshot proof, but drives the shipped CLI through one
// real Realtime voice turn. The raw capture and recording directory remain in
// a caller-selected private artifact directory; only sanitized facts are
// emitted to the test log.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	cubecadeLiveVoiceEnv          = "WEBMCP_CUBECADE_LIVE_VOICE"
	cubecadeLiveVoiceArtifactEnv  = "WEBMCP_CUBECADE_ARTIFACT_DIR"
	cubecadeLiveVoiceQuestion     = "What do you see on the page?"
	cubecadeLiveVoiceMaxDuration  = 30 * time.Second
	cubecadeLiveVoiceTestTimeout  = 2 * time.Minute
	cubecadeLiveVoiceRunGrace     = 20 * time.Second
	cubecadeLiveVoiceArtifactMode = 0o700
	cubecadeLiveVoiceEvidenceMode = 0o600
)

// TestPinnedChromeCubecadeSpokenPageSight is the release-facing story-006
// proof. It is deliberately one billed run: the model receives one spoken
// question, calls show_page once, gets one image projection, and must answer
// with at least two facts that are visible in the returned page pixels.
func TestPinnedChromeCubecadeSpokenPageSight(t *testing.T) {
	if os.Getenv(cubecadeLiveVoiceEnv) != "1" {
		t.Skipf("set %s=1 to run the one billed spoken page-sight proof", cubecadeLiveVoiceEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Skipf("spoken Cubecade proof uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}
	apiKey, keySource := requireLiveOpenAIKey(t, "OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed spoken page-sight proof")

	artifactRoot := cubecadeLiveVoiceArtifactRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), cubecadeLiveVoiceTestTimeout)
	defer cancel()
	chromeWorkDir := filepath.Join(artifactRoot, "chrome")
	if err := os.Mkdir(chromeWorkDir, cubecadeLiveVoiceArtifactMode); err != nil {
		t.Fatalf("create Chrome work directory: %v", err)
	}
	sight := startCubecadeLiveVoiceChrome(t, ctx, chromeWorkDir)
	paths := prepareCubecadeLiveVoiceInputs(t, artifactRoot, sight)
	repository, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	binaryPath := filepath.Join(artifactRoot, "agent")
	if err := buildGateBinary(ctx, repository, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	sessionResult, err := runLiveAgentSession(ctx, cubecadeLiveVoiceMaxDuration+cubecadeLiveVoiceRunGrace, binaryPath, paths.configDir, apiKey, paths.sessionArgs(sight))
	var startErr liveSessionStartError
	if errors.As(err, &startErr) {
		t.Fatalf("start spoken sight agent session: %v", startErr.cause)
	}
	if err != nil {
		t.Fatalf("spoken sight agent session wait: %v", err)
	}
	if sessionResult.Err != nil || sessionResult.ExitCode != 0 {
		t.Fatalf("spoken sight agent session exit=%d err=%v stdout=%q stderr=%q", sessionResult.ExitCode, sessionResult.Err, sessionResult.Stdout, sessionResult.Stderr)
	}

	capture, err := gatewaytesting.LoadSessionCapture(paths.capturePath)
	if err != nil {
		t.Fatalf("load spoken sight provider capture: %v", err)
	}
	observation, err := inspectCubecadeLiveVoiceCapture(capture, sight.browserID, sight.targetID, sight.oracle)
	if err != nil {
		t.Fatalf("inspect spoken sight provider capture: %v", err)
	}
	if err := verifyCubecadeLiveVoiceRecording(paths.recordDir, observation); err != nil {
		t.Fatalf("verify spoken sight recording directory: %v", err)
	}
	audioInfo, err := os.Stat(paths.audioOutPath)
	if err != nil || audioInfo.Size() <= 44 {
		t.Fatalf("spoken output audio = size %d error %v, want a non-empty WAV", fileSize(paths.audioOutPath), err)
	}

	if err := sight.browser.close(); err != nil {
		t.Logf("spoken sight Chrome cleanup returned: %v", err)
	}
	t.Logf("WEBMCP_CUBECADE_SPOKEN_SIGHT_PASS model=%s key_source=%s question=%q chrome=%s revision=%s browser=%s target=%s tool=%s calls=%d image_parts=%d mime=%s image_bytes=%d dimensions=%dx%d sha256=%s capture_elapsed=%dms spoken_facts=%s terminal=%s exit=0 event_order=%s record_dir=<artifact>/recording", capture.Provider.Model, keySource, cubecadeLiveVoiceQuestion, sight.pinned.Lock.Version, sight.pinned.Lock.Revision, observation.BrowserID, observation.TargetID, webmcp.ShowPageToolName, observation.ShowPageCalls, observation.InputImageCount, observation.MIMEType, observation.ByteLength, observation.Width, observation.Height, observation.SHA256, observation.CaptureElapsedMS, strings.Join(observation.SpokenFacts, "+"), observation.TerminalStatus, strings.Join(observation.EventOrder, ">"))
}

// cubecadeLiveVoiceSight is the pinned Chrome showing the solved Cubecade
// fixture, with the exact target identity and its independent oracle.
type cubecadeLiveVoiceSight struct {
	pinned    pinnedChrome
	fixture   *cubecadeScreenshotFixture
	browser   *liveBrowserOwner
	baseURL   string
	browserID string
	targetID  string
	oracle    cubecadeSightOracle
}

func startCubecadeLiveVoiceChrome(t *testing.T, ctx context.Context, workDir string) *cubecadeLiveVoiceSight {
	t.Helper()
	pinned, err := acquirePinnedChrome(ctx, workDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}
	sight := &cubecadeLiveVoiceSight{pinned: pinned, fixture: newCubecadeScreenshotFixture()}
	t.Cleanup(sight.fixture.Close)
	fixtureURL := sight.fixture.URL()
	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	sight.browser = ownLiveBrowser(t, browser, "spoken sight Chrome cleanup")
	sight.baseURL = browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, sight.baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read qualified Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(ctx, sight.baseURL, fixtureURL)
	if err != nil {
		t.Fatalf("discover exact Cubecade target: %v", err)
	}
	if sight.browserID, sight.targetID, err = gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID); err != nil {
		t.Fatalf("derive public browser and target IDs: %v", err)
	}
	if sight.oracle, err = inspectCubecadeSightOracle(ctx, browser.endpoint(), rawTarget.ID); err != nil {
		t.Fatalf("inspect independent Cubecade sight oracle: %v", err)
	}
	if sight.oracle.URL != fixtureURL || !sight.oracle.Solved || !strings.Contains(strings.ToUpper(sight.oracle.StatusText), "SOLVED") {
		t.Fatalf("Cubecade oracle = %+v, want the selected solved fixture page", sight.oracle)
	}
	return sight
}

// cubecadeLiveVoicePaths are the spoken proof's inputs and artifacts.
type cubecadeLiveVoicePaths struct {
	configDir        string
	systemPromptPath string
	audioInPath      string
	capturePath      string
	audioOutPath     string
	recordDir        string
	cdpURL           string
}

func prepareCubecadeLiveVoiceInputs(t *testing.T, artifactRoot string, sight *cubecadeLiveVoiceSight) cubecadeLiveVoicePaths {
	t.Helper()
	paths := cubecadeLiveVoicePaths{
		configDir:        filepath.Join(artifactRoot, "config"),
		systemPromptPath: filepath.Join(artifactRoot, "system-prompt.txt"),
		capturePath:      filepath.Join(artifactRoot, "provider.json"),
		audioOutPath:     filepath.Join(artifactRoot, "assistant.wav"),
		recordDir:        filepath.Join(artifactRoot, "recording"),
		cdpURL:           strings.TrimRight(sight.baseURL, "/") + "/json/version",
	}
	if err := os.Mkdir(paths.configDir, cubecadeLiveVoiceArtifactMode); err != nil {
		t.Fatalf("create spoken sight config directory: %v", err)
	}
	if err := writeCubecadeLiveVoiceConfig(paths.configDir, paths.cdpURL, sight.fixture.server.URL, sight.browserID, sight.targetID); err != nil {
		t.Fatalf("write spoken sight browser config: %v", err)
	}
	if err := os.WriteFile(paths.systemPromptPath, []byte(cubecadeLiveVoiceSystemPrompt), cubecadeLiveVoiceEvidenceMode); err != nil {
		t.Fatalf("write spoken sight system prompt: %v", err)
	}
	paths.audioInPath = gateI2SpokenInput(t.Context(), t, artifactRoot, cubecadeLiveVoiceQuestion)
	return paths
}

func (p cubecadeLiveVoicePaths) sessionArgs(sight *cubecadeLiveVoiceSight) []string {
	return []string{
		"session",
		"--provider", liveProviderOpenAI,
		"--model", cubecadeModel,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", p.cdpURL,
		"--browser-browser", sight.browserID,
		"--browser-tab", sight.targetID,
		"--browser-origin", sight.fixture.server.URL,
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "always",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", p.capturePath,
		"--record-dir", p.recordDir,
		"--audio-in", p.audioInPath,
		"--audio-out", p.audioOutPath,
		"--system-prompt", p.systemPromptPath,
		"--max-duration", cubecadeLiveVoiceMaxDuration.String(),
	}
}

const cubecadeLiveVoiceSystemPrompt = `You are a terse visual assistant. When the user asks what you see on the page or screen, call show_page exactly once with an empty object before speaking. Do not use another tool, infer facts from this instruction, or claim success before the tool result. After the image result arrives, answer the user's question in five words or fewer and state at least two distinct facts that are visibly grounded in the returned pixels. If capture fails, explain the failure accurately and do not claim to see the page.`

func verifyCubecadeLiveVoiceRecording(recordDir string, observation cubecadeLiveVoiceObservation) error {
	manifestBytes, err := os.ReadFile(filepath.Join(recordDir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("validate manifest: %w", err)
	}
	var screenshotPath string
	for _, artifact := range manifest.Artifacts {
		if !strings.HasPrefix(artifact.Path, "screenshots/") {
			continue
		}
		if screenshotPath != "" {
			return errors.New("recording manifest contains more than one screenshot artifact")
		}
		screenshotPath = artifact.Path
		if artifact.SHA256 != observation.SHA256 {
			return fmt.Errorf("recorded screenshot digest=%q, want %q", artifact.SHA256, observation.SHA256)
		}
	}
	if screenshotPath == "" {
		return errors.New("recording manifest has no screenshot artifact")
	}
	stored, err := os.ReadFile(filepath.Join(recordDir, filepath.FromSlash(screenshotPath)))
	if err != nil {
		return fmt.Errorf("read recorded screenshot: %w", err)
	}
	digest := sha256.Sum256(stored)
	if len(stored) != observation.ByteLength || hex.EncodeToString(digest[:]) != observation.SHA256 || !bytes.Equal(stored, observation.Image) {
		return errors.New("recorded screenshot bytes do not match the provider image projection")
	}
	screenshotEntries, err := os.ReadDir(filepath.Join(recordDir, "screenshots"))
	if err != nil {
		return fmt.Errorf("read screenshot directory: %w", err)
	}
	if len(screenshotEntries) != 1 || screenshotEntries[0].Name() != filepath.Base(screenshotPath) {
		return fmt.Errorf("recorded screenshot directory entries=%v, want only %q", screenshotEntries, filepath.Base(screenshotPath))
	}
	logBytes, err := os.ReadFile(filepath.Join(recordDir, "session-log.jsonl"))
	if err != nil {
		return fmt.Errorf("read session log: %w", err)
	}
	for _, want := range []string{`"tool_name":"show_page"`, `"source":"browser_page"`, `"browser_id":"` + observation.BrowserID + `"`, `"target_id":"` + observation.TargetID + `"`, `"sha256":"` + observation.SHA256 + `"`, screenshotPath} {
		if !bytes.Contains(logBytes, []byte(want)) {
			return fmt.Errorf("session log omits %q", want)
		}
	}
	return nil
}

func writeCubecadeLiveVoiceConfig(configDir, cdpURL, origin, browserID, targetID string) error {
	return writeLiveBrowserConfig(configDir, liveBrowserConfig{cdpURL: cdpURL, origin: origin, browserID: browserID, targetID: targetID, cancelOnInterrupt: "always", invocationTimeout: "20s"})
}

func cubecadeLiveVoiceArtifactRoot(t *testing.T) string {
	t.Helper()
	return cubecadeNestedArtifactRoot(t, "webmcp-cubecade-spoken-sight-", "spoken sight")
}

// cubecadeNestedArtifactRoot creates a private per-run directory under the
// operator's Cubecade artifact directory, or under a test temp directory.
func cubecadeNestedArtifactRoot(t *testing.T, prefix, label string) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(cubecadeLiveVoiceArtifactEnv))
	if parent == "" {
		parent = t.TempDir()
	} else if err := os.MkdirAll(parent, cubecadeLiveVoiceArtifactMode); err != nil {
		t.Fatalf("create %s artifact parent: %v", label, err)
	}
	root, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		t.Fatalf("create %s artifact directory: %v", label, err)
	}
	return root
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
