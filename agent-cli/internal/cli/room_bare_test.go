package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

func TestRoomRunCommandBareInvocationPassesResolvedPlanToRunner(t *testing.T) {
	t.Setenv(services.DefaultRoomCredentialEnv, "fake-openai-key")
	registry := newBareRoomCLIRegistry(t)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "config")
	command := NewRoomRunCommandWithDeviceRegistry(globalFlags, registry)

	var got services.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		got = options
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	var output bytes.Buffer
	cmd := command.Generate()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--out", filepath.Join(t.TempDir(), "evidence")})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute bare room: %v", err)
	}
	if got.LaunchPlan == nil || got.LaunchPlan.Mode != services.RoomLaunchModeBare {
		t.Fatalf("launch plan = %+v, want bare plan", got.LaunchPlan)
	}
	if len(got.Manifest.Participants) != 2 || got.Manifest.Participants[0].Kind != room.ParticipantKindHuman || got.Manifest.Participants[1].Kind != room.ParticipantKindAgent {
		t.Fatalf("manifest participants = %+v, want human then agent", got.Manifest.Participants)
	}
	if got.Manifest.Participants[0].InputDevice != registry.input.ID || got.Manifest.Participants[0].OutputDevice != registry.output.ID {
		t.Fatalf("customer manifest devices = %q/%q", got.Manifest.Participants[0].InputDevice, got.Manifest.Participants[0].OutputDevice)
	}
	agent := got.LaunchPlan.Participants[1]
	if agent.Provider != "openai" || agent.Model != services.DefaultOpenAIRealtimeModel || agent.CredentialProvenance != services.RoomCredentialFromEnvironment {
		t.Fatalf("agent plan = %+v", agent)
	}
	if registry.defaultCalls != 2 || registry.openCalls != 0 {
		t.Fatalf("registry observations = defaults:%d opens:%d", registry.defaultCalls, registry.openCalls)
	}
	if cmd.Flags().Changed("config") || cmd.Flags().Changed("manifest") {
		t.Fatal("bare invocation unexpectedly selected a config path")
	}
	if !strings.Contains(output.String(), "room starting: participants=2") {
		t.Fatalf("output = %q, want startup summary", output.String())
	}
}

func TestRoomRunCommandBareDefaultRecordingUsesFreshConfigDirectoryOnEveryRun(t *testing.T) {
	t.Setenv(services.DefaultRoomCredentialEnv, "fake-openai-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "config")
	command := NewRoomRunCommandWithDeviceRegistry(globalFlags, newBareRoomCLIRegistry(t))
	var outputDirs []string
	command.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		outputDirs = append(outputDirs, options.OutputDir)
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})

	var output bytes.Buffer
	first := command.Generate()
	first.SetOut(&output)
	if err := first.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("first bare room run: %v", err)
	}
	second := command.Generate()
	second.SetOut(&output)
	if err := second.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("second bare room run: %v", err)
	}

	if len(outputDirs) != 2 || outputDirs[0] == "" || outputDirs[1] == "" || outputDirs[0] == outputDirs[1] {
		t.Fatalf("default output directories = %q, want two distinct fresh directories", outputDirs)
	}
	configDir := globalFlags.ConfigDir()
	for _, outputDir := range outputDirs {
		if filepath.Dir(outputDir) != configDir || !strings.HasPrefix(filepath.Base(outputDir), "room-run-") {
			t.Fatalf("default output directory %q is not a fresh config child under %q", outputDir, configDir)
		}
		if info, err := os.Stat(outputDir); err != nil || !info.IsDir() {
			t.Fatalf("default output directory %q stat = %v/%v, want directory", outputDir, info, err)
		}
	}
	if !strings.Contains(output.String(), "room starting: participants=2 output="+outputDirs[0]) || !strings.Contains(output.String(), "output="+outputDirs[1]) {
		t.Fatalf("startup output = %q, want both non-secret run paths", output.String())
	}
}

func TestRoomRunCommandConfigRecordingPolicyIsAuthoritative(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "recording-policy.json")
	data := []byte(`{
  "schema_version": 1,
  "room": {"max_turns": 1, "recording": {"enabled": false}},
  "participants": [
    {"id": "alice", "system_prompt": "Alice", "opening_prompt": "Start the room.", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_POLICY_ALICE_KEY", "tools": []},
    {"id": "bob", "system_prompt": "Bob", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_POLICY_BOB_KEY", "tools": []}
  ]
}`)
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatalf("write recording policy manifest: %v", err)
	}
	t.Setenv("ROOM_POLICY_ALICE_KEY", "alice-secret")
	t.Setenv("ROOM_POLICY_BOB_KEY", "bob-secret")
	command := NewRoomRunCommandWithDeviceRegistry(flags.NewGlobalFlags(), newBareRoomCLIRegistry(t))
	var got services.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		got = options
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", manifestPath, "--out", filepath.Join(t.TempDir(), "ignored-explicit-output")})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute recording-disabled room: %v", err)
	}
	if got.OutputDir != "" || got.Manifest.Room.RecordingEnabled() {
		t.Fatalf("recording-disabled options = output:%q policy:%+v, want no evidence", got.OutputDir, got.Manifest.Room.Recording)
	}
}

func TestRoomRunCommandConfigRecordingDestinationIsAuthoritative(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "recording-destination.json")
	destination := filepath.Join(t.TempDir(), "configured-room-evidence")
	data := []byte(fmt.Sprintf(`{
  "schema_version": 1,
  "room": {"max_turns": 1, "recording": {"directory": %q}},
  "participants": [
    {"id": "alice", "system_prompt": "Alice", "opening_prompt": "Start the room.", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_DEST_ALICE_KEY", "tools": []},
    {"id": "bob", "system_prompt": "Bob", "provider": "openai", "model": "gpt-realtime", "api_key_env": "ROOM_DEST_BOB_KEY", "tools": []}
  ]
}`, destination))
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatalf("write recording destination manifest: %v", err)
	}
	t.Setenv("ROOM_DEST_ALICE_KEY", "alice-secret")
	t.Setenv("ROOM_DEST_BOB_KEY", "bob-secret")
	command := NewRoomRunCommandWithDeviceRegistry(flags.NewGlobalFlags(), newBareRoomCLIRegistry(t))
	var got services.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		got = options
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", manifestPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute configured-destination room: %v", err)
	}
	if got.OutputDir != destination {
		t.Fatalf("configured recording destination = %q, want %q", got.OutputDir, destination)
	}
}

func TestRoomRunCommandUsesInjectedSignalCancellation(t *testing.T) {
	t.Setenv(services.DefaultRoomCredentialEnv, "fake-openai-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "config")
	command := NewRoomRunCommandWithDeviceRegistry(globalFlags, newBareRoomCLIRegistry(t))
	runnerStarted := make(chan struct{})
	interrupt := make(chan os.Signal, 1)
	var stopCalls int
	command.SetSignalContextFactory(func(parent context.Context) (context.Context, func()) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			select {
			case signal := <-interrupt:
				if signal == os.Interrupt {
					cancel()
				}
			case <-ctx.Done():
			}
		}()
		return ctx, func() {
			stopCalls++
			cancel()
		}
	})
	command.SetRunner(func(ctx context.Context, _ io.Writer, _ services.RoomRunOptions) (services.RoomResult, error) {
		close(runnerStarted)
		<-ctx.Done()
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs(nil)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(context.Background()) }()
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("room runner did not start")
	}
	// The injected signal source represents one SIGINT after readiness.
	interrupt <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("room command with injected cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("room command did not return after injected cancellation")
	}
	if stopCalls != 1 {
		t.Fatalf("signal cleanup calls = %d, want one", stopCalls)
	}
}

func TestRoomRunCommandBareMissingCredentialDoesNotCallRunnerOrOpenDevices(t *testing.T) {
	t.Setenv(services.DefaultRoomCredentialEnv, "")
	registry := newBareRoomCLIRegistry(t)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "config")
	command := NewRoomRunCommandWithDeviceRegistry(globalFlags, registry)
	var runnerCalls int
	command.SetRunner(func(context.Context, io.Writer, services.RoomRunOptions) (services.RoomResult, error) {
		runnerCalls++
		return services.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--out", filepath.Join(t.TempDir(), "evidence")})
	err := cmd.ExecuteContext(context.Background())
	// `room run` accepts no --api-key flag, so its missing-credential error
	// must recommend only a remedy the command actually accepts: the
	// environment variable, never --api-key (which `room run` would then
	// reject as an unknown flag, sending the user in a circle).
	if err == nil || !strings.Contains(err.Error(), services.DefaultRoomCredentialEnv) {
		t.Fatalf("error = %v, want it to name %s", err, services.DefaultRoomCredentialEnv)
	}
	if err != nil && strings.Contains(err.Error(), "--api-key") {
		t.Fatalf("error = %v, want no --api-key mention: room run has no such flag", err)
	}
	if runnerCalls != 0 || registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("side effects runner=%d defaults=%d opens=%d", runnerCalls, registry.defaultCalls, registry.openCalls)
	}
}

func TestRoomRunCommandConfigAliasRemainsAuthoritative(t *testing.T) {
	manifestPath := writeRoomCLIManifest(t)
	registry := newBareRoomCLIRegistry(t)
	command := NewRoomRunCommandWithDeviceRegistry(flags.NewGlobalFlags(), registry)
	var got services.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		got = options
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", manifestPath, "--out", filepath.Join(t.TempDir(), "evidence")})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute configured room: %v", err)
	}
	if got.LaunchPlan == nil || got.LaunchPlan.Mode != services.RoomLaunchModeConfigured || got.LaunchPlan.ConfigPath != manifestPath {
		t.Fatalf("configured launch plan = %+v", got.LaunchPlan)
	}
	if len(got.Manifest.Participants) != 2 || got.Manifest.Participants[0].ID != "alice" || got.Manifest.Participants[1].ID != "bob" {
		t.Fatalf("configured manifest = %+v", got.Manifest.Participants)
	}
	if registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("configured invocation resolved bare devices: defaults=%d opens=%d", registry.defaultCalls, registry.openCalls)
	}
}

func TestRoomRunCommandConfigAcceptsYAMLAndPreservesCompleteDefinition(t *testing.T) {
	t.Setenv("ROOM_YAML_ALPHA_KEY", "yaml-alpha-secret")
	t.Setenv("ROOM_YAML_BETA_KEY", "yaml-beta-secret")
	configPath := filepath.Join(t.TempDir(), "room.yaml")
	configData := []byte(`schema_version: 1
room:
  max_turns: 4
  max_duration: 21s
participants:
  - id: yaml-alpha
    system_prompt: "YAML alpha"
    opening_prompt: "Start the room."
    provider: openai
    model: gpt-realtime-2.1-mini
    api_key_env: ROOM_YAML_ALPHA_KEY
    voice: cedar
    input_device: fake:configured-input
    output_device: fake:configured-output
    tools: []
  - id: yaml-beta
    system_prompt: "YAML beta"
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_YAML_BETA_KEY
    voice: ash
    tools: []
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write YAML room config: %v", err)
	}
	registry := newBareRoomCLIRegistry(t)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "config")
	command := NewRoomRunCommandWithDeviceRegistry(globalFlags, registry)
	var got services.RoomRunOptions
	command.SetRunner(func(_ context.Context, _ io.Writer, options services.RoomRunOptions) (services.RoomResult, error) {
		got = options
		return services.RoomResult{TerminationReason: services.RoomTerminationStopped}, nil
	})
	outputDir := filepath.Join(t.TempDir(), "configured-evidence")
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", configPath, "--out", outputDir})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute YAML configured room: %v", err)
	}
	if got.LaunchPlan == nil || got.LaunchPlan.Mode != services.RoomLaunchModeConfigured || got.LaunchPlan.ConfigPath != configPath {
		t.Fatalf("configured launch plan = %+v, want exact YAML config path", got.LaunchPlan)
	}
	if got.OutputDir != outputDir || got.ConfigDir != globalFlags.ConfigDir() {
		t.Fatalf("configured recording/config options = %q/%q, want %q/%q", got.OutputDir, got.ConfigDir, outputDir, globalFlags.ConfigDir())
	}
	if got.Manifest.Room.MaxTurns != 4 || got.Manifest.Room.MaxDuration != 21*time.Second || got.Manifest.Room.Interactive {
		t.Fatalf("configured bounds = %+v, want max_turns=4 max_duration=21s and non-interactive", got.Manifest.Room)
	}
	if len(got.Manifest.Participants) != 2 || got.Manifest.Participants[0].ID != "yaml-alpha" || got.Manifest.Participants[1].ID != "yaml-beta" {
		t.Fatalf("configured participants = %+v, want exact YAML participants", got.Manifest.Participants)
	}
	if got.Manifest.Participants[0].InputDevice != "fake:configured-input" || got.Manifest.Participants[0].OutputDevice != "fake:configured-output" || got.Manifest.Participants[1].Model != "gpt-realtime" {
		t.Fatalf("configured device/model choices = %+v, want exact YAML values", got.Manifest.Participants)
	}
	if registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("configured YAML invocation resolved bare devices: defaults=%d opens=%d", registry.defaultCalls, registry.openCalls)
	}
}

func TestRoomRunCommandConfigValidationPrecedesRunnerAndDeviceLookup(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "invalid-room.json")
	if err := os.WriteFile(configPath, []byte(`{"schema_version":1,"room":{"max_turns":1},"participants":[]}`), 0o600); err != nil {
		t.Fatalf("write invalid room config: %v", err)
	}
	registry := newBareRoomCLIRegistry(t)
	command := NewRoomRunCommandWithDeviceRegistry(flags.NewGlobalFlags(), registry)
	var runnerCalls int
	command.SetRunner(func(context.Context, io.Writer, services.RoomRunOptions) (services.RoomResult, error) {
		runnerCalls++
		return services.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", configPath, "--out", filepath.Join(t.TempDir(), "out")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "participants") {
		t.Fatalf("error = %v, want config participant validation error", err)
	}
	if runnerCalls != 0 || registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("invalid config caused startup work: runner=%d defaults=%d opens=%d", runnerCalls, registry.defaultCalls, registry.openCalls)
	}
}

func TestRoomRunCommandConfiguredHumanDeviceValidationPrecedesRunner(t *testing.T) {
	registry := newBareRoomCLIRegistry(t)
	configPath := filepath.Join(t.TempDir(), "invalid-human-room.yaml")
	configData := fmt.Sprintf(
		"schema_version: 1\n"+
			"room:\n"+
			"  max_turns: 1\n"+
			"participants:\n"+
			"  - kind: human\n"+
			"    id: customer\n"+
			"    system_prompt: Human customer\n"+
			"    input_device: %s\n"+
			"    output_device: %s\n"+
			"    tools: []\n"+
			"  - id: agent\n"+
			"    system_prompt: Provider agent\n"+
			"    provider: openai\n"+
			"    model: gpt-realtime-2.1-mini\n"+
			"    api_key_env: ROOM_CONFIG_AGENT_KEY\n"+
			"    tools: []\n",
		registry.output.ID, registry.output.ID,
	)
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatalf("write invalid human room: %v", err)
	}
	t.Setenv("ROOM_CONFIG_AGENT_KEY", "configured-agent-secret")
	command := NewRoomRunCommandWithDeviceRegistry(flags.NewGlobalFlags(), registry)
	var runnerCalls int
	command.SetRunner(func(context.Context, io.Writer, services.RoomRunOptions) (services.RoomResult, error) {
		runnerCalls++
		return services.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", configPath, "--out", filepath.Join(t.TempDir(), "evidence")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "participants[0].input_device") {
		t.Fatalf("error = %v, want field-specific configured input-device error", err)
	}
	if !errors.Is(err, audio.ErrDeviceDirectionMismatch) {
		t.Fatalf("error = %v, want direction mismatch", err)
	}
	if runnerCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("invalid human device caused startup work: runner=%d opens=%d", runnerCalls, registry.openCalls)
	}
}

func TestRoomRunCommandRejectsConflictingConfigAndManifestPathsBeforeRunner(t *testing.T) {
	first := writeRoomCLIManifest(t)
	second := writeRoomCLIManifest(t)
	command := NewRoomRunCommandWithDeviceRegistry(flags.NewGlobalFlags(), newBareRoomCLIRegistry(t))
	var runnerCalls int
	command.SetRunner(func(context.Context, io.Writer, services.RoomRunOptions) (services.RoomResult, error) {
		runnerCalls++
		return services.RoomResult{}, nil
	})
	cmd := command.Generate()
	cmd.SetArgs([]string{"--config", first, "--manifest", second, "--out", filepath.Join(t.TempDir(), "evidence")})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, services.ErrRoomLaunchPathConflict) {
		t.Fatalf("error = %v, want conflicting-path error", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("runner calls = %d, want zero", runnerCalls)
	}
}

type bareRoomCLIRegistry struct {
	input, output audio.Device
	defaultCalls  int
	openCalls     int
}

func newBareRoomCLIRegistry(t *testing.T) *bareRoomCLIRegistry {
	t.Helper()
	input, err := audio.NewDevice("fake", "input", "Fake Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatalf("new fake input: %v", err)
	}
	output, err := audio.NewDevice("fake", "output", "Fake Speakers", audio.DirectionOutput)
	if err != nil {
		t.Fatalf("new fake output: %v", err)
	}
	return &bareRoomCLIRegistry{input: input, output: output}
}

func (r *bareRoomCLIRegistry) List() ([]audio.Device, error) {
	return []audio.Device{r.input, r.output}, nil
}

func (r *bareRoomCLIRegistry) Default(direction audio.Direction) (audio.Device, error) {
	r.defaultCalls++
	switch direction {
	case audio.DirectionInput:
		return r.input, nil
	case audio.DirectionOutput:
		return r.output, nil
	default:
		return audio.Device{}, audio.NewNoDefaultDeviceError(direction)
	}
}

func (r *bareRoomCLIRegistry) Open(id audio.DeviceID) (audio.OpenedDevice, error) {
	r.openCalls++
	return nil, audio.NewDeviceNotFoundError(id)
}

var _ audio.DeviceRegistry = (*bareRoomCLIRegistry)(nil)
