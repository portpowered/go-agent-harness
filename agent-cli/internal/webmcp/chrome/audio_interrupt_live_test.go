//go:build live

package chrome

// This is the opt-in stock-Chrome/production-CLI proof for the scheduled
// audio interrupt path. It intentionally keeps provider traffic live only
// when the operator asks for it; the ordinary hermetic regression lives in
// agent-cli/internal/transport/cli/session_audio_interrupt_integration_test.go.

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	audioInterruptLiveEnv     = "WEBMCP_AUDIO_INTERRUPT_LIVE"
	audioInterruptArtifactEnv = "WEBMCP_AUDIO_INTERRUPT_ARTIFACT_DIR"
	audioInterruptModel       = "gpt-realtime-2.1-mini"
	audioInterruptTool        = "queue_cube_moves"
	audioInterruptMoves       = "R U F L' D B2"
	audioInterruptMaxDuration = 30 * time.Second

	audioInterruptWindowMinimum = 250 * time.Millisecond
	audioInterruptWindowMaximum = 2 * time.Second
	audioInterruptTimingSlack   = 15 * time.Millisecond
)

//go:embed testdata/audio_interrupt.html
var audioInterruptFixtureHTML []byte

type audioInterruptScenario struct {
	name       string
	named      bool
	request    string
	commandTag string
}

type audioInterruptFixtureServer struct {
	server *httptest.Server

	mu     sync.Mutex
	oracle fixtureOracle
}

type audioInterruptWireEvent struct {
	index      int
	timestamp  time.Time
	sequence   int
	eventType  string
	audioBytes int
}

type audioInterruptWireObservation struct {
	provider    string
	model       string
	startedAt   time.Time
	commits     []audioInterruptWireEvent
	appends     []audioInterruptWireEvent
	providerErr int
}

type audioInterruptEvidence struct {
	Schema          string `json:"schema"`
	ObservedAtUTC   string `json:"observed_at_utc"`
	Build           string `json:"build"`
	ChromeChannel   string `json:"chrome_channel"`
	ChromePlatform  string `json:"chrome_platform"`
	ChromeVersion   string `json:"chrome_version"`
	ChromeRevision  string `json:"chrome_revision"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Mode            string `json:"mode"`
	CommandShape    string `json:"command_shape"`
	BrowserID       string `json:"browser_id"`
	TargetID        string `json:"target_id"`
	InvocationID    string `json:"invocation_id"`
	ToolName        string `json:"tool_name"`
	DispatchAtUTC   string `json:"dispatch_at_utc"`
	CommitAtUTC     string `json:"interrupt_commit_at_utc"`
	TerminalAtUTC   string `json:"terminal_at_utc"`
	ToolWindowMS    int64  `json:"tool_window_ms"`
	CommitCount     int    `json:"commit_count"`
	InterruptAppend bool   `json:"interrupt_append_observed"`
	TargetPresent   bool   `json:"external_target_present_after_run"`
	ExitCode        int    `json:"exit_code"`
	Pass            bool   `json:"pass"`
}

// TestPinnedChromeAudioInterruptDuringWebMCP is the release-facing Story 003
// proof. It runs the shipped agent binary against the locked Stable Chrome
// artifact and a real Realtime session. The browser event observer supplies
// the authoritative dispatched/terminal timestamps; the provider capture
// supplies the append and commit timestamps. Both scenarios use the exact
// production command shape, with the named case proving the canonical tool
// filter does not fire for the preceding read-only tool.
func TestPinnedChromeAudioInterruptDuringWebMCP(t *testing.T) {
	// This must remain the first observable operation. Normal test runs do not
	// inspect credentials, acquire Chrome, create a fixture, or use the network.
	if os.Getenv(audioInterruptLiveEnv) != "1" {
		t.Skipf("set %s=1 to run the credentialed stock-Chrome audio interrupt proof", audioInterruptLiveEnv)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("the qualified %s Chrome lock is required; observed %s/%s", lockedChromePlatform, runtime.GOOS, runtime.GOARCH)
	}

	apiKey, _, err := loadGateI2APIKey()
	if errors.Is(err, errGateI2MissingAPIKey) {
		t.Skip("OPENAI_API_KEY or OPENAI_API_KEY_FILE is not set; skipping the credentialed audio interrupt proof")
	}
	if err != nil {
		t.Fatalf("load OpenAI API key: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	artifactRoot := audioInterruptArtifactRoot(t)
	root, err := repositoryRoot()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}

	buildRoot := filepath.Join(artifactRoot, "build")
	if err := os.Mkdir(buildRoot, 0o700); err != nil {
		t.Fatalf("create build artifact directory: %v", err)
	}
	binaryPath := filepath.Join(buildRoot, "agent")
	if err := buildGateBinary(ctx, root, binaryPath); err != nil {
		t.Fatalf("build production agent CLI: %v", err)
	}

	chromeCache := filepath.Join(artifactRoot, "chrome-cache")
	if err := os.Mkdir(chromeCache, 0o700); err != nil {
		t.Fatalf("create Chrome cache directory: %v", err)
	}
	pinned, err := acquirePinnedChrome(ctx, chromeCache)
	if err != nil {
		t.Fatalf("acquire qualified Stable Chrome: %v", err)
	}

	scenarios := []audioInterruptScenario{
		{
			name:       "default_first_dispatched_tool",
			request:    "Use the local browser page tools to queue exactly the cube moves R U F L apostrophe D B 2, then wait for the page action to finish.",
			commandTag: "default",
		},
		{
			name:       "named_queue_cube_moves_after_read",
			named:      true,
			request:    "First read the current cube state, then use the local browser page tool queue_cube_moves to queue exactly the cube moves R U F L apostrophe D B 2, and wait for it to finish.",
			commandTag: "named",
		},
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			runAudioInterruptScenario(t, ctx, artifactRoot, pinned, binaryPath, apiKey, scenario)
		})
	}
}

func runAudioInterruptScenario(t *testing.T, parent context.Context, artifactRoot string, pinned pinnedChrome, binaryPath, apiKey string, scenario audioInterruptScenario) {
	t.Helper()
	scenarioRoot := filepath.Join(artifactRoot, scenario.name)
	if err := os.MkdirAll(scenarioRoot, 0o700); err != nil {
		t.Fatalf("create scenario artifact directory: %v", err)
	}

	fixture := newAudioInterruptFixtureServer()
	t.Cleanup(fixture.Close)
	fixtureURL := fixture.URL()
	assertFixtureHeaders(t, parent, fixtureURL)

	workDir := filepath.Join(scenarioRoot, "chrome")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create scenario Chrome directory: %v", err)
	}
	scenarioPinned := pinned
	scenarioPinned.WorkDir = workDir
	browser, err := launchPinnedChrome(parent, scenarioPinned, fixtureURL)
	if err != nil {
		t.Fatalf("launch qualified Stable Chrome: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if closeErr := browser.Close(); closeErr != nil {
				t.Logf("audio interrupt Chrome cleanup: %v", closeErr)
			}
		}
	})

	baseURL := browserHTTPURL(browser.endpoint())
	version, err := waitForDevToolsVersion(parent, baseURL, lockedChromeVersion)
	if err != nil {
		t.Fatalf("read qualified Chrome DevTools version: %v", err)
	}
	rawTarget, err := waitForFixturePageTarget(parent, baseURL, fixtureURL)
	if err != nil {
		t.Fatalf("discover exact audio interrupt fixture target: %v", err)
	}
	browserID, targetID, err := gateI2PublicIDs(version.WebSocketDebuggerURL, rawTarget.ID)
	if err != nil {
		t.Fatalf("derive normalized browser and target IDs: %v", err)
	}

	configDir := filepath.Join(scenarioRoot, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatalf("create session config directory: %v", err)
	}
	cdpURL := strings.TrimRight(baseURL, "/") + "/json/version"
	if err := writeAudioInterruptConfig(configDir, cdpURL, fixture.Origin(), browserID, targetID); err != nil {
		t.Fatalf("write audio interrupt browser config: %v", err)
	}

	observer, observerClose, err := openConversationalCustomerObserver(parent, browserID, webmcp.TargetID(targetID), version)
	if err != nil {
		t.Fatalf("open independent stock-Chrome event observer: %v", err)
	}
	collector := newConversationalCustomerEventCollector()
	go collector.consume(observer)
	observerClosed := false
	t.Cleanup(func() {
		if observerClosed {
			return
		}
		if closeErr := observerClose(); closeErr != nil {
			t.Logf("audio interrupt observer cleanup: %v", closeErr)
		}
	})

	requestDir := filepath.Join(scenarioRoot, "t1b")
	interruptDir := filepath.Join(scenarioRoot, "t2")
	if err := os.Mkdir(requestDir, 0o700); err != nil {
		t.Fatalf("create scheduled audio directory: %v", err)
	}
	if err := os.Mkdir(interruptDir, 0o700); err != nil {
		t.Fatalf("create interrupt audio directory: %v", err)
	}
	requestPath := gateI2SpokenInput(t, requestDir, scenario.request)
	interruptPath := gateI2SpokenInput(t, interruptDir, "Interrupt the active browser action now.")
	systemPromptPath := filepath.Join(scenarioRoot, "system-prompt.txt")
	if err := os.WriteFile(systemPromptPath, []byte(audioInterruptSystemPrompt(scenario)), 0o600); err != nil {
		t.Fatalf("write audio interrupt system prompt: %v", err)
	}

	capturePath := filepath.Join(scenarioRoot, "provider.json")
	recordDir := filepath.Join(scenarioRoot, "recording")
	args := []string{
		"session",
		"--browser-tools", "webmcp",
		"--browser-cdp-url", cdpURL,
		"--browser-browser", browserID,
		"--browser-tab", targetID,
		"--browser-allowed-origin", fixture.Origin(),
		"--browser-approval", "never",
		"--browser-cancel-on-interrupt", "read-only",
		"--browser-record", "true",
		"--browser-record-arguments", "true",
		"--browser-record-results", "true",
		"--provider", "openai",
		"--model", audioInterruptModel,
		"--record", capturePath,
		"--record-dir", recordDir,
		"--system-prompt", systemPromptPath,
		"--audio-in-turn", requestPath,
		"--audio-interrupt", interruptPath,
		"--max-duration", audioInterruptMaxDuration.String(),
	}
	if scenario.named {
		args = append(args, "--audio-interrupt-on-tool", audioInterruptTool)
	}

	runContext, cancelRun := context.WithTimeout(parent, audioInterruptMaxDuration+20*time.Second)
	process, err := startGateCommandWithEnvironment(runContext, binaryPath, configDir, []string{"AGENT_MODEL__OPENAI__API_KEY=" + apiKey}, args...)
	if err != nil {
		cancelRun()
		t.Fatalf("start production audio interrupt session: %v", err)
	}
	result, waitErr := process.wait(runContext)
	cancelRun()
	if waitErr != nil {
		t.Fatalf("production audio interrupt session wait: %v", waitErr)
	}
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("production audio interrupt session exited unsuccessfully: exit=%d err=%v", result.ExitCode, result.Err)
	}

	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load provider capture: %v", err)
	}
	wire, err := inspectAudioInterruptCapture(capture)
	if err != nil {
		t.Fatalf("inspect provider capture: %v", err)
	}

	events := collector.snapshot()
	invoked, terminal, err := audioInterruptInvocationEvents(events, scenario.named)
	if err != nil {
		t.Fatalf("inspect browser invocation events: %v", err)
	}
	if invoked.At.IsZero() || terminal.At.IsZero() || !terminal.At.After(invoked.At) {
		t.Fatalf("browser invocation timing = invoked=%s terminal=%s, want a real positive in-flight window", invoked.At, terminal.At)
	}
	toolWindow := terminal.At.Sub(invoked.At)
	if toolWindow < audioInterruptWindowMinimum || toolWindow > audioInterruptWindowMaximum {
		t.Fatalf("queue_cube_moves window = %s, want between %s and %s", toolWindow, audioInterruptWindowMinimum, audioInterruptWindowMaximum)
	}
	if terminal.Status == "" || terminal.ErrorCode != "" {
		t.Fatalf("queue_cube_moves terminal = %+v, want successful terminal browser event", terminal)
	}

	oracleContext, cancelOracle := context.WithTimeout(parent, 10*time.Second)
	oracle, oracleErr := waitForFixtureOracle(oracleContext, fixture.StateURL(), func(value fixtureOracle) bool {
		return value.Ready && strings.HasPrefix(value.Value, "completed:") && value.VisibleText == value.Value && !value.Pending && hasAudioInterruptFixtureInvocation(value, audioInterruptTool)
	})
	cancelOracle()
	if oracleErr != nil {
		t.Fatalf("wait for completed slow page-tool oracle: %v", oracleErr)
	}

	commitAt, appendObserved, err := validateAudioInterruptWireWindow(wire, invoked.At, terminal.At)
	if err != nil {
		t.Fatalf("validate provider append/commit window: %v", err)
	}
	evidence := audioInterruptEvidence{
		Schema:          "s2s.audio-interrupt.v1",
		ObservedAtUTC:   time.Now().UTC().Format(time.RFC3339Nano),
		Build:           "agent-cli/cmd/agent (go build ./cmd/agent)",
		ChromeChannel:   lockedChromeChannel,
		ChromePlatform:  lockedChromePlatform,
		ChromeVersion:   lockedChromeVersion,
		ChromeRevision:  lockedChromeRevision,
		Provider:        wire.provider,
		Model:           wire.model,
		Mode:            scenario.commandTag,
		CommandShape:    audioInterruptCommandShape(scenario.named),
		BrowserID:       browserID,
		TargetID:        targetID,
		InvocationID:    string(invoked.InvocationID),
		ToolName:        audioInterruptTool,
		DispatchAtUTC:   invoked.At.UTC().Format(time.RFC3339Nano),
		CommitAtUTC:     commitAt.UTC().Format(time.RFC3339Nano),
		TerminalAtUTC:   terminal.At.UTC().Format(time.RFC3339Nano),
		ToolWindowMS:    toolWindow.Milliseconds(),
		CommitCount:     len(wire.commits),
		InterruptAppend: appendObserved,
		ExitCode:        result.ExitCode,
		Pass:            true,
	}
	evidencePath := filepath.Join(scenarioRoot, "audio-interrupt-evidence.json")
	evidenceBytes, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("encode sanitized audio interrupt evidence: %v", err)
	}
	if err := os.WriteFile(evidencePath, append(evidenceBytes, '\n'), 0o600); err != nil {
		t.Fatalf("write sanitized audio interrupt evidence: %v", err)
	}

	if closeErr := observerClose(); closeErr != nil {
		t.Fatalf("close independent stock-Chrome event observer: %v", closeErr)
	}
	observerClosed = true
	if _, err := waitForFixtureTarget(parent, baseURL, webmcp.TargetID(rawTarget.ID), fixtureURL, true); err != nil {
		t.Fatalf("external target was not retained after the production session: %v", err)
	}
	evidence.TargetPresent = true
	evidenceBytes, err = json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("encode final sanitized audio interrupt evidence: %v", err)
	}
	if err := os.WriteFile(evidencePath, append(evidenceBytes, '\n'), 0o600); err != nil {
		t.Fatalf("rewrite sanitized audio interrupt evidence: %v", err)
	}

	if closeErr := browser.Close(); closeErr != nil {
		t.Logf("audio interrupt Chrome close returned: %v", closeErr)
	}
	closed = true
	t.Logf("WEBMCP_AUDIO_INTERRUPT_PASS mode=%s chrome=%s revision=%s browser=%s target=%s tool=%s invocation=%s commits=%d append_in_window=%t window=%dms command=%s evidence=<artifact>/audio-interrupt-evidence.json", scenario.commandTag, lockedChromeVersion, lockedChromeRevision, browserID, targetID, audioInterruptTool, invoked.InvocationID, len(wire.commits), appendObserved, toolWindow.Milliseconds(), audioInterruptCommandShape(scenario.named))
	_ = oracle
}

func audioInterruptSystemPrompt(scenario audioInterruptScenario) string {
	steps := []string{
		"You are driving one local WebMCP page through the browser capability.",
		"Use webmcp_list_tabs, webmcp_select_tab, and webmcp_list_tools before any page action.",
		"Use webmcp_invoke with the exact current tool_ref and a valid JSON object encoded in input_json; never invent a ref or retry a page action.",
		fmt.Sprintf("The page tool queue_cube_moves accepts a moves string; use exactly %q for this proof.", audioInterruptMoves),
		"After invoking a page tool, wait for its terminal result before speaking.",
	}
	if scenario.named {
		steps = append(steps, "For this run, invoke read_cube_state first, wait for its terminal result, then invoke queue_cube_moves exactly once. The audio interrupt must be associated only with queue_cube_moves.")
	} else {
		steps = append(steps, "For this run, invoke queue_cube_moves exactly once as the first page invocation.")
	}
	steps = append(steps, "The spoken request is authoritative. Confirm only the final completed page result.")
	return strings.Join(steps, "\n")
}

func audioInterruptCommandShape(named bool) string {
	shape := "agent session --browser-tools webmcp --provider openai --model gpt-realtime-2.1-mini --audio-in-turn <t1b.wav> --audio-interrupt <t2.wav>"
	if named {
		shape += " --audio-interrupt-on-tool queue_cube_moves"
	}
	return shape + " --max-duration 30s"
}

func hasAudioInterruptFixtureInvocation(oracle fixtureOracle, toolName string) bool {
	for _, invocation := range oracle.Invocations {
		if invocation == toolName || strings.HasPrefix(invocation, toolName+":") {
			return true
		}
	}
	return false
}

func inspectAudioInterruptCapture(capture gwtesting.SessionCapture) (audioInterruptWireObservation, error) {
	startedAt, err := time.Parse(time.RFC3339Nano, capture.Session.StartedAtUTC)
	if err != nil {
		return audioInterruptWireObservation{}, fmt.Errorf("parse capture start %q: %w", capture.Session.StartedAtUTC, err)
	}
	observation := audioInterruptWireObservation{
		provider:  capture.Provider.Name,
		model:     capture.Provider.Model,
		startedAt: startedAt,
		commits:   make([]audioInterruptWireEvent, 0, 2),
		appends:   make([]audioInterruptWireEvent, 0),
	}
	for index, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			if record.Type == "error" {
				observation.providerErr++
			}
			continue
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		at := startedAt.Add(time.Duration(record.TimestampMs) * time.Millisecond)
		switch record.Type {
		case "input_audio_buffer.commit":
			observation.commits = append(observation.commits, audioInterruptWireEvent{index: index, timestamp: at, sequence: record.Sequence, eventType: record.Type})
		case "input_audio_buffer.append":
			var event struct {
				Audio string `json:"audio"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				return observation, fmt.Errorf("decode input audio append %d: %w", index, err)
			}
			observation.appends = append(observation.appends, audioInterruptWireEvent{index: index, timestamp: at, sequence: record.Sequence, eventType: record.Type, audioBytes: len(event.Audio)})
		}
	}
	if observation.provider != "openai" || observation.model != audioInterruptModel {
		return observation, fmt.Errorf("provider identity=(%q,%q), want (openai,%q)", observation.provider, observation.model, audioInterruptModel)
	}
	if observation.providerErr != 0 {
		return observation, fmt.Errorf("provider emitted %d error event(s)", observation.providerErr)
	}
	return observation, nil
}

func audioInterruptInvocationEvents(events []webmcp.BrowserEvent, named bool) (webmcp.BrowserEvent, webmcp.BrowserEvent, error) {
	var invoked webmcp.BrowserEvent
	var terminal webmcp.BrowserEvent
	queueIndex := -1
	readIndex := -1
	for index, event := range events {
		if event.Type == webmcp.EventToolInvoked && event.ToolName == audioInterruptTool {
			if queueIndex >= 0 {
				return webmcp.BrowserEvent{}, webmcp.BrowserEvent{}, errors.New("queue_cube_moves was dispatched more than once")
			}
			queueIndex = index
			invoked = event
		}
		if event.Type == webmcp.EventToolInvoked && event.ToolName == "read_cube_state" && readIndex < 0 {
			readIndex = index
		}
	}
	if queueIndex < 0 || invoked.InvocationID == "" {
		return webmcp.BrowserEvent{}, webmcp.BrowserEvent{}, errors.New("browser observer did not see a dispatched queue_cube_moves invocation")
	}
	if named && (readIndex < 0 || readIndex >= queueIndex) {
		return webmcp.BrowserEvent{}, webmcp.BrowserEvent{}, errors.New("named run did not dispatch read_cube_state before queue_cube_moves")
	}
	for index := queueIndex + 1; index < len(events); index++ {
		event := events[index]
		if event.Type == webmcp.EventToolResponded && event.InvocationID == invoked.InvocationID {
			if !terminal.At.IsZero() {
				return webmcp.BrowserEvent{}, webmcp.BrowserEvent{}, errors.New("queue_cube_moves emitted duplicate terminal events")
			}
			terminal = event
		}
	}
	if terminal.InvocationID == "" {
		return webmcp.BrowserEvent{}, webmcp.BrowserEvent{}, errors.New("browser observer did not see the queue_cube_moves terminal event")
	}
	return invoked, terminal, nil
}

func validateAudioInterruptWireWindow(observation audioInterruptWireObservation, dispatchedAt, terminalAt time.Time) (time.Time, bool, error) {
	if len(observation.commits) != 2 {
		return time.Time{}, false, fmt.Errorf("provider emitted %d input_audio_buffer.commit events, want exactly the scheduled commit and one interrupt commit", len(observation.commits))
	}
	first, second := observation.commits[0], observation.commits[1]
	if first.index >= second.index || first.sequence >= second.sequence {
		return time.Time{}, false, fmt.Errorf("commit ordering is not monotonic: first=%+v second=%+v", first, second)
	}
	if second.timestamp.Before(dispatchedAt.Add(-audioInterruptTimingSlack)) || second.timestamp.After(terminalAt.Add(audioInterruptTimingSlack)) {
		return time.Time{}, false, fmt.Errorf("interrupt commit at %s is outside dispatched=%s terminal=%s", second.timestamp.UTC().Format(time.RFC3339Nano), dispatchedAt.UTC().Format(time.RFC3339Nano), terminalAt.UTC().Format(time.RFC3339Nano))
	}
	if first.timestamp.After(dispatchedAt.Add(audioInterruptTimingSlack)) {
		return time.Time{}, false, fmt.Errorf("scheduled commit at %s occurred after dispatch=%s", first.timestamp.UTC().Format(time.RFC3339Nano), dispatchedAt.UTC().Format(time.RFC3339Nano))
	}
	appendObserved := false
	for _, event := range observation.appends {
		if event.index >= second.index || event.audioBytes == 0 {
			continue
		}
		if !event.timestamp.Before(dispatchedAt.Add(-audioInterruptTimingSlack)) && !event.timestamp.After(terminalAt.Add(audioInterruptTimingSlack)) {
			appendObserved = true
			break
		}
	}
	if !appendObserved {
		return time.Time{}, false, errors.New("no provider input_audio_buffer.append was recorded inside the dispatched/terminal window")
	}
	return second.timestamp, true, nil
}

func writeAudioInterruptConfig(configDir, cdpURL, origin, browserID, targetID string) error {
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
    cancel_on_interrupt: read-only
  limits:
    invocation_timeout: 10s
  recording:
    enabled: true
    include_arguments: true
    include_results: true
`, cdpURL, browserID, targetID, origin)
	return os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(contents), 0o600)
}

func audioInterruptArtifactRoot(t *testing.T) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv(audioInterruptArtifactEnv))
	if parent == "" {
		return t.TempDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create audio interrupt artifact parent: %v", err)
	}
	root, err := os.MkdirTemp(parent, "s2s-audio-interrupt-")
	if err != nil {
		t.Fatalf("create audio interrupt artifact directory: %v", err)
	}
	return root
}

func newAudioInterruptFixtureServer() *audioInterruptFixtureServer {
	fixture := &audioInterruptFixtureServer{oracle: fixtureOracle{Value: "initial", VisibleText: "initial"}}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			if request.Method != http.MethodGet {
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.Header().Set("Origin-Agent-Cluster", "?1")
			writer.Header().Set("Permissions-Policy", "tools=(self)")
			_, _ = writer.Write(audioInterruptFixtureHTML)
		case "/__test/state":
			fixture.handleOracle(writer, request)
		default:
			http.NotFound(writer, request)
		}
	}))
	return fixture
}

func (f *audioInterruptFixtureServer) URL() string {
	return f.server.URL + "/"
}

func (f *audioInterruptFixtureServer) Origin() string {
	return f.server.URL
}

func (f *audioInterruptFixtureServer) StateURL() string {
	return f.server.URL + "/__test/state"
}

func (f *audioInterruptFixtureServer) Close() {
	if f != nil && f.server != nil {
		f.server.Close()
	}
}

func (f *audioInterruptFixtureServer) handleOracle(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request.Method {
	case http.MethodGet:
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(f.oracle)
	case http.MethodPost:
		var oracle fixtureOracle
		if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64<<10)).Decode(&oracle); err != nil {
			http.Error(writer, "invalid oracle", http.StatusBadRequest)
			return
		}
		oracle.Invocations = append([]string(nil), oracle.Invocations...)
		f.oracle = oracle
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}
