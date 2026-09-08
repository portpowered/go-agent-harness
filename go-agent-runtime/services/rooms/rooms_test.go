package rooms_test

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestManifestValidationRejectsSilentAllAgentRoom(t *testing.T) {
	manifest := rooms.Manifest{SchemaVersion: rooms.SchemaVersion, Room: rooms.Room{MaxTurns: 1}, Participants: []rooms.Participant{
		{ID: "a", SystemPrompt: "a", Provider: "offline", Model: "m", APIKeyEnv: "A", Tools: []string{}},
		{ID: "b", SystemPrompt: "b", Provider: "offline", Model: "m", APIKeyEnv: "B", Tools: []string{}},
	}}
	if !errors.Is(manifest.Validate(), rooms.ErrNoRoomOpener) {
		t.Fatalf("Validate error = %v, want ErrNoRoomOpener", manifest.Validate())
	}
}
