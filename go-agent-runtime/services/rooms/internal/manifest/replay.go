package manifest

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// FromReplay projects admitted replay metadata into the credential-free room
// manifest used by the lifecycle service. Replay policy remains inside the
// room service implementation; callers receive only the public contract.
func FromReplay(plan roomreplay.RoomReplayPlan) rooms.Manifest {
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{Interactive: true}, Participants: make([]rooms.Participant, 0, len(plan.Participants))}
	for _, participant := range plan.Participants {
		kind := NormalizeParticipantKind(rooms.ParticipantKind(participant.Kind))
		provider, model := participant.Provider, participant.Model
		apiKeyEnv, inputDevice, outputDevice := "ROOM_REPLAY", "", ""
		if kind == rooms.ParticipantKindHuman {
			provider, model = "", ""
			apiKeyEnv, inputDevice, outputDevice = "", "replay", "replay"
		}
		manifest.Participants = append(manifest.Participants, rooms.Participant{
			Kind: kind, ID: participant.ID, SystemPrompt: participant.SystemPrompt,
			OpeningPrompt: participant.OpeningPrompt, Provider: provider, Model: model,
			APIKeyEnv: apiKeyEnv, Voice: participant.Voice, Tools: []string{},
			InputDevice: inputDevice, OutputDevice: outputDevice,
		})
	}
	return manifest
}
