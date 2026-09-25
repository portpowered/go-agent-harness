//go:build e2e_internal

package chrome

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	cubecadeAudioDeviceLiveEnv         = "WEBMCP_CUBECADE_AUDIO_DEVICE_LIVE"
	cubecadeAudioDeviceModel           = "gpt-realtime"
	cubecadeAudioDevicePrompt          = "Use the cube page's structured WebMCP state tools, not a screenshot. An initial get_cube_state call before any move is mandatory; verify the cube starts solved with an empty queue. Put it in this exact test position: turn the white face clockwise, the red face twice, and the green face counterclockwise. Wait for the board to settle and verify it. Then restore the cube to solved. A third get_cube_state call after the restoring moves is mandatory; wait for an empty queue and verify solved is true. Only then give the workspace's two-clause final summary; do not describe individual stickers, centers, edges, corners, or positions."
	cubecadeAudioDeviceMaxDuration     = 45 * time.Second
	cubecadeAudioDeviceTestTimeout     = 5 * time.Minute
	cubecadeAudioDeviceArtifactMode    = 0o700
	cubecadeAudioDeviceEvidenceMode    = 0o600
	cubecadeAudioDeviceRenderedMinimum = 1600
)

var cubecadeRawNotationPattern = regexp.MustCompile(`(?i)(^|[^[:alnum:]])[URFDLB](?:2|')?([^[:alnum:]]|$)`)
var cubecadeFaceletDumpPattern = regexp.MustCompile(`(?i)(^|[^[:alnum:]])[URFDLB]{9}([^[:alnum:]]|$)`)

// TestPinnedChromeCubecadeAgentUsesAudioDeviceServer is an opt-in, billed,
// outside-in proof. It runs both shipped binaries against the real Cubecade
// deployment and OpenAI Realtime; assertions use only provider capture, the
// browser DOM, and the remote audio-device server's public control endpoint.
func TestPinnedChromeCubecadeAgentUsesAudioDeviceServer(t *testing.T) {
	if os.Getenv(cubecadeAudioDeviceLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the billed Cubecade audio-device proof", cubecadeAudioDeviceLiveEnv)
	}
	if runtime.GOOS != goosDarwin || runtime.GOARCH != goarchARM64 {
		t.Skipf("Cubecade audio-device proof uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}
	apiKey, keySource := requireLiveOpenAIKey(t, "OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed Cubecade audio-device proof")

	artifactRoot := cubecadeAudioDeviceArtifactRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), cubecadeAudioDeviceTestTimeout)
	defer cancel()
	proof := prepareCubecadeAudioDeviceProof(t, ctx, artifactRoot)
	endpoint, stopDevice := startCubecadeAudioDeviceServer(t, ctx, proof.deviceBinary)
	defer stopDevice()
	if err := waitUntilCubecade(ctx, proof.launchStartedAt.Add(cubecadeLaunchDelay)); err != nil {
		t.Fatalf("wait for qualified Cubecade attachment offset: %v", err)
	}

	capturePath := filepath.Join(artifactRoot, "provider.json")
	result, err := runLiveAgentSession(ctx, cubecadeAudioDeviceMaxDuration+20*time.Second, proof.agentBinary, proof.configDir, apiKey, proof.sessionArgs(capturePath, filepath.Join(artifactRoot, "recording"), endpoint))
	var startErr liveSessionStartError
	if errors.As(err, &startErr) {
		t.Fatalf("start Cubecade audio-device session: %v", startErr.cause)
	}
	if err != nil {
		t.Fatalf("wait for Cubecade audio-device session: %v", err)
	}
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("Cubecade session exit=%d err=%v stdout=%q stderr=%q", result.ExitCode, result.Err, result.Stdout, result.Stderr)
	}

	capture, err := gatewaytesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load provider capture: %v", err)
	}
	observation, err := inspectCubecadeAudioDeviceCapture(capture)
	if err != nil {
		t.Fatalf("inspect Cubecade provider capture: %v", err)
	}
	proof.assertFinalBoard(t, ctx)
	snapshot := assertCubecadeAudioDevicePlayback(t, ctx, endpoint)

	if err := proof.browser.close(); err != nil {
		t.Logf("Cubecade Chrome cleanup returned: %v", err)
	}
	t.Logf("WEBMCP_CUBECADE_AUDIO_DEVICE_PASS model=%s key_source=%s chrome=%s browser=%s target=%s queue_calls=2 state_reads=%d rendered_samples=%d callbacks=%d transcript=%q artifacts=%s", capture.Provider.Model, keySource, proof.pinned.Lock.Version, proof.browserID, proof.targetID, observation.StateReads, len(snapshot.RenderedSamples), snapshot.Playback.CallbackCount, observation.Transcript, artifactRoot)
}

// cubecadeAudioDeviceProof holds the binaries, pinned Chrome, and exact target.
type cubecadeAudioDeviceProof struct {
	workspaceDir    string
	configDir       string
	agentBinary     string
	deviceBinary    string
	pinned          pinnedChrome
	browser         *liveBrowserOwner
	launchStartedAt time.Time
	rawTargetID     string
	browserID       string
	targetID        string
	cdpURL          string
}

func prepareCubecadeAudioDeviceProof(t *testing.T, ctx context.Context, artifactRoot string) *cubecadeAudioDeviceProof {
	t.Helper()
	repository, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	proof := &cubecadeAudioDeviceProof{
		workspaceDir: filepath.Join(artifactRoot, "workspace"),
		configDir:    filepath.Join(artifactRoot, "config"),
		agentBinary:  filepath.Join(artifactRoot, "agent"),
		deviceBinary: filepath.Join(artifactRoot, "audio-device-server"),
	}
	chromeDir := filepath.Join(artifactRoot, "chrome")
	for _, directory := range []string{proof.workspaceDir, proof.configDir, chromeDir} {
		if err := os.Mkdir(directory, cubecadeAudioDeviceArtifactMode); err != nil {
			t.Fatalf("create artifact directory %s: %v", filepath.Base(directory), err)
		}
	}
	if err := os.WriteFile(filepath.Join(proof.workspaceDir, "AGENTS.md"), []byte(cubecadeAudioDeviceAgentsMD), cubecadeAudioDeviceEvidenceMode); err != nil {
		t.Fatalf("write cube workspace AGENTS.md: %v", err)
	}
	if err := buildGateBinary(ctx, repository, proof.agentBinary); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	if err := buildCubecadeAudioDeviceServer(ctx, repository, proof.deviceBinary); err != nil {
		t.Fatalf("build production audio-device server: %v", err)
	}
	if proof.pinned, err = acquirePinnedChrome(ctx, chromeDir); err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}
	proof.startChrome(t, ctx)
	return proof
}

func (p *cubecadeAudioDeviceProof) startChrome(t *testing.T, ctx context.Context) {
	t.Helper()
	p.launchStartedAt = time.Now()
	browser, err := launchCubecadeAudioChrome(ctx, p.pinned, cubecadeURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	p.browser = ownLiveBrowser(t, browser, "Cubecade Chrome cleanup")
	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read qualified Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForCubecadePageTarget(ctx, baseURL)
	if err != nil {
		t.Fatalf("discover exact Cubecade target: %v", err)
	}
	p.rawTargetID = rawTarget.ID
	if p.browserID, p.targetID, err = gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID); err != nil {
		t.Fatalf("derive public browser and target IDs: %v", err)
	}
	p.cdpURL = strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeCubecadeAudioDeviceConfig(p.configDir, p.cdpURL, cubecadeOrigin, p.browserID, p.targetID); err != nil {
		t.Fatalf("write exact browser config: %v", err)
	}
}

func (p *cubecadeAudioDeviceProof) sessionArgs(capturePath, recordDir, endpoint string) []string {
	return []string{
		"--workdir", p.workspaceDir,
		"session",
		"--provider", liveProviderOpenAI,
		"--model", cubecadeAudioDeviceModel,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", p.cdpURL,
		"--browser-browser", p.browserID,
		"--browser-tab", p.targetID,
		"--browser-origin", cubecadeOrigin,
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "always",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--audio-device-server", endpoint,
		"--audio-out-device=",
		"--prompt", cubecadeAudioDevicePrompt,
		"--max-duration", cubecadeAudioDeviceMaxDuration.String(),
	}
}

// assertFinalBoard checks the DOM oracle shows a solved board after two queues.
func (p *cubecadeAudioDeviceProof) assertFinalBoard(t *testing.T, ctx context.Context) {
	t.Helper()
	finalOracle, err := inspectCubecadeTarget(ctx, p.browser.browser.endpoint(), p.rawTargetID)
	if err != nil {
		t.Fatalf("inspect final Cubecade oracle: %v", err)
	}
	if !finalOracle.Solved || !strings.Contains(finalOracle.State, "solved: true") {
		t.Fatalf("final Cubecade board is not solved: %+v", finalOracle)
	}
	if got := strings.Count(finalOracle.Terminal, "$ queue_cube_moves [3]"); got != 2 {
		t.Fatalf("page queue log count=%d, want exact-position and restore calls; terminal=%q", got, finalOracle.Terminal)
	}
}

// assertCubecadeAudioDevicePlayback checks lossless 16 kHz spoken playback.
func assertCubecadeAudioDevicePlayback(t *testing.T, ctx context.Context, endpoint string) devicegw.DeviceServerSnapshot {
	t.Helper()
	snapshot, err := waitForCubecadeAudioDeviceDrain(ctx, endpoint)
	if err != nil {
		t.Fatalf("read audio-device server evidence: %v", err)
	}
	if len(snapshot.RenderedSamples) < cubecadeAudioDeviceRenderedMinimum || snapshot.Playback.RenderedSamples < cubecadeAudioDeviceRenderedMinimum {
		t.Fatalf("rendered audio samples=%d stats=%+v, want at least %d", len(snapshot.RenderedSamples), snapshot.Playback, cubecadeAudioDeviceRenderedMinimum)
	}
	if snapshot.Playback.DroppedSamples != 0 || snapshot.Playback.OverflowEvents != 0 || snapshot.Playback.QueuedSamples != 0 {
		t.Fatalf("audio playback did not drain losslessly: %+v", snapshot.Playback)
	}
	if snapshot.Playback.Format.SampleRate != 16000 || snapshot.Playback.CallbackCount == 0 {
		t.Fatalf("audio device format/callbacks=%+v, want active 16 kHz output", snapshot.Playback)
	}
	return snapshot
}

type cubecadeAudioDeviceObservation struct {
	StateReads int
	Transcript string
}

// cubecadeAudioDeviceInspector accumulates one capture's page-tool facts.
type cubecadeAudioDeviceInspector struct {
	observation cubecadeAudioDeviceObservation
	queueInputs []string
	transcripts []string
	pageTools   map[string]string
}

func inspectCubecadeAudioDeviceCapture(capture gatewaytesting.SessionCapture) (cubecadeAudioDeviceObservation, error) {
	inspector := &cubecadeAudioDeviceInspector{queueInputs: make([]string, 0, 2), transcripts: make([]string, 0, 2), pageTools: make(map[string]string)}
	if capture.Provider.Name != liveProviderOpenAI || capture.Provider.Model != cubecadeAudioDeviceModel {
		return inspector.observation, fmt.Errorf("provider=(%q,%q), want (openai,%q)", capture.Provider.Name, capture.Provider.Model, cubecadeAudioDeviceModel)
	}
	for index, record := range capture.Records {
		payload := liveRecordPayload(record)
		if len(payload) == 0 {
			return inspector.observation, fmt.Errorf("capture record %d (%s) is empty", index, record.Type)
		}
		if err := inspector.observe(index, record, payload); err != nil {
			return inspector.observation, err
		}
	}
	if got, want := strings.Join(inspector.queueInputs, " then "), `["U","R2","F'"] then ["F","R2","U'"]`; got != want {
		return inspector.observation, fmt.Errorf("page move inputs=%s, want %s", got, want)
	}
	if inspector.observation.StateReads < 3 {
		return inspector.observation, fmt.Errorf("get_cube_state reads=%d, want initial, positioned, and solved verification", inspector.observation.StateReads)
	}
	inspector.observation.Transcript = strings.Join(inspector.transcripts, " ")
	return inspector.observation, validateCubecadeAudioDeviceTranscript(inspector.observation.Transcript)
}

func (i *cubecadeAudioDeviceInspector) observe(index int, record gatewaytesting.CapturedSessionEvent, payload []byte) error {
	if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == liveEventConversationItemCreate {
		return i.observeCatalog(payload)
	}
	if record.Direction != gatewaytesting.DirectionServerToClient {
		return nil
	}
	switch record.Type {
	case liveEventFunctionCallArgumentsDone:
		return i.observeToolArguments(payload)
	case liveEventOutputAudioTranscriptDone:
		var event struct {
			Transcript string `json:"transcript"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode output audio transcript: %w", err)
		}
		if text := strings.TrimSpace(event.Transcript); text != "" {
			i.transcripts = append(i.transcripts, text)
		}
	case liveEventProviderError:
		return fmt.Errorf("provider emitted error at record %d", index)
	}
	return nil
}

// observeCatalog maps successful catalogs' refs to page tool names; other
// tool results are ignored.
func (i *cubecadeAudioDeviceInspector) observeCatalog(payload []byte) error {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode function output item: %w", err)
	}
	if event.Item.Type != liveItemFunctionCallOutput {
		return nil
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
	if err == nil && envelope.OK {
		var catalog struct {
			Tools []struct {
				Name string `json:"name"`
				Ref  string `json:"ref"`
			} `json:"tools"`
		}
		if json.Unmarshal(envelope.Data, &catalog) == nil {
			for _, tool := range catalog.Tools {
				i.pageTools[tool.Ref] = tool.Name
			}
		}
	}
	return nil
}

func (i *cubecadeAudioDeviceInspector) observeToolArguments(payload []byte) error {
	var event struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode function arguments: %w", err)
	}
	toolName, inputJSON := event.Name, event.Arguments
	switch event.Name {
	case webmcp.InvokeToolName:
		var brokerArgs struct {
			ToolRef   string `json:"tool_ref"`
			InputJSON string `json:"input_json"`
		}
		if err := json.Unmarshal([]byte(event.Arguments), &brokerArgs); err != nil {
			return fmt.Errorf("decode webmcp_invoke arguments: %w", err)
		}
		toolName = i.pageTools[brokerArgs.ToolRef]
		if toolName == "" {
			return fmt.Errorf("webmcp_invoke used unknown catalog ref %q", brokerArgs.ToolRef)
		}
		inputJSON = brokerArgs.InputJSON
	case webmcp.ShowPageToolName:
		return errors.New("model called show_page instead of the structured cube-state tools")
	case cubecadeSharedBrowserStateTool, cubecadeSharedBrowserQueueTool:
	default:
		return nil
	}
	return i.observePageInput(toolName, inputJSON)
}

func (i *cubecadeAudioDeviceInspector) observePageInput(toolName, inputJSON string) error {
	var pageInput struct {
		Moves []string `json:"moves"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &pageInput); err != nil {
		return fmt.Errorf("decode page input %q: %w", inputJSON, err)
	}
	switch toolName {
	case cubecadeSharedBrowserQueueTool:
		if len(pageInput.Moves) == 0 {
			return errors.New("queue_cube_moves received no moves")
		}
		encoded, err := json.Marshal(pageInput.Moves)
		if err != nil {
			return fmt.Errorf("encode queue_cube_moves input: %w", err)
		}
		i.queueInputs = append(i.queueInputs, string(encoded))
	case cubecadeSharedBrowserStateTool:
		var stateInput map[string]json.RawMessage
		if err := json.Unmarshal([]byte(inputJSON), &stateInput); err != nil || len(stateInput) != 0 {
			return fmt.Errorf("get_cube_state input=%q, want empty object", inputJSON)
		}
		i.observation.StateReads++
	default:
		return fmt.Errorf("unexpected page tool %q in cube protocol", toolName)
	}
	return nil
}

// validateCubecadeAudioDeviceTranscript enforces the alignment-level summary.
func validateCubecadeAudioDeviceTranscript(transcript string) error {
	if transcript == "" {
		return errors.New("provider emitted no spoken transcript")
	}
	if cubecadeRawNotationPattern.MatchString(transcript) || cubecadeFaceletDumpPattern.MatchString(transcript) {
		return fmt.Errorf("spoken transcript leaks raw cube notation or facelets: %q", transcript)
	}
	normalized := strings.ToLower(transcript)
	colorMentions := 0
	for _, color := range []string{"white", "red", "green", "yellow", "orange", "blue"} {
		if strings.Contains(normalized, color) {
			colorMentions++
		}
	}
	if (colorMentions == 0 && !strings.Contains(normalized, "color face")) || (!strings.Contains(normalized, "align") && !strings.Contains(normalized, "mixed")) || !strings.Contains(normalized, "solv") {
		return fmt.Errorf("spoken transcript lacks a color/alignment summary: %q", transcript)
	}
	for _, forbidden := range []string{"sticker", "facelet", "center", "edge", "corner", "position 1", "position 2", "position 3", "position 4", "position 5", "position 6", "position 7", "position 8", "position 9"} {
		if strings.Contains(normalized, forbidden) {
			return fmt.Errorf("spoken transcript describes per-element cube state (%q): %q", forbidden, transcript)
		}
	}
	if words := len(strings.Fields(transcript)); words > 80 {
		return fmt.Errorf("spoken transcript has %d words, want at most 80: %q", words, transcript)
	}
	return validateCubecadeAudioDeviceSummaries(transcript, normalized)
}

// validateCubecadeAudioDeviceSummaries checks the test and restored clauses.
func validateCubecadeAudioDeviceSummaries(transcript, normalized string) error {
	restorationIndex := strings.Index(normalized, "after restoration")
	if restorationIndex < 0 {
		return fmt.Errorf("spoken transcript does not separate test and restored states: %q", transcript)
	}
	testSummary, restoredSummary := normalized[:restorationIndex], normalized[restorationIndex:]
	if !strings.Contains(testSummary, "all six") || !strings.Contains(testSummary, "mixed") || strings.Contains(testSummary, "aligned") || strings.Contains(testSummary, "solved") {
		return fmt.Errorf("test-position summary is not the observed all-six-faces-mixed state: %q", transcript)
	}
	if !strings.Contains(restoredSummary, "all six") || !strings.Contains(restoredSummary, "aligned") || !strings.Contains(restoredSummary, "solved") || strings.Contains(restoredSummary, "mixed") {
		return fmt.Errorf("restoration summary is not the observed aligned solved state: %q", transcript)
	}
	return nil
}

func waitForCubecadeAudioDeviceDrain(ctx context.Context, endpoint string) (devicegw.DeviceServerSnapshot, error) {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var latest devicegw.DeviceServerSnapshot
	for {
		snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
		if err != nil {
			return latest, err
		}
		latest = snapshot
		if snapshot.Playback.QueuedSamples == 0 && snapshot.Playback.RenderedSamples > 0 {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		case <-deadline.C:
			return latest, fmt.Errorf("audio playback did not drain: %+v", latest.Playback)
		case <-ticker.C:
		}
	}
}

func buildCubecadeAudioDeviceServer(ctx context.Context, repository, destination string) error {
	command := exec.CommandContext(ctx, "go", "build", "-o", destination, "./cmd/audio-device-server")
	command.Dir = filepath.Join(repository, "agent-cli")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build audio-device-server: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writeCubecadeAudioDeviceConfig(configDir, cdpURL, origin, browserID, targetID string) error {
	if err := writeCubecadeLiveVoiceConfig(configDir, cdpURL, origin, browserID, targetID); err != nil {
		return err
	}
	path := filepath.Join(configDir, "config.yaml")
	browserConfig, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read generated browser config: %w", err)
	}
	// Keep sleep for deterministic animation settling; page and stable WebMCP
	// tools are composed separately. Every unrelated default tool is disabled
	// so each billed continuation does not resend irrelevant schemas.
	toolConfig := `tools:
  list:
    - {id: exec, enabled: false}
    - {id: read_file, enabled: false}
    - {id: read_image, enabled: false}
    - {id: write_file, enabled: false}
    - {id: edit_file, enabled: false}
    - {id: append_file, enabled: false}
    - {id: list_dir, enabled: false}
    - {id: web_fetch, enabled: false}
    - {id: web_search, enabled: false}
    - {id: show, enabled: false}
    - {id: mouse, enabled: false}
    - {id: load_skill, enabled: false}
    - {id: sleep, enabled: true}
`
	return os.WriteFile(path, append([]byte(toolConfig), browserConfig...), cubecadeAudioDeviceEvidenceMode)
}

func launchCubecadeAudioChrome(ctx context.Context, pinned pinnedChrome, pageURL string) (*runningChrome, error) {
	profileDir := filepath.Join(pinned.WorkDir, "profile")
	if err := os.Mkdir(profileDir, cubecadeAudioDeviceArtifactMode); err != nil {
		return nil, fmt.Errorf("create isolated Cubecade Chrome profile: %w", err)
	}
	baseArgs := pinnedChromeLaunchFlags(profileDir, pageURL, 0)
	args := make([]string, 0, len(baseArgs)+3)
	for _, argument := range baseArgs {
		if argument != "--disable-gpu" {
			args = append(args, argument)
		}
	}
	args = append(args, "--enable-webgl", "--use-angle=swiftshader", "--enable-unsafe-swiftshader")
	command := exec.CommandContext(ctx, pinned.Executable, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Cubecade Chrome stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture Cubecade Chrome stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Cubecade Chrome: %w", err)
	}
	running := &runningChrome{cmd: command, done: make(chan struct{})}
	go func() {
		running.waitErr = command.Wait()
		close(running.done)
	}()
	endpoint := make(chan string, 1)
	var stdoutLog, stderrLog bytes.Buffer
	go scanChromeEndpoint(io.TeeReader(stdout, &stdoutLog), endpoint)
	go scanChromeEndpoint(io.TeeReader(stderr, &stderrLog), endpoint)
	select {
	case value := <-endpoint:
		running.setEndpoint(value)
		return running, nil
	case <-running.done:
		return nil, fmt.Errorf("Cubecade Chrome exited before DevTools: %v (stdout=%q stderr=%q)", running.waitErr, strings.TrimSpace(stdoutLog.String()), strings.TrimSpace(stderrLog.String()))
	case <-ctx.Done():
		discardSecondaryError(running.Close)
		return nil, fmt.Errorf("wait for Cubecade Chrome DevTools: %w", ctx.Err())
	}
}

func startCubecadeAudioDeviceServer(t *testing.T, parent context.Context, binary string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	command := exec.CommandContext(ctx, binary, "--listen", "127.0.0.1:0", "--sample-rate", "16000", "--render-quantum", "480", "--capture-quantum", "480")
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("open audio-device server stdout: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start audio-device server: %v", err)
	}
	type announcementRead struct {
		line []byte
		err  error
	}
	ready := make(chan announcementRead, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadBytes('\n')
		ready <- announcementRead{line: line, err: readErr}
	}()
	var read announcementRead
	select {
	case read = <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		discardSecondaryError(command.Wait)
		t.Fatalf("audio-device server did not become ready; stderr=%q", stderr.String())
	}
	var announcement struct {
		Endpoint string `json:"endpoint"`
		Input    string `json:"input_device"`
		Output   string `json:"output_device"`
	}
	if err := json.Unmarshal(read.line, &announcement); err != nil {
		cancel()
		discardSecondaryError(command.Wait)
		t.Fatalf("decode audio-device server announcement %q (read error: %v): %v", read.line, read.err, err)
	}
	if announcement.Endpoint == "" || announcement.Input == "" || announcement.Output == "" {
		cancel()
		discardSecondaryError(command.Wait)
		t.Fatalf("incomplete audio-device server announcement: %+v", announcement)
	}
	return announcement.Endpoint, func() {
		cancel()
		if err := command.Wait(); err != nil && parent.Err() == nil && ctx.Err() == nil {
			t.Errorf("wait for audio-device server: %v; stderr=%q", err, stderr.String())
		}
	}
}

func cubecadeAudioDeviceArtifactRoot(t *testing.T) string {
	t.Helper()
	return cubecadeNestedArtifactRoot(t, "webmcp-cubecade-audio-device-", "Cubecade audio-device")
}
