package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/events"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
)

// NewRoomSecretRedactor resolves the room's participant credentials with the
// same rules room evidence uses and returns a redactor for every text that
// leaves the room boundary: live event frames, host progress, and errors.
func NewRoomSecretRedactor(sources rooms.RoomCredentialSources) rooms.RoomSecretRedactor {
	return events.NewRedactor(planning.EvidenceSecrets(sources.Manifest, rooms.RoomRunOptions{
		CredentialLookup: sources.CredentialLookup, ConfigCredential: sources.ConfigCredential,
	}))
}

// NewRoomEventStream constructs the bounded, redacting room event fan-out a
// host transport serves to its subscribers.
func NewRoomEventStream(options rooms.RoomEventStreamOptions) (rooms.RoomEventStream, error) {
	stream, err := events.NewStream(options)
	if err != nil {
		return nil, err
	}
	return stream, nil
}
