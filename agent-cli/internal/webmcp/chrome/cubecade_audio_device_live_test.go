//go:build live

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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
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
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("Cubecade audio-device proof uses the qualified %s Chrome lock; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	apiKey, keySource, err := loadGateI2APIKey()
	if errors.Is(err, errGateI2MissingAPIKey) {
		t.Skip("OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed Cubecade audio-device proof")
	}
	if err != nil {
		t.Fatalf("load OpenAI API key: %v", err)
	}

	artifactRoot := cubecadeAudioDeviceArtifactRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), cubecadeAudioDeviceTestTimeout)
	defer cancel()
	repository, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}

	workspaceDir := filepath.Join(artifactRoot, "workspace")
	configDir := filepath.Join(artifactRoot, "config")
	chromeDir := filepath.Join(artifactRoot, "chrome")
	for _, directory := range []string{workspaceDir, configDir, chromeDir} {
		if err := os.Mkdir(directory, cubecadeAudioDeviceArtifactMode); err != nil {
			t.Fatalf("create artifact directory %s: %v", filepath.Base(directory), err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "AGENTS.md"), []byte(cubecadeAudioDeviceAgentsMD), cubecadeAudioDeviceEvidenceMode); err != nil {
		t.Fatalf("write cube workspace AGENTS.md: %v", err)
	}
	agentBinary := filepath.Join(artifactRoot, "agent")
	deviceBinary := filepath.Join(artifactRoot, "audio-device-server")
	if err := buildGateBinary(ctx, repository, agentBinary); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}
	if err := buildCubecadeAudioDeviceServer(ctx, repository, deviceBinary); err != nil {
		t.Fatalf("build production audio-device server: %v", err)
	}

	pinned, err := acquirePinnedChrome(ctx, chromeDir)
	if err != nil {
		t.Fatalf("acquire qualified Chrome for Testing: %v", err)
	}
	launchStartedAt := time.Now()
	browser, err := launchCubecadeAudioChrome(ctx, pinned, cubecadeURL)
	if err != nil {
		t.Fatalf("launch qualified Chrome for Testing: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = browser.Close()
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(ctx, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read qualified Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForCubecadePageTarget(ctx, baseURL)
	if err != nil {
		t.Fatalf("discover exact Cubecade target: %v", err)
	}
	browserID, targetID, err := gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID)
	if err != nil {
		t.Fatalf("derive public browser and target IDs: %v", err)
	}
	cdpURL := strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeCubecadeAudioDeviceConfig(configDir, cdpURL, cubecadeOrigin, browserID, targetID); err != nil {
		t.Fatalf("write exact browser config: %v", err)
	}
	endpoint, stopDevice := startCubecadeAudioDeviceServer(t, ctx, deviceBinary)
	defer stopDevice()
	if err := waitUntilCubecade(ctx, launchStartedAt.Add(cubecadeLaunchDelay)); err != nil {
		t.Fatalf("wait for qualified Cubecade attachment offset: %v", err)
	}

	capturePath := filepath.Join(artifactRoot, "provider.json")
	recordDir := filepath.Join(artifactRoot, "recording")
	args := []string{
		"--workdir", workspaceDir,
		"session",
		"--provider", "openai",
		"--model", cubecadeAudioDeviceModel,
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-browser", browserID,
		"--browser-tab", targetID,
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

	runCtx, cancelRun := context.WithTimeout(ctx, cubecadeAudioDeviceMaxDuration+20*time.Second)
	process, err := startGateCommandWithEnvironment(runCtx, agentBinary, configDir, []string{"AGENT_MODEL__OPENAI__API_KEY=" + apiKey}, args...)
	if err != nil {
		cancelRun()
		t.Fatalf("start Cubecade audio-device session: %v", err)
	}
	result, waitErr := process.wait(runCtx)
	cancelRun()
	if waitErr != nil {
		t.Fatalf("wait for Cubecade audio-device session: %v", waitErr)
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
	finalOracle, err := inspectCubecadeTarget(ctx, browser.endpoint(), rawTarget.ID)
	if err != nil {
		t.Fatalf("inspect final Cubecade oracle: %v", err)
	}
	if !finalOracle.Solved || !strings.Contains(finalOracle.State, "solved: true") {
		t.Fatalf("final Cubecade board is not solved: %+v", finalOracle)
	}
	if got := strings.Count(finalOracle.Terminal, "$ queue_cube_moves [3]"); got != 2 {
		t.Fatalf("page queue log count=%d, want exact-position and restore calls; terminal=%q", got, finalOracle.Terminal)
	}

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

	closed = true
	if err := browser.Close(); err != nil {
		t.Logf("Cubecade Chrome cleanup returned: %v", err)
	}
	t.Logf("WEBMCP_CUBECADE_AUDIO_DEVICE_PASS model=%s key_source=%s chrome=%s browser=%s target=%s queue_calls=2 state_reads=%d rendered_samples=%d callbacks=%d transcript=%q artifacts=%s", capture.Provider.Model, keySource, pinned.Lock.Version, browserID, targetID, observation.StateReads, len(snapshot.RenderedSamples), snapshot.Playback.CallbackCount, observation.Transcript, artifactRoot)
}

type cubecadeAudioDeviceObservation struct {
	StateReads int
	Transcript string
}

func inspectCubecadeAudioDeviceCapture(capture gatewaytesting.SessionCapture) (cubecadeAudioDeviceObservation, error) {
	observation := cubecadeAudioDeviceObservation{}
	if capture.Provider.Name != "openai" || capture.Provider.Model != cubecadeAudioDeviceModel {
		return observation, fmt.Errorf("provider=(%q,%q), want (openai,%q)", capture.Provider.Name, capture.Provider.Model, cubecadeAudioDeviceModel)
	}
	queueInputs := make([]string, 0, 2)
	transcripts := make([]string, 0, 2)
	pageTools := make(map[string]string)
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			return observation, fmt.Errorf("capture record %d (%s) is empty", index, record.Type)
		}
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "conversation.item.create" {
			var event struct {
				Item struct {
					Type   string `json:"type"`
					Output string `json:"output"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode function output item: %w", err)
			}
			if event.Item.Type != "function_call_output" {
				continue
			}
			envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
			if err != nil || !envelope.OK {
				continue
			}
			var catalog struct {
				Tools []struct {
					Name string `json:"name"`
					Ref  string `json:"ref"`
				} `json:"tools"`
			}
			if json.Unmarshal(envelope.Data, &catalog) == nil {
				for _, tool := range catalog.Tools {
					pageTools[tool.Ref] = tool.Name
				}
			}
			continue
		}
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.function_call_arguments.done":
			var event struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode function arguments: %w", err)
			}
			toolName := event.Name
			inputJSON := event.Arguments
			if event.Name == webmcp.InvokeToolName {
				var brokerArgs struct {
					ToolRef   string `json:"tool_ref"`
					InputJSON string `json:"input_json"`
				}
				if err := json.Unmarshal([]byte(event.Arguments), &brokerArgs); err != nil {
					return observation, fmt.Errorf("decode webmcp_invoke arguments: %w", err)
				}
				toolName = pageTools[brokerArgs.ToolRef]
				if toolName == "" {
					return observation, fmt.Errorf("webmcp_invoke used unknown catalog ref %q", brokerArgs.ToolRef)
				}
				inputJSON = brokerArgs.InputJSON
			} else if event.Name == webmcp.ShowPageToolName {
				return observation, errors.New("model called show_page instead of the structured cube-state tools")
			} else if event.Name != "get_cube_state" && event.Name != "queue_cube_moves" {
				continue
			}
			var pageInput struct {
				Moves []string `json:"moves"`
			}
			if err := json.Unmarshal([]byte(inputJSON), &pageInput); err != nil {
				return observation, fmt.Errorf("decode page input %q: %w", inputJSON, err)
			}
			switch toolName {
			case "queue_cube_moves":
				if len(pageInput.Moves) == 0 {
					return observation, errors.New("queue_cube_moves received no moves")
				}
				encoded, _ := json.Marshal(pageInput.Moves)
				queueInputs = append(queueInputs, string(encoded))
			case "get_cube_state":
				var stateInput map[string]json.RawMessage
				if err := json.Unmarshal([]byte(inputJSON), &stateInput); err != nil || len(stateInput) != 0 {
					return observation, fmt.Errorf("get_cube_state input=%q, want empty object", inputJSON)
				}
				observation.StateReads++
			default:
				return observation, fmt.Errorf("unexpected page tool %q in cube protocol", toolName)
			}
		case "response.output_audio_transcript.done":
			var event struct {
				Transcript string `json:"transcript"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode output audio transcript: %w", err)
			}
			if text := strings.TrimSpace(event.Transcript); text != "" {
				transcripts = append(transcripts, text)
			}
		case "error":
			return observation, fmt.Errorf("provider emitted error at record %d", index)
		}
	}
	if got, want := strings.Join(queueInputs, " then "), `["U","R2","F'"] then ["F","R2","U'"]`; got != want {
		return observation, fmt.Errorf("page move inputs=%s, want %s", got, want)
	}
	if observation.StateReads < 3 {
		return observation, fmt.Errorf("get_cube_state reads=%d, want initial, positioned, and solved verification", observation.StateReads)
	}
	observation.Transcript = strings.Join(transcripts, " ")
	if observation.Transcript == "" {
		return observation, errors.New("provider emitted no spoken transcript")
	}
	if cubecadeRawNotationPattern.MatchString(observation.Transcript) || cubecadeFaceletDumpPattern.MatchString(observation.Transcript) {
		return observation, fmt.Errorf("spoken transcript leaks raw cube notation or facelets: %q", observation.Transcript)
	}
	normalized := strings.ToLower(observation.Transcript)
	colorMentions := 0
	for _, color := range []string{"white", "red", "green", "yellow", "orange", "blue"} {
		if strings.Contains(normalized, color) {
			colorMentions++
		}
	}
	if (colorMentions == 0 && !strings.Contains(normalized, "color face")) || (!strings.Contains(normalized, "align") && !strings.Contains(normalized, "mixed")) || !strings.Contains(normalized, "solv") {
		return observation, fmt.Errorf("spoken transcript lacks a color/alignment summary: %q", observation.Transcript)
	}
	for _, forbidden := range []string{"sticker", "facelet", "center", "edge", "corner", "position 1", "position 2", "position 3", "position 4", "position 5", "position 6", "position 7", "position 8", "position 9"} {
		if strings.Contains(normalized, forbidden) {
			return observation, fmt.Errorf("spoken transcript describes per-element cube state (%q): %q", forbidden, observation.Transcript)
		}
	}
	if words := len(strings.Fields(observation.Transcript)); words > 80 {
		return observation, fmt.Errorf("spoken transcript has %d words, want at most 80: %q", words, observation.Transcript)
	}
	restorationIndex := strings.Index(normalized, "after restoration")
	if restorationIndex < 0 {
		return observation, fmt.Errorf("spoken transcript does not separate test and restored states: %q", observation.Transcript)
	}
	testSummary, restoredSummary := normalized[:restorationIndex], normalized[restorationIndex:]
	if !strings.Contains(testSummary, "all six") || !strings.Contains(testSummary, "mixed") || strings.Contains(testSummary, "aligned") || strings.Contains(testSummary, "solved") {
		return observation, fmt.Errorf("test-position summary is not the observed all-six-faces-mixed state: %q", observation.Transcript)
	}
	if !strings.Contains(restoredSummary, "all six") || !strings.Contains(restoredSummary, "aligned") || !strings.Contains(restoredSummary, "solved") || strings.Contains(restoredSummary, "mixed") {
		return observation, fmt.Errorf("restoration summary is not the observed aligned solved state: %q", observation.Transcript)
	}
	return observation, nil
}

func waitForCubecadeAudioDeviceDrain(ctx context.Context, endpoint string) (audio.DeviceServerSnapshot, error) {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var latest audio.DeviceServerSnapshot
	for {
		snapshot, err := audio.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
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
		_ = running.Close()
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
	ready := make(chan []byte, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadBytes('\n')
		ready <- line
	}()
	var line []byte
	select {
	case line = <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		_ = command.Wait()
		t.Fatalf("audio-device server did not become ready; stderr=%q", stderr.String())
	}
	var announcement struct {
		Endpoint string `json:"endpoint"`
		Input    string `json:"input_device"`
		Output   string `json:"output_device"`
	}
	if err := json.Unmarshal(line, &announcement); err != nil {
		cancel()
		_ = command.Wait()
		t.Fatalf("decode audio-device server announcement %q: %v", line, err)
	}
	if announcement.Endpoint == "" || announcement.Input == "" || announcement.Output == "" {
		cancel()
		_ = command.Wait()
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
	parent := strings.TrimSpace(os.Getenv(cubecadeArtifactEnv))
	if parent == "" {
		parent = t.TempDir()
	} else if err := os.MkdirAll(parent, cubecadeAudioDeviceArtifactMode); err != nil {
		t.Fatalf("create Cubecade artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "webmcp-cubecade-audio-device-")
	if err != nil {
		t.Fatalf("create Cubecade audio-device artifact directory: %v", err)
	}
	return root
}
