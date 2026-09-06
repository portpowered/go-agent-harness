// Package planning resolves room admission inputs into immutable decisions.
// It does not construct a provider, open a device, create a goroutine, or
// retain a host configuration object.
package planning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

type Planner struct{}

func New() Planner { return Planner{} }

// Resolve admits an explicitly configured room. Bare room synthesis is kept
// behind the outer wire until the device and live-session ports are present.
func (Planner) Resolve(options rooms.RoomLaunchOptions) (rooms.RoomLaunchPlan, error) {
	path, err := sourcePath(options.ConfigPath, options.ManifestPath)
	if err != nil {
		return rooms.RoomLaunchPlan{}, err
	}
	if path == "" {
		return rooms.RoomLaunchPlan{}, fmt.Errorf("%w: bare launch requires an injected host plan", rooms.ErrRoomServiceUnavailable)
	}
	lookup := options.CredentialLookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, err := manifest.Read(path, rooms.ValidationOptions{LookupCredential: lookup})
	if err != nil {
		return rooms.RoomLaunchPlan{}, fmt.Errorf("validate room config: %w", err)
	}
	return plan(value, path, options.ConfigDir, lookup), nil
}

func sourcePath(configPath, manifestPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	manifestPath = strings.TrimSpace(manifestPath)
	if configPath != "" && manifestPath != "" && configPath != manifestPath {
		return "", fmt.Errorf("%w: --config=%q and --manifest=%q", rooms.ErrLaunchPathConflict, configPath, manifestPath)
	}
	if configPath != "" {
		return configPath, nil
	}
	return manifestPath, nil
}

func plan(value rooms.Manifest, path, configDir string, lookup func(string) (string, bool)) rooms.RoomLaunchPlan {
	if strings.TrimSpace(configDir) == "" {
		configDir = filepath.Dir(path)
	}
	result := rooms.RoomLaunchPlan{
		Mode: rooms.RoomLaunchModeConfigured, ConfigPath: path, ConfigDir: configDir,
		Manifest: value, Participants: make([]rooms.RoomLaunchParticipantPlan, 0, len(value.Participants)),
	}
	for _, participant := range value.Participants {
		kind := manifest.NormalizeParticipantKind(participant.Kind)
		decision := rooms.RoomLaunchParticipantPlan{
			ID: participant.ID, Kind: kind, InputDevice: participant.InputDevice,
			OutputDevice: participant.OutputDevice, Provider: participant.Provider,
			Model: participant.Model, CredentialReference: participant.APIKeyEnv,
		}
		if participant.APIKeyEnv != "" {
			if credential, ok := lookup(participant.APIKeyEnv); ok && strings.TrimSpace(credential) != "" {
				decision.CredentialProvenance = rooms.RoomCredentialFromEnvironment
			}
		}
		result.Participants = append(result.Participants, decision)
	}
	return result
}
