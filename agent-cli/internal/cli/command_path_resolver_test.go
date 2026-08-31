package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/spf13/cobra"
)

func newPathPreflightRoot(command *cobra.Command, resolver *pathResolver) *cobra.Command {
	root := &cobra.Command{Use: "agent", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(command)
	router := &Router{pathResolver: resolver}
	root.PersistentPreRunE = func(command *cobra.Command, args []string) error {
		return router.resolveCommandPaths(command, args)
	}
	return root
}

func testPathResolver(currentHome, namedHome string) *pathResolver {
	return &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
		lookupUser: func(name string) (string, error) {
			if name != "alice" {
				return "", errors.New("unknown test user")
			}
			return namedHome, nil
		},
	}
}

func TestRouterPreRunNormalizesSessionPathFlagsAndPreservesSentinels(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	sessionOwner := NewSessionCommand(askFlags, globalFlags, nil, nil)
	sessionCommand := sessionOwner.Generate()
	root := newPathPreflightRoot(sessionCommand, testPathResolver(currentHome, namedHome))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{
		"session",
		"--transport", "webrtc",
		"--signaling", "wss://signal.example.test/~alice",
		"--base-url", "wss://provider.example.test/~alice",
		"--record", "~/captures/session.json",
		"--record-dir", "~alice/captures/complete",
		"--replay", "~/captures/replay.json",
		"--system-prompt", "~alice/prompts/system.txt",
		"--audio-in", "-",
		"--audio-in-turn", "~/audio/one.raw",
		"--audio-in-turn", "~alice/audio/two.raw",
		"--audio-interrupt", "~/audio/interrupt.raw",
		"--audio-interrupt", "~alice/audio/interrupt-two.raw",
		"--audio-out", "-",
		"--image", "~/images/one.png",
		"--image", "~alice/images/two.png",
		"--browser-user-data-dir", "~/browser/profile",
		"--browser-replay", "~alice/browser/session.json",
	})

	err := root.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, ErrSessionWebRTCUnavailable) {
		t.Fatalf("session preflight result = %v, want the post-preflight WebRTC availability error", err)
	}

	assertFlagString(t, sessionCommand, "record-dir", filepath.Join(namedHome, "captures", "complete"))
	assertFlagString(t, sessionCommand, "audio-in", "-")
	assertFlagString(t, sessionCommand, "audio-out", "-")
	assertFlagString(t, sessionCommand, "browser-user-data-dir", filepath.Join(currentHome, "browser", "profile"))
	assertFlagString(t, sessionCommand, "browser-replay", filepath.Join(namedHome, "browser", "session.json"))
	assertFlagString(t, sessionCommand, "signaling", "wss://signal.example.test/~alice")
	if askFlags.RecordCapturePath != filepath.Join(currentHome, "captures", "session.json") {
		t.Fatalf("--record = %q, want current-home path", askFlags.RecordCapturePath)
	}
	if askFlags.ReplayCapturePath != filepath.Join(currentHome, "captures", "replay.json") {
		t.Fatalf("--replay = %q, want current-home path", askFlags.ReplayCapturePath)
	}
	if askFlags.SystemPrompt != filepath.Join(namedHome, "prompts", "system.txt") {
		t.Fatalf("--system-prompt = %q, want named-home path", askFlags.SystemPrompt)
	}
	if askFlags.BaseURL != "wss://provider.example.test/~alice" {
		t.Fatalf("--base-url = %q, want URL unchanged", askFlags.BaseURL)
	}

	wantTurns := []string{
		filepath.Join(currentHome, "audio", "one.raw"),
		filepath.Join(namedHome, "audio", "two.raw"),
	}
	gotTurns, err := sessionCommand.Flags().GetStringArray("audio-in-turn")
	if err != nil || !equalStringSlices(gotTurns, wantTurns) {
		t.Fatalf("--audio-in-turn = %#v (error %v), want %#v", gotTurns, err, wantTurns)
	}
	wantInterrupts := []string{
		filepath.Join(currentHome, "audio", "interrupt.raw"),
		filepath.Join(namedHome, "audio", "interrupt-two.raw"),
	}
	gotInterrupts, err := sessionCommand.Flags().GetStringArray("audio-interrupt")
	if err != nil || !equalStringSlices(gotInterrupts, wantInterrupts) {
		t.Fatalf("--audio-interrupt = %#v (error %v), want %#v", gotInterrupts, err, wantInterrupts)
	}
	wantImages := []string{
		filepath.Join(currentHome, "images", "one.png"),
		filepath.Join(namedHome, "images", "two.png"),
	}
	if !equalStringSlices(sessionOwner.imagePaths, wantImages) {
		t.Fatalf("--image = %#v, want %#v", sessionOwner.imagePaths, wantImages)
	}
}

func TestRouterPreRunRejectsInvalidRepeatableSessionPathBeforeApplyingAnyValue(t *testing.T) {
	currentHome := t.TempDir()
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	sessionOwner := NewSessionCommand(askFlags, globalFlags, nil, nil)
	sessionCommand := sessionOwner.Generate()
	root := newPathPreflightRoot(sessionCommand, &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
		lookupUser:  func(name string) (string, error) { return "", errors.New("user lookup failed for " + name) },
	})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"session", "--image", "~/valid.png", "--image", "~missing/invalid.png"})

	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--image") || !strings.Contains(err.Error(), "~missing/invalid.png") {
		t.Fatalf("invalid repeatable path error = %v, want flag and input", err)
	}
	if !equalStringSlices(sessionOwner.imagePaths, []string{"~/valid.png", "~missing/invalid.png"}) {
		t.Fatalf("image values after failed preflight = %#v, want original values", sessionOwner.imagePaths)
	}
	if output.Len() != 0 {
		t.Fatalf("command output after failed preflight = %q, want empty", output.String())
	}
}

func TestRouterPreRunResolvesAskTildeAttachmentBeforeReadingIt(t *testing.T) {
	currentHome := t.TempDir()
	attachment := filepath.Join(currentHome, "notes with spaces.txt")
	if err := os.WriteFile(attachment, []byte("home notes"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	askFlags := flags.NewAskFlags()
	loopFlags := flags.NewLoopFlags()
	owner := NewAskCommand(agent.NewExecutor(nil, nil, nil), askFlags, loopFlags, globalFlags)
	var gotInput agentloop.ExecuteInput
	var calls int
	owner.runAsk = func(_ context.Context, _ *agent.Config, input agentloop.ExecuteInput, _ io.Writer) (string, error) {
		calls++
		gotInput = input
		return "ok", nil
	}
	root := newPathPreflightRoot(owner.Generate(), &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
	})
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"ask", "--system-prompt", "Use ~assistant syntax", "summarize", "~/notes with spaces.txt"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ask command: %v", err)
	}
	if calls != 1 || gotInput.Message != "summarize" {
		t.Fatalf("ask invocation count/input = %d/%#v, want one call with prompt", calls, gotInput)
	}
	if len(gotInput.ContentParts) != 1 {
		t.Fatalf("ask content parts = %#v, want one attachment", gotInput.ContentParts)
	}
	part, ok := gotInput.ContentParts[0].(messages.FilePart)
	if !ok || part.Name != filepath.Base(attachment) || string(part.Bytes) != "home notes" {
		t.Fatalf("ask attachment = %#v, want expanded home file", gotInput.ContentParts[0])
	}
	if askFlags.SystemPrompt != "Use ~assistant syntax" {
		t.Fatalf("literal --system-prompt = %q, want unchanged", askFlags.SystemPrompt)
	}
}

func TestRouterPreRunRejectsAskTildeAttachmentBeforeCommandExecution(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	owner := NewAskCommand(agent.NewExecutor(nil, nil, nil), askFlags, flags.NewLoopFlags(), globalFlags)
	var calls int
	owner.runAsk = func(context.Context, *agent.Config, agentloop.ExecuteInput, io.Writer) (string, error) {
		calls++
		return "", nil
	}
	root := newPathPreflightRoot(owner.Generate(), &pathResolver{
		currentHome: func() (string, error) { return "", errors.New("current home unavailable") },
	})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"ask", "describe", "~/missing.txt"})

	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "resolve ask file operand") || !strings.Contains(err.Error(), "~/missing.txt") {
		t.Fatalf("ask path error = %v, want operand and input", err)
	}
	if calls != 0 || output.Len() != 0 {
		t.Fatalf("ask side effects after path failure = calls:%d output:%q", calls, output.String())
	}
}

func TestRouterPreRunNormalizesSelfPlayOutputDirectory(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	globalFlags := flags.NewGlobalFlags()
	owner := NewSessionSelfPlayCommand(globalFlags)
	var got services.SelfPlayRunOptions
	owner.SetRunner(func(_ context.Context, _ io.Writer, options services.SelfPlayRunOptions) error {
		got = options
		return nil
	})

	sessionGroup := &cobra.Command{Use: "session"}
	sessionGroup.AddCommand(owner.Generate())
	root := newPathPreflightRoot(sessionGroup, testPathResolver(currentHome, namedHome))
	root.SetArgs([]string{"session", "self-play", "--output-dir", "~alice/self-play"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("self-play command: %v", err)
	}
	if got.OutputDir != filepath.Join(namedHome, "self-play") {
		t.Fatalf("self-play output directory = %q, want named-home path", got.OutputDir)
	}
}

func TestRouterPreRunNormalizesRoomManifestAndOutputPaths(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	manifestPath := filepath.Join(currentHome, "room.json")
	t.Setenv("ROOM_PATH_TEST_KEY", "test-key")
	manifest := []byte(`{
  "schema_version": 1,
  "room": {"max_turns": 1},
  "participants": [
    {"id": "alice", "system_prompt": "Alice", "opening_prompt": "Start", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_PATH_TEST_KEY", "tools": []},
    {"id": "bob", "system_prompt": "Bob", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_PATH_TEST_KEY", "tools": []}
  ]
}`)
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatalf("write room manifest: %v", err)
	}

	globalFlags := flags.NewGlobalFlags()
	owner := NewRoomRunCommand(globalFlags)
	var got services.RoomRunOptions
	owner.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		got = options
		if err := os.MkdirAll(options.OutputDir, 0o700); err != nil {
			return services.RoomResult{}, err
		}
		if err := os.WriteFile(filepath.Join(options.OutputDir, "evidence.json"), []byte("{}"), 0o600); err != nil {
			return services.RoomResult{}, err
		}
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	roomGroup := NewRoomCommand().Generate()
	roomGroup.AddCommand(owner.Generate())
	root := newPathPreflightRoot(roomGroup, testPathResolver(currentHome, namedHome))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"room", "run", "--manifest", "~/room.json", "--out", "~alice/evidence"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("room command: %v", err)
	}
	if got.LaunchPlan == nil || got.LaunchPlan.ConfigPath != manifestPath {
		t.Fatalf("room manifest path = %#v, want %q", got.LaunchPlan, manifestPath)
	}
	if got.OutputDir != filepath.Join(namedHome, "evidence") {
		t.Fatalf("room output directory = %q, want named-home path", got.OutputDir)
	}
	if _, err := os.Stat(filepath.Join(namedHome, "evidence", "evidence.json")); err != nil {
		t.Fatalf("room evidence artifact = %v, want artifact below expanded output directory", err)
	}
	if _, err := os.Stat(filepath.Join(currentHome, "~alice")); !os.IsNotExist(err) {
		t.Fatalf("literal current-home tilde tree exists or could not be checked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(currentHome, "room.json")); err != nil {
		t.Fatalf("manifest disappeared: %v", err)
	}
}

func TestRouterPreRunNormalizesMediaReplayFixtureAndLeavesURLOperandAlone(t *testing.T) {
	currentHome := t.TempDir()
	fixturePath := filepath.Join(currentHome, "session.json")
	var gotFixture string
	probe := NewMediaProbeCommandWithOptions(WithSessionReplayProbe(func(_ context.Context, fixture string) (gatewaytesting.SessionReplayProbeReport, error) {
		gotFixture = fixture
		return gatewaytesting.SessionReplayProbeReport{}, nil
	}))
	mediaGroup := &cobra.Command{Use: "media"}
	mediaGroup.AddCommand(probe.Generate())
	root := newPathPreflightRoot(mediaGroup, &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
	})
	root.SetArgs([]string{"media", "probe", "--replay-fixture", "~/session.json", "go2rtc://camera/~alice"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("media probe command: %v", err)
	}
	if gotFixture != fixturePath {
		t.Fatalf("media replay fixture = %q, want %q", gotFixture, fixturePath)
	}
}

func TestRouterPreRunNormalizesInteractionReplayFixtureOperand(t *testing.T) {
	currentHome := t.TempDir()
	fixturePath := filepath.Join(currentHome, "interaction.json")
	data, err := json.Marshal(interactionFixture())
	if err != nil {
		t.Fatalf("marshal interaction fixture: %v", err)
	}
	if err := os.WriteFile(fixturePath, data, 0o600); err != nil {
		t.Fatalf("write interaction fixture: %v", err)
	}

	interactionGroup := NewInteractionCommand().Generate()
	interactionGroup.AddCommand(NewInteractionReplayCommand().Generate())
	root := newPathPreflightRoot(interactionGroup, &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
	})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"interaction", "replay", "~/interaction.json"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("interaction replay command: %v", err)
	}
	if output.Len() == 0 || !strings.Contains(output.String(), `"interactionId":"int-123"`) {
		t.Fatalf("interaction replay output = %q, want replayed fixture", output.String())
	}
}

func newProbePathPreflightRoot(command *cobra.Command, resolver *pathResolver) *cobra.Command {
	probeGroup := &cobra.Command{Use: "probe"}
	probeGroup.AddCommand(command)
	return newPathPreflightRoot(probeGroup, resolver)
}

func TestRouterPreRunNormalizesProbeRunPathsAndWritesUnderExpandedHomes(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	fixtureData, err := os.ReadFile(probeSessionFixture)
	if err != nil {
		t.Fatalf("read replay fixture: %v", err)
	}
	fixturePath := filepath.Join(currentHome, "session.session.json")
	if err := os.WriteFile(fixturePath, fixtureData, 0o600); err != nil {
		t.Fatalf("write replay fixture: %v", err)
	}
	observation := probeFixtureObservation(t)
	firstScenario := writeProbeScenario(t, currentHome, "home-scenario-one", len(observation.Observations))
	secondScenario := writeProbeScenario(t, namedHome, "home-scenario-two", len(observation.Observations))

	owner := NewProbeRunCommand()
	command := owner.Generate()
	root := newProbePathPreflightRoot(command, testPathResolver(currentHome, namedHome))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"probe", "run", "~/home-scenario-one.scenario.json",
		"--scenario", "~alice/home-scenario-two.scenario.json",
		"--replay", "~/session.session.json",
		"--out", "~alice/results.jsonl",
		"--summary", "~/summary.jsonl",
		"--evidence-root", "~alice/evidence",
		"--browser-user-data-dir", "~/browser/profile",
		"--browser-replay", "~alice/browser/replay.json",
		"--json",
	})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("probe run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if owner.Replay != fixturePath {
		t.Fatalf("--replay = %q, want %q", owner.Replay, fixturePath)
	}
	if owner.OutPath != filepath.Join(namedHome, "results.jsonl") {
		t.Fatalf("--out = %q, want expanded named-home path", owner.OutPath)
	}
	if owner.SummaryPath != filepath.Join(currentHome, "summary.jsonl") {
		t.Fatalf("--summary = %q, want expanded current-home path", owner.SummaryPath)
	}
	if owner.RecordingRoot != filepath.Join(namedHome, "evidence") {
		t.Fatalf("--evidence-root = %q, want expanded named-home path", owner.RecordingRoot)
	}
	if !equalStringSlices(owner.Scenarios, []string{firstScenario, secondScenario}) {
		t.Fatalf("--scenario values = %#v, want expanded paths", owner.Scenarios)
	}
	assertFlagString(t, command, "browser-user-data-dir", filepath.Join(currentHome, "browser", "profile"))
	assertFlagString(t, command, "browser-replay", filepath.Join(namedHome, "browser", "replay.json"))

	results, err := os.ReadFile(filepath.Join(namedHome, "results.jsonl"))
	if err != nil || len(strings.TrimSpace(string(results))) == 0 {
		t.Fatalf("expanded probe results = %q (error %v), want result lines below named home", results, err)
	}
	if _, err := os.Stat(filepath.Join(currentHome, "summary.jsonl")); err != nil {
		t.Fatalf("expanded probe summary: %v", err)
	}
	if _, err := os.Stat("~"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("literal cwd/~ exists or could not be checked: %v", err)
	}
}

func TestRouterPreRunNormalizesProbeReportInputAndOutputs(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	inputPath := filepath.Join(currentHome, "run.jsonl")
	input := []byte("{\"name\":\"home-report\",\"pass\":true,\"terminal_reason\":\"disconnect\"}\n{\"total\":1,\"passed\":1,\"failed\":0,\"status\":\"pass\"}\n")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatalf("write report input: %v", err)
	}

	command := NewProbeReportCommand().Generate()
	root := newProbePathPreflightRoot(command, testPathResolver(currentHome, namedHome))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"probe", "report",
		"--out", "~/run.jsonl",
		"--json", "~alice/friction.json",
		"--summary", "~/friction.txt",
	})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("probe report: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("probe report leaked file outputs to streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	var report map[string]any
	reportBytes, err := os.ReadFile(filepath.Join(namedHome, "friction.json"))
	if err != nil {
		t.Fatalf("read expanded friction report: %v", err)
	}
	if err := json.Unmarshal(reportBytes, &report); err != nil || report["total"] != float64(1) {
		t.Fatalf("expanded friction report = %q (error %v), want one report result", reportBytes, err)
	}
	if _, err := os.Stat(filepath.Join(currentHome, "friction.txt")); err != nil {
		t.Fatalf("read expanded friction summary: %v", err)
	}
	if _, err := os.Stat("~"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("literal cwd/~ exists or could not be checked: %v", err)
	}
}

func TestRouterPreRunNormalizesProbeGateRepeatableArtifactsAndSentinel(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	inputPath := filepath.Join(currentHome, "run.jsonl")
	input := []byte("{\"name\":\"home-gate\",\"pass\":true,\"terminal_reason\":\"disconnect\"}\n")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatalf("write gate input: %v", err)
	}

	owner := NewProbeGateCommand()
	command := owner.Generate()
	root := newProbePathPreflightRoot(command, testPathResolver(currentHome, namedHome))
	root.SetIn(strings.NewReader(string(input)))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"probe", "gate",
		"--out", "~/run.jsonl",
		"--out", "-",
		"--json", "~alice/gate.json",
	})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("probe gate: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	gotArtifacts, err := command.Flags().GetStringArray("out")
	if err != nil || !equalStringSlices(gotArtifacts, []string{inputPath, "-"}) {
		t.Fatalf("gate artifacts = %#v (error %v), want expanded file and unchanged stdin sentinel", gotArtifacts, err)
	}
	if _, err := os.Stat(filepath.Join(namedHome, "gate.json")); err != nil {
		t.Fatalf("expanded gate JSON: %v", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("gate stdout is empty, want JSON verdict")
	}
}

func TestRouterPreRunRejectsProbeRepeatablePathBeforeOutputs(t *testing.T) {
	currentHome := t.TempDir()
	owner := NewProbeReportCommand()
	command := owner.Generate()
	root := newProbePathPreflightRoot(command, &pathResolver{
		currentHome: func() (string, error) { return currentHome, nil },
		lookupUser:  func(name string) (string, error) { return "", fmt.Errorf("unknown test user %q", name) },
	})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{
		"probe", "report",
		"--out", "~/valid.jsonl",
		"--out", "~missing/invalid.jsonl",
		"--json", "~/report.json",
	})

	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--out") || !strings.Contains(err.Error(), "~missing/invalid.jsonl") {
		t.Fatalf("probe repeatable path error = %v, want flag and invalid input", err)
	}
	if !equalStringSlices(owner.Inputs, []string{"~/valid.jsonl", "~missing/invalid.jsonl"}) {
		t.Fatalf("report inputs after failed preflight = %#v, want original values", owner.Inputs)
	}
	if output.Len() != 0 {
		t.Fatalf("output after failed probe preflight = %q, want empty", output.String())
	}
	if _, err := os.Stat(filepath.Join(currentHome, "report.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report output was created after failed preflight: %v", err)
	}
}

func TestRouterPreRunNormalizesCustomerSimulationPathsAndScenarioOperands(t *testing.T) {
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	owner := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	command := owner.Generate()
	var gotArgs []string
	command.RunE = func(_ *cobra.Command, args []string) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}
	root := newProbePathPreflightRoot(command, testPathResolver(currentHome, namedHome))
	root.SetArgs([]string{
		"probe", "customer-simulation", "~alice/positional.scenario.json",
		"--scenario", "~/scenario-one.json",
		"--scenario", "~alice/scenario-two.json",
		"--audio", "~/audio-one.raw",
		"--audio", "~alice/audio-two.raw",
		"--audio-dir", "~/audio",
		"--patience-reprompt-audio", "~alice/patience.wav",
		"--binary", "~/bin/agent",
		"--run-root", "~alice/runs",
		"--system-prompt", "~alice/prompt.txt",
		"--secret-file", "~/secret",
		"--validator-secret-file", "~alice/validator-secret",
		"--report", "~/report.json",
	})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("customer simulation preflight: %v", err)
	}
	wantScenarioFlags := []string{filepath.Join(currentHome, "scenario-one.json"), filepath.Join(namedHome, "scenario-two.json")}
	if !equalStringSlices(owner.ScenarioPaths, wantScenarioFlags) {
		t.Fatalf("customer --scenario = %#v, want %#v", owner.ScenarioPaths, wantScenarioFlags)
	}
	if !equalStringSlices(owner.AudioPaths, []string{filepath.Join(currentHome, "audio-one.raw"), filepath.Join(namedHome, "audio-two.raw")}) {
		t.Fatalf("customer --audio = %#v, want expanded paths", owner.AudioPaths)
	}
	if owner.AudioDir != filepath.Join(currentHome, "audio") || owner.PatienceRepromptAudioPath != filepath.Join(namedHome, "patience.wav") {
		t.Fatalf("customer audio paths = %q/%q, want expanded paths", owner.AudioDir, owner.PatienceRepromptAudioPath)
	}
	if owner.BinaryPath != filepath.Join(currentHome, "bin", "agent") || owner.RunRoot != filepath.Join(namedHome, "runs") {
		t.Fatalf("customer binary/run root = %q/%q, want expanded paths", owner.BinaryPath, owner.RunRoot)
	}
	if owner.SystemPrompt != filepath.Join(namedHome, "prompt.txt") || owner.SecretFile != filepath.Join(currentHome, "secret") || owner.ValidatorSecretFile != filepath.Join(namedHome, "validator-secret") || owner.ReportPath != filepath.Join(currentHome, "report.json") {
		t.Fatalf("customer path flags = prompt:%q secret:%q validator:%q report:%q, want expanded paths", owner.SystemPrompt, owner.SecretFile, owner.ValidatorSecretFile, owner.ReportPath)
	}
	if !equalStringSlices(gotArgs, []string{filepath.Join(namedHome, "positional.scenario.json")}) {
		t.Fatalf("customer positional scenario = %#v, want expanded named-home path", gotArgs)
	}
}

func TestRouterPreRunRejectsCustomerSimulationPathBeforeRunner(t *testing.T) {
	owner := NewCustomerSimulationCommand(flags.NewGlobalFlags())
	var calls int
	owner.SetRunner(func(context.Context, probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error) {
		calls++
		return probe.CustomerSimulationSuiteResult{}, nil
	})
	command := owner.Generate()
	root := newProbePathPreflightRoot(command, &pathResolver{
		currentHome: func() (string, error) { return t.TempDir(), nil },
		lookupUser:  func(name string) (string, error) { return "", errors.New("lookup failed for " + name) },
	})
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{
		"probe", "customer-simulation",
		"--live", "--family", "A",
		"--audio", "~/valid.raw",
		"--audio", "~missing/invalid.raw",
		"--report", "~/report.json",
	})

	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--audio") || !strings.Contains(err.Error(), "~missing/invalid.raw") {
		t.Fatalf("customer path error = %v, want flag and invalid input", err)
	}
	if calls != 0 || output.Len() != 0 {
		t.Fatalf("customer side effects after path failure = calls:%d output:%q", calls, output.String())
	}
}

func TestRouterPreRunNormalizesProbeFleetPaths(t *testing.T) {
	owner := NewProbeFleetCommand()
	command := owner.Generate()
	command.RunE = func(*cobra.Command, []string) error { return nil }
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	root := newProbePathPreflightRoot(command, testPathResolver(currentHome, namedHome))
	root.SetArgs([]string{"probe", "fleet", "--manifest", "~/fleet.json", "--replay", "~alice/replay"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("fleet preflight: %v", err)
	}
	if owner.ManifestPath != filepath.Join(currentHome, "fleet.json") || owner.Replay != filepath.Join(namedHome, "replay") {
		t.Fatalf("fleet paths = %q/%q, want expanded paths", owner.ManifestPath, owner.Replay)
	}
}

func TestRouterPreRunNormalizesAcceptanceExecutableButNotGoal(t *testing.T) {
	owner := NewProbeAcceptanceCommand()
	command := owner.Generate()
	var gotArgs []string
	command.RunE = func(_ *cobra.Command, args []string) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}
	currentHome := t.TempDir()
	namedHome := t.TempDir()
	root := newProbePathPreflightRoot(command, testPathResolver(currentHome, namedHome))
	root.SetArgs([]string{"probe", "accept", "~alice/bin/agent", "~literal goal"})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("acceptance preflight: %v", err)
	}
	if !equalStringSlices(gotArgs, []string{filepath.Join(namedHome, "bin", "agent"), "~literal goal"}) {
		t.Fatalf("acceptance args = %#v, want executable expanded and goal unchanged", gotArgs)
	}
}

func assertFlagString(t *testing.T, command *cobra.Command, name, want string) {
	t.Helper()
	got, err := command.Flags().GetString(name)
	if err != nil {
		t.Fatalf("get --%s: %v", name, err)
	}
	if got != want {
		t.Fatalf("--%s = %q, want %q", name, got, want)
	}
}
