//go:build live && darwin && arm64

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
