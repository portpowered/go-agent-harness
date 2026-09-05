package agentruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestResolveBareRoomLaunchPlanUsesDefaultDevicesAndSharedOpenAIResolution(t *testing.T) {
	input, err := devicegw.NewDevice("fake", "input", "Fake Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	output, err := devicegw.NewDevice("fake", "output", "Fake Speakers", devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaults: map[devicegw.Direction]devicegw.Device{
		devicegw.DirectionInput: input, devicegw.DirectionOutput: output,
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
	// `room run` has no --api-key flag, so its missing-credential error must
	// direct the user to the environment variable it actually accepts
	// instead of the shared session-config helper's --api-key wording (that
	// wording is correct for `session`/`ask`/`chat`, which do accept the
	// flag, but sends a `room run` user in a circle).
	if err == nil || !strings.Contains(err.Error(), DefaultRoomCredentialEnv) {
		t.Fatalf("missing credential error = %v, want it to name %s", err, DefaultRoomCredentialEnv)
	}
	if err != nil && strings.Contains(err.Error(), "--api-key") {
		t.Fatalf("missing credential error = %v, want no --api-key mention: room run does not accept that flag", err)
	}
	if registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("missing credential touched devices: defaults=%d opens=%d", registry.defaultCalls, registry.openCalls)
	}
}

func TestResolveBareRoomLaunchPlanUsesConfigCredentialWhenEnvironmentIsUnset(t *testing.T) {
	input, err := devicegw.NewDevice("fake", "input", "Fake Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	output, err := devicegw.NewDevice("fake", "output", "Fake Speakers", devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaults: map[devicegw.Direction]devicegw.Device{
		devicegw.DirectionInput: input, devicegw.DirectionOutput: output,
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
	input, err := devicegw.NewDevice("fake", "input", "Fake Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaultErr: map[devicegw.Direction]error{
		devicegw.DirectionOutput: devicegw.NewNoDefaultDeviceError(devicegw.DirectionOutput),
	}, defaults: map[devicegw.Direction]devicegw.Device{
		devicegw.DirectionInput: input,
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
	if !errors.As(err, &deviceErr) || deviceErr.Direction != devicegw.DirectionOutput {
		t.Fatalf("error = %v, want output RoomLaunchDeviceError", err)
	}
	if registry.openCalls != 0 {
		t.Fatalf("device planning opened %d devices, want zero", registry.openCalls)
	}
}

func TestResolveBareRoomLaunchPlanRejectsWrongDirectionalDefaultBeforeAcquisition(t *testing.T) {
	output, err := devicegw.NewDevice("fake", "output", "Fake Speakers", devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}
	registry := &roomLaunchTestRegistry{defaults: map[devicegw.Direction]devicegw.Device{
		devicegw.DirectionInput:  output,
		devicegw.DirectionOutput: output,
	}}
	_, err = ResolveBareRoomLaunchPlan(RoomLaunchOptions{
		DeviceRegistry: registry,
		LoadedConfig:   &config.Config{},
		CredentialLookup: func(string) (string, bool) {
			return "fake-openai-key", true
		},
	})
	if err == nil || !errors.Is(err, devicegw.ErrDeviceDirectionMismatch) {
		t.Fatalf("wrong-direction default error = %v, want direction mismatch", err)
	}
	var deviceErr *RoomLaunchDeviceError
	if !errors.As(err, &deviceErr) || deviceErr.Direction != devicegw.DirectionInput {
		t.Fatalf("error = %v, want input RoomLaunchDeviceError", err)
	}
	if registry.defaultCalls != 1 || registry.openCalls != 0 {
		t.Fatalf("wrong-direction planning calls = defaults:%d opens:%d, want 1/0", registry.defaultCalls, registry.openCalls)
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
    opening_prompt: "Start the configured room."
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

func TestResolveRoomLaunchPlanConfiguredHumanDevicesAreValidatedBeforeAcquisition(t *testing.T) {
	input, err := devicegw.NewDevice("fake", "input", "Fake Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	output, err := devicegw.NewDevice("fake", "output", "Fake Speakers", devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}

	tests := []struct {
		name           string
		inputSelector  string
		outputSelector string
		field          string
		cause          error
		wantValid      bool
		wantListCalls  int
	}{
		{
			name:           "missing input selector",
			outputSelector: output.ID,
			field:          "participants[0].input_device",
			cause:          room.ErrInvalidParticipant,
			wantListCalls:  0,
		},
		{
			name:          "missing output selector",
			inputSelector: input.ID,
			field:         "participants[0].output_device",
			cause:         room.ErrInvalidParticipant,
			wantListCalls: 0,
		},
		{
			name:           "missing input device",
			inputSelector:  "fake:missing-input",
			outputSelector: output.ID,
			field:          "participants[0].input_device",
			cause:          devicegw.ErrDeviceNotFound,
			wantListCalls:  1,
		},
		{
			name:           "wrong input direction",
			inputSelector:  output.ID,
			outputSelector: output.ID,
			field:          "participants[0].input_device",
			cause:          devicegw.ErrDeviceDirectionMismatch,
			wantListCalls:  1,
		},
		{
			name:           "valid exact IDs",
			inputSelector:  input.ID,
			outputSelector: output.ID,
			wantValid:      true,
			wantListCalls:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "room.yaml")
			inputLine := ""
			if test.inputSelector != "" {
				inputLine = fmt.Sprintf("\n    input_device: %s", test.inputSelector)
			}
			outputLine := ""
			if test.outputSelector != "" {
				outputLine = fmt.Sprintf("\n    output_device: %s", test.outputSelector)
			}
			data := []byte(fmt.Sprintf(
				"schema_version: 1\n"+
					"room:\n"+
					"  max_turns: 1\n"+
					"participants:\n"+
					"  - kind: human\n"+
					"    id: customer\n"+
					"    system_prompt: \"Human customer\"%s%s\n"+
					"    tools: []\n"+
					"  - id: agent\n"+
					"    system_prompt: \"Provider agent\"\n"+
					"    provider: openai\n"+
					"    model: gpt-realtime-2.1-mini\n"+
					"    api_key_env: ROOM_AGENT_KEY\n"+
					"    tools: []\n",
				inputLine, outputLine))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write room: %v", err)
			}
			registry := &roomLaunchTestRegistry{defaults: map[devicegw.Direction]devicegw.Device{
				devicegw.DirectionInput: input, devicegw.DirectionOutput: output,
			}}
			plan, err := ResolveRoomLaunchPlan(RoomLaunchOptions{
				ConfigPath:     path,
				DeviceRegistry: registry,
				CredentialLookup: func(name string) (string, bool) {
					return "configured-secret", name == "ROOM_AGENT_KEY"
				},
			})
			if test.wantValid {
				if err != nil {
					t.Fatalf("resolve valid configured human room: %v", err)
				}
				customer, ok := plan.Participant("customer")
				if !ok || customer.InputDevice.ID != input.ID || customer.OutputDevice.ID != output.ID {
					t.Fatalf("resolved customer devices = %+v, want exact IDs %q/%q", customer, input.ID, output.ID)
				}
			} else {
				if err == nil {
					t.Fatal("resolve invalid configured human room succeeded")
				}
				if !strings.Contains(err.Error(), test.field) {
					t.Fatalf("error = %v, want field %q", err, test.field)
				}
				if !errors.Is(err, test.cause) {
					t.Fatalf("error = %v, want errors.Is(%v)", err, test.cause)
				}
			}
			if registry.listCalls != test.wantListCalls || registry.openCalls != 0 {
				t.Fatalf("registry calls = list:%d open:%d, want list:%d open:0", registry.listCalls, registry.openCalls, test.wantListCalls)
			}
		})
	}
}

type roomLaunchTestRegistry struct {
	defaults     map[devicegw.Direction]devicegw.Device
	defaultErr   map[devicegw.Direction]error
	defaultCalls int
	listCalls    int
	openCalls    int
}

func (r *roomLaunchTestRegistry) List() ([]devicegw.Device, error) {
	r.listCalls++
	devices := make([]devicegw.Device, 0, len(r.defaults))
	for _, device := range r.defaults {
		devices = append(devices, device)
	}
	return devices, nil
}

func (r *roomLaunchTestRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	r.defaultCalls++
	if err := r.defaultErr[direction]; err != nil {
		return devicegw.Device{}, err
	}
	device, ok := r.defaults[direction]
	if !ok {
		return devicegw.Device{}, devicegw.NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

func (r *roomLaunchTestRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	r.openCalls++
	return nil, devicegw.NewDeviceNotFoundError(id)
}

var _ devicegw.DeviceRegistry = (*roomLaunchTestRegistry)(nil)
