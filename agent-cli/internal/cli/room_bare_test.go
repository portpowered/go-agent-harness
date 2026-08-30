package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

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
	if err == nil || !strings.Contains(err.Error(), "OpenAI API key is required for live realtime session mode") {
		t.Fatalf("error = %v, want shared OpenAI credential error", err)
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
