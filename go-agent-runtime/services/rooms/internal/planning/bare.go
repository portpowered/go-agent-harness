package planning

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

const (
	bareCustomerID     = "customer"
	bareAgentID        = "agent"
	bareProvider       = "openai"
	bareCustomerPrompt = "You are the human customer in a live room. Speak naturally and briefly."
	bareAgentPrompt    = "You are the room's helpful OpenAI realtime agent. Speak naturally and briefly."
)

// bare synthesizes the interactive customer-plus-agent room. The credential
// is checked before host defaults are queried so a missing key never touches
// a device, and the resolved credential value is never retained.
func bare(options rooms.RoomLaunchOptions, lookup func(string) (string, bool)) (rooms.RoomLaunchPlan, error) {
	if options.Devices == nil {
		return rooms.RoomLaunchPlan{}, fmt.Errorf("bare room launch requires an audio device registry: %w", rooms.ErrLaunchDeviceInventoryUnavailable)
	}
	provenance, err := bareCredential(options.ConfigCredential, lookup)
	if err != nil {
		return rooms.RoomLaunchPlan{}, err
	}
	input, err := defaultDevice(options.Devices, rooms.LaunchDeviceInput)
	if err != nil {
		return rooms.RoomLaunchPlan{}, err
	}
	output, err := defaultDevice(options.Devices, rooms.LaunchDeviceOutput)
	if err != nil {
		return rooms.RoomLaunchPlan{}, err
	}
	return barePlan(options.ConfigDir, input.ID, output.ID, provenance), nil
}

func bareCredential(configured rooms.ConfigCredentialLookup, lookup func(string) (string, bool)) (rooms.RoomCredentialProvenance, error) {
	fallback := ""
	if configured != nil {
		value, err := configured(rooms.DefaultOpenAIAPIKeyEnv)
		if err != nil {
			return "", err
		}
		fallback = value
	}
	if value, ok := lookup(rooms.DefaultOpenAIAPIKeyEnv); ok && strings.TrimSpace(value) != "" {
		return rooms.RoomCredentialFromEnvironment, nil
	}
	if strings.TrimSpace(fallback) != "" {
		return rooms.RoomCredentialFromConfig, nil
	}
	return "", fmt.Errorf("bare room requires an OpenAI API key; set the %s environment variable before running room run", rooms.DefaultOpenAIAPIKeyEnv)
}

func barePlan(configDir, input, output string, provenance rooms.RoomCredentialProvenance) rooms.RoomLaunchPlan {
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{Interactive: true}, Participants: []rooms.Participant{
		{Kind: rooms.ParticipantKindHuman, ID: bareCustomerID, SystemPrompt: bareCustomerPrompt, Tools: []string{}, InputDevice: input, OutputDevice: output},
		{Kind: rooms.ParticipantKindAgent, ID: bareAgentID, SystemPrompt: bareAgentPrompt, Provider: bareProvider, Model: rooms.DefaultRealtimeModel, APIKeyEnv: rooms.DefaultOpenAIAPIKeyEnv, Tools: []string{}},
	}}
	return rooms.RoomLaunchPlan{Mode: rooms.RoomLaunchModeBare, ConfigDir: configDir, Manifest: manifest, Participants: []rooms.RoomLaunchParticipantPlan{
		{ID: bareCustomerID, Kind: rooms.ParticipantKindHuman, InputDevice: input, OutputDevice: output},
		{ID: bareAgentID, Kind: rooms.ParticipantKindAgent, Provider: bareProvider, Model: rooms.DefaultRealtimeModel, CredentialReference: rooms.DefaultOpenAIAPIKeyEnv, CredentialProvenance: provenance},
	}}
}
