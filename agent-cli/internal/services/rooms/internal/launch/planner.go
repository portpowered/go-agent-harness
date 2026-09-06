// Package launch resolves CLI room paths and host device selectors before the
// runtime service is admitted. It is intentionally side effect free with
// respect to providers and device opens.
package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

const (
	defaultCustomerID     = "customer"
	defaultAgentID        = "agent"
	defaultCredentialEnv  = "AGENT_MODEL__OPENAI__API_KEY"
	defaultRealtimeModel  = "gpt-realtime-2.1-mini"
	defaultCustomerPrompt = "You are the human customer in a live room. Speak naturally and briefly."
	defaultAgentPrompt    = "You are the room's helpful OpenAI realtime agent. Speak naturally and briefly."
)

var errDeviceRegistry = errors.New("bare room launch requires an audio device registry")

// Planner owns the CLI-only concerns needed to turn a command path into a
// public room plan. The returned plan contains selectors and metadata only;
// no OpenedDevice is retained.
type Planner struct {
	registry devicegw.DeviceRegistry
}

func NewPlanner(registry devicegw.DeviceRegistry) *Planner { return &Planner{registry: registry} }

func (p *Planner) Resolve(options runtimeRooms.RoomLaunchOptions) (runtimeRooms.RoomLaunchPlan, error) {
	path, err := sourcePath(options.ConfigPath, options.ManifestPath)
	if err != nil {
		return runtimeRooms.RoomLaunchPlan{}, err
	}
	lookup := options.CredentialLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if path != "" {
		return p.configured(path, options.ConfigDir, lookup)
	}
	return p.bare(options.ConfigDir, lookup)
}

func (p *Planner) configured(path, configDir string, lookup func(string) (string, bool)) (runtimeRooms.RoomLaunchPlan, error) {
	manifest, err := room.ReadManifest(path, room.ValidationOptions{LookupCredential: lookup})
	if err != nil {
		return runtimeRooms.RoomLaunchPlan{}, fmt.Errorf("validate room config: %w", err)
	}
	devices, err := p.humanDevices(manifest)
	if err != nil {
		return runtimeRooms.RoomLaunchPlan{}, fmt.Errorf("validate room config: %w", err)
	}
	if strings.TrimSpace(configDir) == "" {
		configDir = filepath.Dir(path)
	}
	participants := make([]runtimeRooms.RoomLaunchParticipantPlan, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
		kind := normalizeParticipantKind(participant.Kind)
		plan := runtimeRooms.RoomLaunchParticipantPlan{ID: participant.ID, Kind: runtimeRooms.ParticipantKind(kind), Provider: participant.Provider, Model: participant.Model, CredentialReference: participant.APIKeyEnv}
		if selected, ok := devices[participant.ID]; ok {
			plan.InputDevice, plan.OutputDevice = selected.input, selected.output
		}
		if participant.APIKeyEnv != "" {
			if value, ok := lookup(participant.APIKeyEnv); ok && strings.TrimSpace(value) != "" {
				plan.CredentialProvenance = "environment"
			}
		}
		participants = append(participants, plan)
	}
	return runtimeRooms.RoomLaunchPlan{Mode: runtimeRooms.RoomLaunchModeConfigured, ConfigPath: path, ConfigDir: configDir, Manifest: toRuntimeManifest(manifest), Participants: participants}, nil
}

func (p *Planner) bare(configDir string, lookup func(string) (string, bool)) (runtimeRooms.RoomLaunchPlan, error) {
	if p == nil || p.registry == nil {
		return runtimeRooms.RoomLaunchPlan{}, errDeviceRegistry
	}
	loaded, err := loadConfig(configDir)
	if err != nil {
		return runtimeRooms.RoomLaunchPlan{}, err
	}
	credential, present := lookup(defaultCredentialEnv)
	fromEnvironment := present && strings.TrimSpace(credential) != ""
	if strings.TrimSpace(credential) == "" && loaded != nil && loaded.Model.OpenAI != nil {
		credential = loaded.Model.OpenAI.APIKey
	}
	if strings.TrimSpace(credential) == "" {
		return runtimeRooms.RoomLaunchPlan{}, fmt.Errorf("bare room requires an OpenAI API key; set the %s environment variable before running room run", defaultCredentialEnv)
	}
	input, err := p.defaultDevice(devicegw.DirectionInput)
	if err != nil {
		return runtimeRooms.RoomLaunchPlan{}, err
	}
	output, err := p.defaultDevice(devicegw.DirectionOutput)
	if err != nil {
		return runtimeRooms.RoomLaunchPlan{}, err
	}
	manifest := runtimeRooms.Manifest{SchemaVersion: runtimeRooms.SchemaVersion, Room: runtimeRooms.Room{Interactive: true}, Participants: []runtimeRooms.Participant{
		{Kind: runtimeRooms.ParticipantKindHuman, ID: defaultCustomerID, SystemPrompt: defaultCustomerPrompt, Tools: []string{}, InputDevice: input.ID, OutputDevice: output.ID},
		{Kind: runtimeRooms.ParticipantKindAgent, ID: defaultAgentID, SystemPrompt: defaultAgentPrompt, Provider: config.ProviderOpenAI, Model: defaultRealtimeModel, APIKeyEnv: defaultCredentialEnv, Tools: []string{}},
	}}
	provenance := "config"
	if fromEnvironment {
		provenance = "environment"
	}
	return runtimeRooms.RoomLaunchPlan{Mode: runtimeRooms.RoomLaunchModeBare, ConfigDir: configDir, Manifest: manifest, Participants: []runtimeRooms.RoomLaunchParticipantPlan{
		{ID: defaultCustomerID, Kind: runtimeRooms.ParticipantKindHuman, InputDevice: input.ID, OutputDevice: output.ID},
		{ID: defaultAgentID, Kind: runtimeRooms.ParticipantKindAgent, Provider: config.ProviderOpenAI, Model: defaultRealtimeModel, CredentialReference: defaultCredentialEnv, CredentialProvenance: runtimeRooms.RoomCredentialProvenance(provenance)},
	}}, nil
}

type selectedDevices struct {
	input  string
	output string
}

func (p *Planner) humanDevices(manifest room.Manifest) (map[string]selectedDevices, error) {
	result := make(map[string]selectedDevices)
	human := false
	for _, participant := range manifest.Participants {
		if normalizeParticipantKind(participant.Kind) == room.ParticipantKindHuman {
			human = true
			break
		}
	}
	if !human {
		return result, nil
	}
	if p == nil || p.registry == nil {
		return nil, fmt.Errorf("audio device registry is unavailable; run agent devices list: %w", devicegw.ErrNilDeviceRegistry)
	}
	listed, err := p.registry.List()
	if err != nil {
		return nil, fmt.Errorf("could not inspect available devices: %w", err)
	}
	byID := make(map[devicegw.DeviceID]devicegw.Device, len(listed))
	for _, value := range listed {
		byID[value.ID] = value
	}
	for index, participant := range manifest.Participants {
		if normalizeParticipantKind(participant.Kind) != room.ParticipantKindHuman {
			continue
		}
		input, err := selectDevice(byID, participant.InputDevice, devicegw.DirectionInput, index, "input_device")
		if err != nil {
			return nil, err
		}
		output, err := selectDevice(byID, participant.OutputDevice, devicegw.DirectionOutput, index, "output_device")
		if err != nil {
			return nil, err
		}
		result[participant.ID] = selectedDevices{input: input.ID, output: output.ID}
	}
	return result, nil
}

func normalizeParticipantKind(kind room.ParticipantKind) room.ParticipantKind {
	switch room.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))) {
	case "", room.ParticipantKindAgent:
		return room.ParticipantKindAgent
	case room.ParticipantKindHuman, room.ParticipantKindCustomer:
		return room.ParticipantKindHuman
	default:
		return room.ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind))))
	}
}

func selectDevice(byID map[devicegw.DeviceID]devicegw.Device, selector string, want devicegw.Direction, index int, field string) (devicegw.Device, error) {
	value, ok := byID[selector]
	if !ok {
		return devicegw.Device{}, &room.ValidationError{Field: fmt.Sprintf("participants[%d].%s", index, field), Value: selector, Problem: "device is unavailable; run agent devices list", Cause: devicegw.NewDeviceNotFoundError(selector)}
	}
	if err := value.Validate(); err != nil {
		return devicegw.Device{}, &room.ValidationError{Field: fmt.Sprintf("participants[%d].%s", index, field), Value: selector, Problem: err.Error(), Cause: err}
	}
	if value.Direction != want {
		cause := &devicegw.DeviceDirectionError{ID: value.ID, Direction: want, Want: want, Got: value.Direction, Kind: devicegw.ErrDeviceDirectionMismatch}
		return devicegw.Device{}, &room.ValidationError{Field: fmt.Sprintf("participants[%d].%s", index, field), Value: selector, Problem: cause.Error(), Cause: cause}
	}
	return value, nil
}

func (p *Planner) defaultDevice(direction devicegw.Direction) (devicegw.Device, error) {
	value, err := p.registry.Default(direction)
	if err != nil {
		return devicegw.Device{}, fmt.Errorf("bare room customer %s device is unavailable: %w; run agent devices list", direction, err)
	}
	if err := value.Validate(); err != nil {
		return devicegw.Device{}, err
	}
	if value.Direction != direction {
		return devicegw.Device{}, &devicegw.DeviceDirectionError{ID: value.ID, Direction: direction, Want: direction, Got: value.Direction, Kind: devicegw.ErrDeviceDirectionMismatch}
	}
	return value, nil
}

func loadConfig(configDir string) (*config.Config, error) {
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize room config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load room config: %w", err)
	}
	return loaded, nil
}

func sourcePath(configPath, manifestPath string) (string, error) {
	configPath, manifestPath = strings.TrimSpace(configPath), strings.TrimSpace(manifestPath)
	if configPath != "" && manifestPath != "" && configPath != manifestPath {
		return "", fmt.Errorf("%w: --config=%q and --manifest=%q", runtimeRooms.ErrLaunchPathConflict, configPath, manifestPath)
	}
	if configPath != "" {
		return configPath, nil
	}
	return manifestPath, nil
}

func toRuntimeManifest(value room.Manifest) runtimeRooms.Manifest {
	result := runtimeRooms.Manifest{
		SchemaVersion: value.SchemaVersion,
		Room: runtimeRooms.Room{
			MaxTurns: value.Room.MaxTurns, MaxDuration: value.Room.MaxDuration,
			Interactive: value.Room.Interactive,
		},
		Participants: make([]runtimeRooms.Participant, 0, len(value.Participants)),
	}
	if value.Room.Recording != nil {
		result.Room.Recording = &runtimeRooms.RoomRecordingConfig{
			Enabled: cloneBool(value.Room.Recording.Enabled), Directory: value.Room.Recording.Directory,
		}
	}
	for _, participant := range value.Participants {
		result.Participants = append(result.Participants, runtimeRooms.Participant{
			Kind: runtimeRooms.ParticipantKind(participant.Kind), ID: participant.ID,
			SystemPrompt: participant.SystemPrompt, OpeningPrompt: participant.OpeningPrompt,
			Provider: participant.Provider, Model: participant.Model, APIKeyEnv: participant.APIKeyEnv,
			Voice: participant.Voice, Tools: append([]string(nil), participant.Tools...),
			BrowserTools: toRuntimeBrowserTools(participant.BrowserTools),
			InputDevice:  participant.InputDevice, OutputDevice: participant.OutputDevice,
		})
	}
	return result
}

func toRuntimeBrowserTools(value *room.BrowserToolsConfig) *runtimeRooms.BrowserToolsConfig {
	if value == nil {
		return nil
	}
	return &runtimeRooms.BrowserToolsConfig{
		Backend: value.Backend,
		Connection: runtimeRooms.BrowserConnectionConfig{
			CDPURL: value.Connection.CDPURL, WSEndpoint: value.Connection.WSEndpoint,
			UserDataDir: value.Connection.UserDataDir, AllowProcessScan: value.Connection.AllowProcessScan,
			AllowRemoteCDP: value.Connection.AllowRemoteCDP,
		},
		Selection: runtimeRooms.BrowserSelectionConfig{
			Browser: value.Selection.Browser, Tab: value.Selection.Tab, Origin: value.Selection.Origin,
			AutoSelect: value.Selection.AutoSelect, ActivateTab: value.Selection.ActivateTab,
			Persist: value.Selection.Persist,
		},
		Policy: runtimeRooms.BrowserPolicyConfig{
			AllowedOrigins: append([]string(nil), value.Policy.AllowedOrigins...),
			DeniedOrigins:  append([]string(nil), value.Policy.DeniedOrigins...),
			Approval:       value.Policy.Approval, CancelOnInterrupt: value.Policy.CancelOnInterrupt,
		},
		Limits: runtimeRooms.BrowserLimitsConfig{
			InvocationTimeout: value.Limits.InvocationTimeout, MaxInputBytes: value.Limits.MaxInputBytes,
			MaxResultBytes: value.Limits.MaxResultBytes, SerializePerTarget: value.Limits.SerializePerTarget,
		},
		Recording: runtimeRooms.BrowserRecordingConfig{
			Enabled: value.Recording.Enabled, IncludeArguments: value.Recording.IncludeArguments,
			IncludeResults: value.Recording.IncludeResults, RedactURLQuery: value.Recording.RedactURLQuery,
			RedactURLFragment: value.Recording.RedactURLFragment,
		},
		Replay: runtimeRooms.BrowserReplayConfig{Path: value.Replay.Path, Strict: value.Replay.Strict},
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
