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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
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
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("spoken Cubecade proof uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	apiKey, keySource, err := loadGateI2APIKey()
	if errors.Is(err, errGateI2MissingAPIKey) {
		t.Skip("OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed spoken page-sight proof")
	}
	if err != nil {
		t.Fatalf("load OpenAI API key: %v", err)
	}

	artifactRoot := cubecadeLiveVoiceArtifactRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), cubecadeLiveVoiceTestTimeout)
	defer cancel()

	chromeWorkDir := filepath.Join(artifactRoot, "chrome")
	if err := os.Mkdir(chromeWorkDir, cubecadeLiveVoiceArtifactMode); err != nil {
		t.Fatalf("create Chrome work directory: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, chromeWorkDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}

	fixture := newCubecadeScreenshotFixture()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	browser, err := launchPinnedChrome(ctx, pinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("spoken sight Chrome cleanup: %v", closeErr)
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
		t.Fatalf("discover exact Cubecade target: %v", err)
	}
	browserID, targetID, err := gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID)
	if err != nil {
		t.Fatalf("derive public browser and target IDs: %v", err)
	}
	oracle, err := inspectCubecadeSightOracle(ctx, browser.endpoint(), rawTarget.ID)
	if err != nil {
		t.Fatalf("inspect independent Cubecade sight oracle: %v", err)
	}
	if oracle.URL != fixtureURL || !oracle.Solved || !strings.Contains(strings.ToUpper(oracle.StatusText), "SOLVED") {
		t.Fatalf("Cubecade oracle = %+v, want the selected solved fixture page", oracle)
	}

	configDir := filepath.Join(artifactRoot, "config")
	if err := os.Mkdir(configDir, cubecadeLiveVoiceArtifactMode); err != nil {
		t.Fatalf("create spoken sight config directory: %v", err)
	}
	cdpURL := strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeCubecadeLiveVoiceConfig(configDir, cdpURL, fixture.server.URL, browserID, targetID); err != nil {
		t.Fatalf("write spoken sight browser config: %v", err)
	}
	systemPromptPath := filepath.Join(artifactRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(cubecadeLiveVoiceSystemPrompt), cubecadeLiveVoiceEvidenceMode); err != nil {
		t.Fatalf("write spoken sight system prompt: %v", err)
	}
	audioInPath := gateI2SpokenInput(t, artifactRoot, cubecadeLiveVoiceQuestion)
	capturePath := filepath.Join(artifactRoot, "provider.json")
	audioOutPath := filepath.Join(artifactRoot, "assistant.wav")
	recordDir := filepath.Join(artifactRoot, "recording")

	repository, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	binaryPath := filepath.Join(artifactRoot, "agent")
	if err := buildGateBinary(ctx, repository, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	sessionArgs := []string{
		"session",
		"--provider", "openai",
		"--model", cubecadeModel,
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
		"--audio-in", audioInPath,
		"--audio-out", audioOutPath,
		"--system-prompt", systemPromptPath,
		"--max-duration", cubecadeLiveVoiceMaxDuration.String(),
	}

	runContext, cancelRun := context.WithTimeout(ctx, cubecadeLiveVoiceMaxDuration+cubecadeLiveVoiceRunGrace)
	process, err := startGateCommandWithEnvironment(runContext, binaryPath, configDir, []string{"AGENT_MODEL__OPENAI__API_KEY=" + apiKey}, sessionArgs...)
	if err != nil {
		cancelRun()
		t.Fatalf("start spoken sight agent session: %v", err)
	}
	sessionResult, waitErr := process.wait(runContext)
	cancelRun()
	if waitErr != nil {
		t.Fatalf("spoken sight agent session wait: %v", waitErr)
	}
	if sessionResult.Err != nil || sessionResult.ExitCode != 0 {
		t.Fatalf("spoken sight agent session exit=%d err=%v stdout=%q stderr=%q", sessionResult.ExitCode, sessionResult.Err, sessionResult.Stdout, sessionResult.Stderr)
	}

	capture, err := gatewaytesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load spoken sight provider capture: %v", err)
	}
	observation, err := inspectCubecadeLiveVoiceCapture(capture, browserID, targetID, oracle)
	if err != nil {
		t.Fatalf("inspect spoken sight provider capture: %v", err)
	}
	if err := verifyCubecadeLiveVoiceRecording(recordDir, observation); err != nil {
		t.Fatalf("verify spoken sight recording directory: %v", err)
	}
	audioInfo, err := os.Stat(audioOutPath)
	if err != nil || audioInfo.Size() <= 44 {
		t.Fatalf("spoken output audio = size %d error %v, want a non-empty WAV", fileSize(audioOutPath), err)
	}

	closed = true
	if err := browser.Close(); err != nil {
		t.Logf("spoken sight Chrome cleanup returned: %v", err)
	}

	t.Logf("WEBMCP_CUBECADE_SPOKEN_SIGHT_PASS model=%s key_source=%s question=%q chrome=%s revision=%s browser=%s target=%s tool=%s calls=%d image_parts=%d mime=%s image_bytes=%d dimensions=%dx%d sha256=%s capture_elapsed=%dms spoken_facts=%s terminal=%s exit=0 event_order=%s record_dir=<artifact>/recording", capture.Provider.Model, keySource, cubecadeLiveVoiceQuestion, pinned.Lock.Version, pinned.Lock.Revision, observation.BrowserID, observation.TargetID, webmcp.ShowPageToolName, observation.ShowPageCalls, observation.InputImageCount, observation.MIMEType, observation.ByteLength, observation.Width, observation.Height, observation.SHA256, observation.CaptureElapsedMS, strings.Join(observation.SpokenFacts, "+"), observation.TerminalStatus, strings.Join(observation.EventOrder, ">"))
}

const cubecadeLiveVoiceSystemPrompt = `You are a terse visual assistant. When the user asks what you see on the page or screen, call show_page exactly once with an empty object before speaking. Do not use another tool, infer facts from this instruction, or claim success before the tool result. After the image result arrives, answer the user's question in five words or fewer and state at least two distinct facts that are visibly grounded in the returned pixels. If capture fails, explain the failure accurately and do not claim to see the page.`

type cubecadeLiveVoiceObservation struct {
	BrowserID         string
	TargetID          string
	MIMEType          string
	ByteLength        int
	Width             int
	Height            int
	SHA256            string
	ShowPageCalls     int
	FunctionOutput    int
	InputImageCount   int
	EncodedImageUses  int
	ContinuationCount int
	CaptureElapsedMS  int64
	TerminalStatus    string
	SpokenTranscript  string
	SpokenFacts       []string
	EventOrder        []string
	Image             []byte
}

type cubecadeLiveVoiceFunctionOutput struct {
	Index  int
	CallID string
	Result webmcpTools.ShowPageResult
	Image  []byte
}

func inspectCubecadeLiveVoiceCapture(capture gatewaytesting.SessionCapture, expectedBrowserID, expectedTargetID string, oracle cubecadeSightOracle) (cubecadeLiveVoiceObservation, error) {
	observation := cubecadeLiveVoiceObservation{EventOrder: make([]string, 0, len(capture.Records))}
	if capture.Provider.Name != "openai" || capture.Provider.Model != cubecadeModel {
		return observation, fmt.Errorf("provider=(%q,%q), want (openai,%q)", capture.Provider.Name, capture.Provider.Model, cubecadeModel)
	}

	showCallIndex := -1
	showCallID := ""
	showArgumentsIndex := -1
	functionOutputIndex := -1
	imageIndex := -1
	continuationIndex := -1
	terminalIndex := -1
	var output cubecadeLiveVoiceFunctionOutput
	type transcriptEvent struct {
		Index int
		Text  string
	}
	transcripts := make([]transcriptEvent, 0, 2)
	outputAudioIndices := make([]int, 0, 16)
	advertisedShowPage := false
	type responseDone struct {
		Index  int
		Status string
	}
	responseDoneEvents := make([]responseDone, 0, 2)
	responseCreateIndices := make([]int, 0, 2)

	for index, record := range capture.Records {
		prefix := "S"
		if record.Direction == gatewaytesting.DirectionClientToServer {
			prefix = "C"
		}
		observation.EventOrder = append(observation.EventOrder, prefix+":"+record.Type)
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			return observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}

		if record.Direction == gatewaytesting.DirectionServerToClient {
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
					return observation, fmt.Errorf("decode show_page output item: %w", err)
				}
				if event.Item.Type == "function_call" && event.Item.Name == webmcp.ShowPageToolName {
					observation.ShowPageCalls++
					showCallIndex = index
					showCallID = event.Item.CallID
				}
			case "response.function_call_arguments.done":
				var event struct {
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode show_page arguments: %w", err)
				}
				if event.Name == webmcp.ShowPageToolName {
					if event.CallID != showCallID {
						return observation, fmt.Errorf("show_page arguments call ID %q, want %q", event.CallID, showCallID)
					}
					if strings.TrimSpace(event.Arguments) != "{}" {
						return observation, fmt.Errorf("show_page arguments=%q, want an empty object", event.Arguments)
					}
					showArgumentsIndex = index
				}
			case "response.output_audio_transcript.done":
				var event struct {
					Transcript string `json:"transcript"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode spoken transcript: %w", err)
				}
				if strings.TrimSpace(event.Transcript) != "" {
					transcripts = append(transcripts, transcriptEvent{Index: index, Text: strings.TrimSpace(event.Transcript)})
				}
			case "response.output_audio.delta":
				outputAudioIndices = append(outputAudioIndices, index)
			case "response.done":
				var event struct {
					Response struct {
						Status string `json:"status"`
					} `json:"response"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal(payload, &event); err != nil {
					return observation, fmt.Errorf("decode response.done: %w", err)
				}
				status := event.Response.Status
				if status == "" {
					status = event.Status
				}
				responseDoneEvents = append(responseDoneEvents, responseDone{Index: index, Status: status})
			case "error":
				return observation, errors.New("provider emitted an error event")
			}
			continue
		}

		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		if record.Type == "session.update" {
			var event struct {
				Session struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				} `json:"session"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode session.update: %w", err)
			}
			showDefinitions := 0
			for _, tool := range event.Session.Tools {
				if tool.Name == webmcp.ShowPageToolName {
					showDefinitions++
				}
			}
			if showDefinitions > 1 {
				return observation, fmt.Errorf("session advertised show_page %d times, want at most once per update", showDefinitions)
			}
			if showDefinitions == 1 {
				advertisedShowPage = true
			}
		}
		if record.Type == "response.create" {
			responseCreateIndices = append(responseCreateIndices, index)
			continue
		}
		if record.Type != "conversation.item.create" {
			continue
		}
		var event struct {
			Item struct {
				Type    string `json:"type"`
				CallID  string `json:"call_id"`
				Output  string `json:"output"`
				Content []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
				} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return observation, fmt.Errorf("decode provider conversation item: %w", err)
		}
		switch event.Item.Type {
		case "function_call_output":
			if event.Item.CallID != showCallID {
				return observation, fmt.Errorf("function output call ID %q, want %q", event.Item.CallID, showCallID)
			}
			if observation.FunctionOutput > 0 {
				return observation, errors.New("show_page function output was delivered more than once")
			}
			if strings.Contains(strings.ToLower(event.Item.Output), "base64") || strings.Contains(strings.ToLower(event.Item.Output), "data:image/") {
				return observation, errors.New("show_page metadata envelope contains encoded pixels")
			}
			envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
			if err != nil {
				return observation, fmt.Errorf("decode show_page envelope: %w", err)
			}
			if !envelope.OK {
				return observation, fmt.Errorf("show_page returned failure: %+v", envelope.Error)
			}
			if err := json.Unmarshal(envelope.Data, &output.Result); err != nil {
				return observation, fmt.Errorf("decode show_page metadata: %w", err)
			}
			if output.Result.Version != webmcpTools.ShowPageResultVersion || output.Result.Status != webmcpTools.ShowPageResultStatusSuccess || output.Result.Source != "browser_page" || output.Result.TypedProjection != webmcpTools.ShowPageResultTypedProjectionInputImage || output.Result.BrowserID == "" || output.Result.TargetID == "" || output.Result.MIMEType != "image/png" || output.Result.ByteLength <= 0 || output.Result.Width <= 0 || output.Result.Height <= 0 || len(output.Result.SHA256) != sha256.Size*2 || strings.ToLower(output.Result.SHA256) != output.Result.SHA256 {
				return observation, fmt.Errorf("show_page metadata=%+v, want one complete browser-page PNG result", output.Result)
			}
			output.Index = index
			output.CallID = event.Item.CallID
			observation.FunctionOutput++
			functionOutputIndex = index
			observation.BrowserID = output.Result.BrowserID
			observation.TargetID = output.Result.TargetID
			observation.MIMEType = output.Result.MIMEType
			observation.ByteLength = output.Result.ByteLength
			observation.Width = output.Result.Width
			observation.Height = output.Result.Height
			observation.SHA256 = output.Result.SHA256
		case "message":
			for _, part := range event.Item.Content {
				if part.Type != "input_image" {
					continue
				}
				observation.InputImageCount++
				if imageIndex >= 0 {
					return observation, errors.New("more than one input_image projection was delivered")
				}
				imageMIME, imageBytes, err := decodeLiveVoiceDataURL(part.ImageURL)
				if err != nil {
					return observation, fmt.Errorf("decode show_page input_image: %w", err)
				}
				observation.MIMEType = imageMIME
				observation.Image = imageBytes
				imageIndex = index
				imageDigest := sha256.Sum256(imageBytes)
				observation.EncodedImageUses = countEncodedImageOccurrences(capture.Records, base64.StdEncoding.EncodeToString(imageBytes))
				if output.Result.ByteLength != len(imageBytes) || output.Result.SHA256 != hex.EncodeToString(imageDigest[:]) {
					return observation, fmt.Errorf("projected image bytes do not match show_page metadata")
				}
				if output.Result.MIMEType != imageMIME {
					return observation, fmt.Errorf("projected image MIME=%q, metadata MIME=%q", imageMIME, output.Result.MIMEType)
				}
				decoded, format, err := image.Decode(bytes.NewReader(imageBytes))
				if err != nil {
					return observation, fmt.Errorf("decode projected show_page image: %w", err)
				}
				if format != "png" || decoded.Bounds().Dx() <= 200 || decoded.Bounds().Dy() <= 200 || decoded.Bounds().Dx() != output.Result.Width || decoded.Bounds().Dy() != output.Result.Height {
					return observation, fmt.Errorf("projected show_page image format/dimensions=%s/%dx%d metadata=%dx%d", format, decoded.Bounds().Dx(), decoded.Bounds().Dy(), output.Result.Width, output.Result.Height)
				}
				if assertCubecadeScreenshotMarkerNoFatal(decoded, oracle) == 0 {
					return observation, errors.New("projected show_page image did not contain the independent Cubecade marker")
				}
			}
		}
	}

	if !advertisedShowPage {
		return observation, errors.New("session never advertised show_page")
	}
	if observation.ShowPageCalls != 1 || showCallID == "" || showArgumentsIndex <= showCallIndex {
		return observation, fmt.Errorf("show_page call count/id/order=%d/%q/%d/%d, want one real call with arguments after dispatch", observation.ShowPageCalls, showCallID, showCallIndex, showArgumentsIndex)
	}
	if observation.FunctionOutput != 1 || functionOutputIndex <= showArgumentsIndex {
		return observation, fmt.Errorf("show_page function output count/order=%d/%d, want one output after arguments", observation.FunctionOutput, functionOutputIndex)
	}
	if observation.InputImageCount != 1 || imageIndex <= functionOutputIndex {
		return observation, fmt.Errorf("input_image count/order=%d/%d, want one projection after function output", observation.InputImageCount, imageIndex)
	}
	if observation.EncodedImageUses != 1 {
		return observation, fmt.Errorf("encoded image occurrence count=%d, want exactly once across provider frames", observation.EncodedImageUses)
	}
	if observation.BrowserID != expectedBrowserID || observation.TargetID != expectedTargetID {
		return observation, fmt.Errorf("show_page identity=(%q,%q), want selected (%q,%q)", observation.BrowserID, observation.TargetID, expectedBrowserID, expectedTargetID)
	}
	if len(responseCreateIndices) < 2 {
		return observation, fmt.Errorf("response.create count=%d, want an initial request and one continuation", len(responseCreateIndices))
	}
	continuations := make([]int, 0, 1)
	for _, responseIndex := range responseCreateIndices {
		if responseIndex > imageIndex {
			continuations = append(continuations, responseIndex)
		}
	}
	if len(continuations) != 1 {
		return observation, fmt.Errorf("continuation response.create count=%d, want exactly one after image projection=%d", len(continuations), imageIndex)
	}
	observation.ContinuationCount = 1
	continuationIndex = continuations[0]
	if len(responseDoneEvents) == 0 {
		return observation, errors.New("provider emitted no response.done")
	}
	for _, done := range responseDoneEvents {
		if done.Index <= continuationIndex {
			continue
		}
		if done.Status != "completed" {
			return observation, fmt.Errorf("continuation response.done status=%q, want completed", done.Status)
		}
		terminalIndex = done.Index
		observation.TerminalStatus = done.Status
		break
	}
	if terminalIndex < 0 {
		return observation, errors.New("no completed terminal response.done followed the continuation")
	}
	spokenTranscripts := make([]string, 0, len(transcripts))
	spokenAudioEvents := 0
	for _, event := range transcripts {
		if event.Index > continuationIndex && event.Index <= terminalIndex {
			spokenTranscripts = append(spokenTranscripts, event.Text)
		}
	}
	for _, eventIndex := range outputAudioIndices {
		if eventIndex > continuationIndex && eventIndex <= terminalIndex {
			spokenAudioEvents++
		}
	}
	if spokenAudioEvents == 0 || len(spokenTranscripts) == 0 {
		return observation, fmt.Errorf("spoken continuation audio events=%d transcript_count=%d, want non-empty spoken continuation", spokenAudioEvents, len(spokenTranscripts))
	}
	observation.SpokenTranscript = strings.Join(spokenTranscripts, " ")
	observation.SpokenFacts = cubecadeLiveVoiceFacts(observation.SpokenTranscript)
	if len(observation.SpokenFacts) < 2 {
		return observation, fmt.Errorf("spoken transcript=%q contains %d grounded visual facts, want at least two", observation.SpokenTranscript, len(observation.SpokenFacts))
	}
	if functionOutputIndex >= continuationIndex || imageIndex >= continuationIndex {
		return observation, errors.New("show_page result or image was not delivered before continuation")
	}
	if functionOutputIndex >= 0 && showArgumentsIndex >= 0 {
		observation.CaptureElapsedMS = capture.Records[functionOutputIndex].TimestampMs - capture.Records[showArgumentsIndex].TimestampMs
		if observation.CaptureElapsedMS < 0 {
			return observation, errors.New("show_page capture elapsed time is negative")
		}
		if observation.CaptureElapsedMS >= int64(cubecadeScreenshotBudget/time.Millisecond) {
			return observation, fmt.Errorf("show_page capture elapsed=%dms, want less than %s", observation.CaptureElapsedMS, cubecadeScreenshotBudget)
		}
	}
	return observation, nil
}

func cubecadeLiveVoiceFacts(transcript string) []string {
	normalized := strings.ToLower(transcript)
	facts := make([]string, 0, 3)
	if strings.Contains(normalized, "solved") || strings.Contains(normalized, "complete") {
		facts = append(facts, "solved_status")
	}
	if strings.Contains(normalized, "cube") || strings.Contains(normalized, "cubecade") {
		facts = append(facts, "cube_or_brand")
	}
	for _, color := range []string{"neon", "bright", "colored", "green", "pink", "blue", "yellow", "orange", "lime"} {
		if strings.Contains(normalized, color) {
			facts = append(facts, "visible_color")
			break
		}
	}
	return facts
}

func decodeLiveVoiceDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", nil, errors.New("image projection is not a base64 data URL")
	}
	mimeType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if mimeType == "" {
		return "", nil, errors.New("image projection has no MIME type")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, err
	}
	if len(decoded) == 0 {
		return "", nil, errors.New("image projection is empty")
	}
	return mimeType, decoded, nil
}

func countEncodedImageOccurrences(records []gatewaytesting.CapturedSessionEvent, encoded string) int {
	if encoded == "" {
		return 0
	}
	count := 0
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		count += strings.Count(string(payload), encoded)
	}
	return count
}

func assertCubecadeScreenshotMarkerNoFatal(screenshot image.Image, oracle cubecadeSightOracle) int {
	want, ok := parseCSSRGB(oracle.MarkerBackground)
	if !ok || oracle.ViewportWidth <= 0 || oracle.ViewportHeight <= 0 || oracle.MarkerRect.Width <= 0 || oracle.MarkerRect.Height <= 0 {
		return 0
	}
	bounds := screenshot.Bounds()
	scaleX := float64(bounds.Dx()) / oracle.ViewportWidth
	scaleY := float64(bounds.Dy()) / oracle.ViewportHeight
	left := maxInt(bounds.Min.X, bounds.Min.X+int(oracle.MarkerRect.X*scaleX))
	top := maxInt(bounds.Min.Y, bounds.Min.Y+int(oracle.MarkerRect.Y*scaleY))
	right := minInt(bounds.Max.X, bounds.Min.X+int((oracle.MarkerRect.X+oracle.MarkerRect.Width)*scaleX))
	bottom := minInt(bounds.Max.Y, bounds.Min.Y+int((oracle.MarkerRect.Y+oracle.MarkerRect.Height)*scaleY))
	if right <= left || bottom <= top {
		return 0
	}
	matches := 0
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			red, green, blue, _ := screenshot.At(x, y).RGBA()
			if closeScreenshotColor(uint8(red>>8), uint8(green>>8), uint8(blue>>8), want) {
				matches++
			}
		}
	}
	return matches
}

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
	contents := fmt.Sprintf(`browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
    allow_remote_cdp: false
  selection:
    browser: %q
    tab: %q
    auto_select: off
    activate_tab: false
    persist: false
  policy:
    allowed_origins:
      - %q
    approval: never
    cancel_on_interrupt: always
  limits:
    invocation_timeout: 20s
  recording:
    enabled: true
    include_arguments: true
    include_results: true
`, cdpURL, browserID, targetID, origin)
	return os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), cubecadeLiveVoiceEvidenceMode)
}

func cubecadeLiveVoiceArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(cubecadeLiveVoiceArtifactEnv))
	if parent == "" {
		parent = t.TempDir()
	} else if err := os.MkdirAll(parent, cubecadeLiveVoiceArtifactMode); err != nil {
		t.Fatalf("create spoken sight artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "webmcp-cubecade-spoken-sight-")
	if err != nil {
		t.Fatalf("create spoken sight artifact directory: %v", err)
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
