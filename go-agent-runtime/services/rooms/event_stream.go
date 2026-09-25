package rooms

import "time"

// Room event stream projection names. They are the stable "type" and
// "event" values carried by every RoomEventStream frame.
const (
	// RoomStreamParticipantID is the reserved participant identity used for
	// room-scoped events such as run termination.
	RoomStreamParticipantID = "room"

	RoomStreamTypeDiagnostic      = "diagnostic"
	RoomStreamTypeTranscriptDelta = "transcript_delta"
	RoomStreamTypeTranscriptEnd   = "transcript_end"
	RoomStreamTypeRoom            = "room"

	RoomStreamEventParticipantJoined        = "participant_joined"
	RoomStreamEventParticipantReady         = "participant_ready"
	RoomStreamEventParticipantFailed        = "participant_failed"
	RoomStreamEventParticipantLivenessFault = "participant_liveness_fault"
	RoomStreamEventParticipantTerminated    = "participant_terminated"
	RoomStreamEventRunTerminated            = "run_terminated"
)

// RoomEventStream is a room's forward-only, in-memory live event fan-out.
// Every frame is a complete JSON object with the room's participant
// credentials already redacted. Publishing never blocks on a slow
// subscriber: a subscriber whose bounded queue fills is dropped and its
// channel closed. Events for one participant are delivered in publish order.
type RoomEventStream interface {
	EventSink
	// PublishRoomEvent projects one room lifecycle event. An empty
	// participant identity names the room itself.
	PublishRoomEvent(event, participantID, reason string)
	// Subscribe registers a forward-only subscriber. An empty participant
	// receives every event; a known participant receives its own events plus
	// broadcast liveness faults. Unknown participants fail with
	// ErrUnknownRoomStreamParticipant and a closed stream fails with
	// ErrRoomEventStreamClosed.
	Subscribe(participantID string) (RoomEventSubscription, error)
	// Close closes every subscriber channel; later publishes are dropped.
	Close() error
}

// RoomEventSubscription is one subscriber's bounded frame queue.
type RoomEventSubscription interface {
	// Frames yields one JSON-encoded event per value and is closed when the
	// subscriber is dropped, closed, or the stream closes.
	Frames() <-chan []byte
	// Close unregisters the subscriber. It is safe to call more than once.
	Close()
}

// RoomSecretRedactor removes a room's resolved participant credentials from
// text before the text leaves the room boundary.
type RoomSecretRedactor interface {
	Redact(string) string
}

// RoomCredentialSources resolve the credential values a room's participants
// use, with the same rules the room applies to its evidence artifacts.
type RoomCredentialSources struct {
	Manifest Manifest
	// CredentialLookup resolves api_key_env references. Nil uses the process
	// environment.
	CredentialLookup func(string) (string, bool)
	// ConfigCredential supplies host-configured fallback credentials.
	ConfigCredential ConfigCredentialLookup
}

// RoomEventStreamOptions configure one room event stream.
type RoomEventStreamOptions struct {
	ParticipantIDs []string
	// Redactor is applied to every projected field before a frame is
	// encoded. Nil publishes fields unchanged.
	Redactor RoomSecretRedactor
	// QueueSize bounds each subscriber queue; zero selects the default.
	QueueSize int
	// Now stamps events; nil uses the wall clock.
	Now func() time.Time
}
