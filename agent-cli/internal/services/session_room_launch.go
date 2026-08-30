package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

const (
	// DefaultRoomCustomerID and DefaultRoomAgentID are stable identities for a
	// zero-flag room. They are also used by evidence and event consumers, so
	// changing them would be a compatibility break.
	DefaultRoomCustomerID = "customer"
	DefaultRoomAgentID    = "agent"

	// DefaultRoomCredentialEnv is the same environment-backed credential name
	// used by the ordinary OpenAI realtime session configuration.
	DefaultRoomCredentialEnv = SessionOpenAIAPIKeyEnv

	DefaultRoomCustomerSystemPrompt = "You are the human customer in a live room. Speak naturally and briefly."
	DefaultRoomAgentSystemPrompt    = "You are the room's helpful OpenAI realtime agent. Speak naturally and briefly."
)

var (
	// ErrRoomLaunchDeviceRegistry identifies a bare launch without an audio
	// registry. Device lookup is a composition-root responsibility and must be
	// present before a bare plan can be resolved.
	ErrRoomLaunchDeviceRegistry = errors.New("bare room launch requires an audio device registry")
	// ErrRoomLaunchConfig identifies a configuration loader that returned no
	// usable snapshot before any provider or device acquisition.
	ErrRoomLaunchConfig = errors.New("bare room launch could not resolve agent configuration")
	// ErrRoomLaunchPathConflict identifies both supported file spellings being
	// supplied with different values.
	ErrRoomLaunchPathConflict = errors.New("room launch config and manifest paths conflict")
)

// RoomLaunchMode distinguishes synthesized bare startup from an explicit
// room document. The latter is intentionally authoritative and is never
// merged with the former.
type RoomLaunchMode string

const (
	RoomLaunchModeBare       RoomLaunchMode = "bare"
	RoomLaunchModeConfigured RoomLaunchMode = "configured"
)

// RoomCredentialProvenance describes where the redacted agent credential was
// found. It never contains the credential value itself.
type RoomCredentialProvenance string

const (
	RoomCredentialFromEnvironment RoomCredentialProvenance = "environment"
	RoomCredentialFromConfig      RoomCredentialProvenance = "config"
)

// RoomLaunchParticipantPlan is the side-effect-free, non-secret projection of
// one resolved participant. InputDevice and OutputDevice are populated for a
// synthesized customer; provider metadata is populated for an agent.
type RoomLaunchParticipantPlan struct {
	ID                   string
	Kind                 room.ParticipantKind
	InputDevice          audio.Device
	OutputDevice         audio.Device
	Provider             string
	Model                string
	CredentialReference  string
	CredentialProvenance RoomCredentialProvenance
}

// RoomLaunchPlan is the complete normalized decision made before participant
// sessions, device handles, evidence writers, or provider connections are
// started. Manifest contains only credential references, never a key value.
type RoomLaunchPlan struct {
	Mode         RoomLaunchMode
	ConfigPath   string
	ConfigDir    string
	Manifest     room.Manifest
	Participants []RoomLaunchParticipantPlan
}

// Participant returns a copied participant decision by stable ID.
func (p RoomLaunchPlan) Participant(id string) (RoomLaunchParticipantPlan, bool) {
	for _, participant := range p.Participants {
		if participant.ID == id {
			return participant, true
		}
	}
	return RoomLaunchParticipantPlan{}, false
}

// RoomLaunchOptions supplies only the inputs needed to resolve a launch plan.
// LoadedConfig and LoadConfig are test/composition seams; when both are nil,
// the normal config storage and environment loader used by bare sessions are
// used.
type RoomLaunchOptions struct {
	ConfigPath   string
	ManifestPath string
	ConfigDir    string

	DeviceRegistry   audio.DeviceRegistry
	LoadedConfig     *config.Config
	LoadConfig       func() (*config.Config, error)
	CredentialLookup func(string) (string, bool)
}

// ResolveRoomLaunchPlan selects an explicit room document when a path is
// supplied, otherwise it resolves the safe customer-plus-agent bare room.
// Resolution performs no device Open and does not construct or dial a
// provider session.
func ResolveRoomLaunchPlan(options RoomLaunchOptions) (RoomLaunchPlan, error) {
	path, err := roomLaunchPath(options.ConfigPath, options.ManifestPath)
	if err != nil {
		return RoomLaunchPlan{}, err
	}
	if path != "" {
		return resolveConfiguredRoomLaunchPlan(path, options)
	}
	return ResolveBareRoomLaunchPlan(options)
}

// ResolveBareRoomLaunchPlan resolves one human customer and one OpenAI agent
// using the shared OpenAI realtime model/key helper and the registry's current
// directional defaults. It is safe for tests to call before any startup
// side-effect is introduced.
func ResolveBareRoomLaunchPlan(options RoomLaunchOptions) (RoomLaunchPlan, error) {
	if options.DeviceRegistry == nil {
		return RoomLaunchPlan{}, ErrRoomLaunchDeviceRegistry
	}

	loadedConfig, err := resolveRoomLaunchConfig(options)
	if err != nil {
		return RoomLaunchPlan{}, err
	}
	lookup := options.CredentialLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	apiKey, _ := lookup(DefaultRoomCredentialEnv)
	resolvedAgent, err := resolveOpenAIRealtimeSessionConfig(SessionRunOptions{
		Provider:      config.ProviderOpenAI,
		Model:         DefaultOpenAIRealtimeModel,
		ModelProvided: true,
		APIKey:        apiKey,
		ConfigDir:     options.ConfigDir,
		LoadedConfig:  loadedConfig,
	})
	if err != nil {
		return RoomLaunchPlan{}, err
	}

	input, err := resolveBareRoomDevice(options.DeviceRegistry, audio.DirectionInput)
	if err != nil {
		return RoomLaunchPlan{}, err
	}
	output, err := resolveBareRoomDevice(options.DeviceRegistry, audio.DirectionOutput)
	if err != nil {
		return RoomLaunchPlan{}, err
	}

	credentialProvenance := RoomCredentialFromConfig
	if value, present := lookup(DefaultRoomCredentialEnv); present && strings.TrimSpace(value) != "" {
		credentialProvenance = RoomCredentialFromEnvironment
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{Interactive: true},
		Participants: []room.Participant{
			{
				Kind:         room.ParticipantKindHuman,
				ID:           DefaultRoomCustomerID,
				SystemPrompt: DefaultRoomCustomerSystemPrompt,
				Tools:        []string{},
				InputDevice:  input.ID,
				OutputDevice: output.ID,
			},
			{
				Kind:         room.ParticipantKindAgent,
				ID:           DefaultRoomAgentID,
				SystemPrompt: DefaultRoomAgentSystemPrompt,
				Provider:     config.ProviderOpenAI,
				Model:        resolvedAgent.Model,
				APIKeyEnv:    DefaultRoomCredentialEnv,
				Tools:        []string{},
			},
		},
	}

	return RoomLaunchPlan{
		Mode:       RoomLaunchModeBare,
		ConfigPath: "",
		ConfigDir:  options.ConfigDir,
		Manifest:   manifest,
		Participants: []RoomLaunchParticipantPlan{
			{
				ID:           DefaultRoomCustomerID,
				Kind:         room.ParticipantKindHuman,
				InputDevice:  input,
				OutputDevice: output,
			},
			{
				ID:                   DefaultRoomAgentID,
				Kind:                 room.ParticipantKindAgent,
				Provider:             config.ProviderOpenAI,
				Model:                resolvedAgent.Model,
				CredentialReference:  DefaultRoomCredentialEnv,
				CredentialProvenance: credentialProvenance,
			},
		},
	}, nil
}

// ResolveDefaultRoomLaunchPlan is a descriptive alias for callers that want
// to make the zero-flag behavior explicit at the call site.
func ResolveDefaultRoomLaunchPlan(options RoomLaunchOptions) (RoomLaunchPlan, error) {
	return ResolveBareRoomLaunchPlan(options)
}

func roomLaunchPath(configPath, manifestPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	manifestPath = strings.TrimSpace(manifestPath)
	if configPath != "" && manifestPath != "" && configPath != manifestPath {
		return "", fmt.Errorf("%w: --config=%q and --manifest=%q", ErrRoomLaunchPathConflict, configPath, manifestPath)
	}
	if configPath != "" {
		return configPath, nil
	}
	return manifestPath, nil
}

func resolveConfiguredRoomLaunchPlan(path string, options RoomLaunchOptions) (RoomLaunchPlan, error) {
	manifest, err := room.ReadManifest(path)
	if err != nil {
		return RoomLaunchPlan{}, fmt.Errorf("validate room config: %w", err)
	}
	lookup := options.CredentialLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	participants := make([]RoomLaunchParticipantPlan, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
		kind := room.NormalizeParticipantKind(participant.Kind)
		decision := RoomLaunchParticipantPlan{
			ID:                  participant.ID,
			Kind:                kind,
			Provider:            participant.Provider,
			Model:               participant.Model,
			CredentialReference: participant.APIKeyEnv,
		}
		if participant.APIKeyEnv != "" {
			if value, present := lookup(participant.APIKeyEnv); present && strings.TrimSpace(value) != "" {
				decision.CredentialProvenance = RoomCredentialFromEnvironment
			}
		}
		participants = append(participants, decision)
	}
	return RoomLaunchPlan{
		Mode:         RoomLaunchModeConfigured,
		ConfigPath:   path,
		ConfigDir:    options.ConfigDir,
		Manifest:     manifest,
		Participants: participants,
	}, nil
}

func resolveRoomLaunchConfig(options RoomLaunchOptions) (*config.Config, error) {
	if options.LoadedConfig != nil {
		return options.LoadedConfig, nil
	}
	if options.LoadConfig != nil {
		loaded, err := options.LoadConfig()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRoomLaunchConfig, err)
		}
		if loaded == nil {
			return nil, fmt.Errorf("%w: loader returned nil configuration", ErrRoomLaunchConfig)
		}
		return loaded, nil
	}
	storage, err := config.NewDefaultConfigStorage(options.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("%w: initialize config: %v", ErrRoomLaunchConfig, err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("%w: load config: %v", ErrRoomLaunchConfig, err)
	}
	return loaded, nil
}

func resolveBareRoomDevice(registry audio.DeviceRegistry, direction audio.Direction) (audio.Device, error) {
	device, err := registry.Default(direction)
	if err != nil {
		return audio.Device{}, &RoomLaunchDeviceError{Direction: direction, Err: err}
	}
	if err := device.Validate(); err != nil {
		return audio.Device{}, &RoomLaunchDeviceError{Direction: direction, Err: err}
	}
	return device, nil
}

// RoomLaunchDeviceError identifies the direction that prevented a bare room
// from being resolved while preserving the registry's typed error.
type RoomLaunchDeviceError struct {
	Direction audio.Direction
	Err       error
}

func (e *RoomLaunchDeviceError) Error() string {
	if e == nil {
		return "bare room audio device is unavailable"
	}
	return fmt.Sprintf("bare room customer %s device is unavailable: %v; run agent devices list", e.Direction, e.Err)
}

func (e *RoomLaunchDeviceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RoomLaunchConfigDir returns the absolute directory used for the config
// snapshot. It is kept small and side-effect free for recording-path planning.
func RoomLaunchConfigDir(configDir string) (string, error) {
	if strings.TrimSpace(configDir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, config.ConfigDirName)
	}
	return filepath.Abs(configDir)
}
