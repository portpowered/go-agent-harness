package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

func TestResolveBareRoomLaunchPlanUsesDefaultDevicesAndSharedOpenAIResolution(t *testing.T) {
	input, err := audio.NewDevice("fake", "input", "Fake Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	output, err := audio.NewDevice("fake", "output", "Fake Speakers", audio.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaults: map[audio.Direction]audio.Device{
		audio.DirectionInput: input, audio.DirectionOutput: output,
	}}
	loaded := &config.Config{Model: config.ModelConfig{Provider: config.ProviderOpenRouter}}
	plan, err := ResolveBareRoomLaunchPlan(RoomLaunchOptions{
		ConfigDir:      "/tmp/room-launch-config",
		DeviceRegistry: registry,
		LoadedConfig:   loaded,
		CredentialLookup: func(name string) (string, bool) {
			if name == DefaultRoomCredentialEnv {
				return "fake-openai-key", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("resolve bare launch plan: %v", err)
	}
	if plan.Mode != RoomLaunchModeBare || len(plan.Participants) != 2 {
		t.Fatalf("plan mode/participants = %q/%d, want bare/2", plan.Mode, len(plan.Participants))
	}
	if plan.Manifest.Room != (room.Room{Interactive: true}) {
		t.Fatalf("room = %+v, want interactive unbounded room", plan.Manifest.Room)
	}
	customer, ok := plan.Participant(DefaultRoomCustomerID)
	if !ok || customer.Kind != room.ParticipantKindHuman {
		t.Fatalf("customer plan = %+v, want human customer", customer)
	}
	if customer.InputDevice.ID != input.ID || customer.OutputDevice.ID != output.ID {
		t.Fatalf("customer devices = %q/%q, want %q/%q", customer.InputDevice.ID, customer.OutputDevice.ID, input.ID, output.ID)
	}
	agent, ok := plan.Participant(DefaultRoomAgentID)
	if !ok || agent.Kind != room.ParticipantKindAgent {
		t.Fatalf("agent plan = %+v, want provider agent", agent)
	}
	if agent.Provider != config.ProviderOpenAI || agent.Model != DefaultOpenAIRealtimeModel {
		t.Fatalf("agent provider/model = %q/%q, want %q/%q", agent.Provider, agent.Model, config.ProviderOpenAI, DefaultOpenAIRealtimeModel)
	}
	if agent.CredentialReference != DefaultRoomCredentialEnv || agent.CredentialProvenance != RoomCredentialFromEnvironment {
		t.Fatalf("agent credential metadata = %q/%q", agent.CredentialReference, agent.CredentialProvenance)
	}
	if registry.openCalls != 0 {
		t.Fatalf("bare planning opened %d devices, want zero", registry.openCalls)
	}
	if registry.defaultCalls != 2 {
		t.Fatalf("bare planning default calls = %d, want 2", registry.defaultCalls)
	}
	if err := plan.Manifest.Validate(room.ValidationOptions{LookupCredential: func(name string) (string, bool) {
		return "fake-openai-key", name == DefaultRoomCredentialEnv
	}}); err != nil {
		t.Fatalf("synthesized manifest validation: %v", err)
	}
}

func TestResolveBareRoomLaunchPlanMissingCredentialFailsBeforeDeviceAcquisition(t *testing.T) {
	registry := &roomLaunchTestRegistry{}
	_, err := ResolveBareRoomLaunchPlan(RoomLaunchOptions{
		DeviceRegistry: registry,
		LoadedConfig:   &config.Config{},
		CredentialLookup: func(string) (string, bool) {
			return "", false
		},
	})
	if err == nil || !strings.Contains(err.Error(), "OpenAI API key is required for live realtime session mode") {
		t.Fatalf("missing credential error = %v", err)
	}
	if registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("missing credential touched devices: defaults=%d opens=%d", registry.defaultCalls, registry.openCalls)
	}
}

func TestResolveBareRoomLaunchPlanUsesConfigCredentialWhenEnvironmentIsUnset(t *testing.T) {
	input, err := audio.NewDevice("fake", "input", "Fake Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	output, err := audio.NewDevice("fake", "output", "Fake Speakers", audio.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaults: map[audio.Direction]audio.Device{
		audio.DirectionInput: input, audio.DirectionOutput: output,
	}}
	plan, err := ResolveBareRoomLaunchPlan(RoomLaunchOptions{
		DeviceRegistry: registry,
		LoadedConfig: &config.Config{Model: config.ModelConfig{
			Provider: config.ProviderOpenAI,
			OpenAI:   &config.OpenAIConfig{APIKey: "config-only-key", Model: "ignored-by-bare-default"},
		}},
		CredentialLookup: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("resolve config-backed bare plan: %v", err)
	}
	agent, ok := plan.Participant(DefaultRoomAgentID)
	if !ok || agent.CredentialProvenance != RoomCredentialFromConfig || agent.Model != DefaultOpenAIRealtimeModel {
		t.Fatalf("agent config-backed plan = %+v", agent)
	}
}

func TestResolveBareRoomLaunchPlanReportsDirectionalDefaultFailure(t *testing.T) {
	input, err := audio.NewDevice("fake", "input", "Fake Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaultErr: map[audio.Direction]error{
		audio.DirectionOutput: audio.NewNoDefaultDeviceError(audio.DirectionOutput),
	}, defaults: map[audio.Direction]audio.Device{
		audio.DirectionInput: input,
	}}
	_, err = ResolveBareRoomLaunchPlan(RoomLaunchOptions{
		DeviceRegistry: registry,
		LoadedConfig:   &config.Config{},
		CredentialLookup: func(string) (string, bool) {
			return "fake-openai-key", true
		},
	})
	if err == nil || !strings.Contains(err.Error(), "customer output device") || !strings.Contains(err.Error(), "agent devices list") {
		t.Fatalf("default output error = %v", err)
	}
	var deviceErr *RoomLaunchDeviceError
	if !errors.As(err, &deviceErr) || deviceErr.Direction != audio.DirectionOutput {
		t.Fatalf("error = %v, want output RoomLaunchDeviceError", err)
	}
	if registry.openCalls != 0 {
		t.Fatalf("device planning opened %d devices, want zero", registry.openCalls)
	}
}

func TestResolveRoomLaunchPlanConfiguredIsAuthoritativeAndUsesInjectedCredentialLookup(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(t.TempDir(), "room.yaml")
	data := []byte(`schema_version: 1
room:
  max_turns: 7
  max_duration: 13s
participants:
  - id: alpha-configured
    system_prompt: "Alpha configured persona"
    provider: openai
    model: gpt-realtime-2.1-mini
    api_key_env: ROOM_ALPHA_CONFIGURED_KEY
    voice: cedar
    input_device: fake:alpha-input
    output_device: fake:alpha-output
    tools: []
  - id: beta-configured
    system_prompt: "Beta configured persona"
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_BETA_CONFIGURED_KEY
    voice: ash
    input_device: fake:beta-input
    output_device: fake:beta-output
    tools: []
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write configured room: %v", err)
	}
	registry := &roomLaunchTestRegistry{}
	loadCalls := 0
	plan, err := ResolveRoomLaunchPlan(RoomLaunchOptions{
		ConfigPath:     path,
		ConfigDir:      configDir,
		DeviceRegistry: registry,
		LoadConfig: func() (*config.Config, error) {
			loadCalls++
			return nil, errors.New("configured room must not load bare defaults")
		},
		CredentialLookup: func(name string) (string, bool) {
			return "configured-" + name, name == "ROOM_ALPHA_CONFIGURED_KEY" || name == "ROOM_BETA_CONFIGURED_KEY"
		},
	})
	if err != nil {
		t.Fatalf("resolve configured room: %v", err)
	}
	if plan.Mode != RoomLaunchModeConfigured || plan.ConfigPath != path || plan.ConfigDir != configDir {
		t.Fatalf("launch metadata = %+v, want configured path and config dir preserved", plan)
	}
	if plan.Manifest.Room.MaxTurns != 7 || plan.Manifest.Room.MaxDuration != 13*time.Second || plan.Manifest.Room.Interactive {
		t.Fatalf("configured room bounds = %+v, want max_turns=7 max_duration=13s and non-interactive", plan.Manifest.Room)
	}
	if len(plan.Manifest.Participants) != 2 || plan.Manifest.Participants[0].ID != "alpha-configured" || plan.Manifest.Participants[1].ID != "beta-configured" {
		t.Fatalf("configured participants = %+v, want exact file participants", plan.Manifest.Participants)
	}
	if plan.Manifest.Participants[0].InputDevice != "fake:alpha-input" || plan.Manifest.Participants[0].OutputDevice != "fake:alpha-output" || plan.Manifest.Participants[1].Model != "gpt-realtime" {
		t.Fatalf("configured participant choices = %+v, want exact device/model values", plan.Manifest.Participants)
	}
	for _, id := range []string{"alpha-configured", "beta-configured"} {
		participant, ok := plan.Participant(id)
		if !ok || participant.Kind != room.ParticipantKindAgent || participant.CredentialProvenance != RoomCredentialFromEnvironment {
			t.Fatalf("configured participant plan %q = %+v, want agent with injected credential provenance", id, participant)
		}
	}
	if loadCalls != 0 || registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("configured resolution triggered bare startup work: load=%d defaults=%d opens=%d", loadCalls, registry.defaultCalls, registry.openCalls)
	}
}

type roomLaunchTestRegistry struct {
	defaults     map[audio.Direction]audio.Device
	defaultErr   map[audio.Direction]error
	defaultCalls int
	openCalls    int
}

func (r *roomLaunchTestRegistry) List() ([]audio.Device, error) {
	devices := make([]audio.Device, 0, len(r.defaults))
	for _, device := range r.defaults {
		devices = append(devices, device)
	}
	return devices, nil
}

func (r *roomLaunchTestRegistry) Default(direction audio.Direction) (audio.Device, error) {
	r.defaultCalls++
	if err := r.defaultErr[direction]; err != nil {
		return audio.Device{}, err
	}
	device, ok := r.defaults[direction]
	if !ok {
		return audio.Device{}, audio.NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

func (r *roomLaunchTestRegistry) Open(id audio.DeviceID) (audio.OpenedDevice, error) {
	r.openCalls++
	return nil, audio.NewDeviceNotFoundError(id)
}

var _ audio.DeviceRegistry = (*roomLaunchTestRegistry)(nil)
