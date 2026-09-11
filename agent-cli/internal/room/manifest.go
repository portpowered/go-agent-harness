// Package room retains the CLI room-document names as compatibility aliases.
// Admission, normalization, validation, and typed error construction live in
// the embeddable runtime rooms service.
package room

import (
	"os"
	"strings"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
)

const SchemaVersion = runtimeRooms.SchemaVersion

var (
	ErrInvalidManifest        = runtimeRooms.ErrInvalidManifest
	ErrUnsupportedSchema      = runtimeRooms.ErrUnsupportedSchema
	ErrMissingBound           = runtimeRooms.ErrMissingBound
	ErrInvalidBound           = runtimeRooms.ErrInvalidBound
	ErrTooFewParticipants     = runtimeRooms.ErrTooFewParticipants
	ErrInvalidParticipant     = runtimeRooms.ErrInvalidParticipant
	ErrUnknownParticipantKind = runtimeRooms.ErrUnknownParticipantKind
	ErrDuplicateParticipant   = runtimeRooms.ErrDuplicateParticipant
	ErrCredential             = runtimeRooms.ErrCredential
	ErrUnknownProvider        = runtimeRooms.ErrUnknownProvider
	ErrUnknownModel           = runtimeRooms.ErrUnknownModel
	ErrUnknownTool            = runtimeRooms.ErrUnknownTool
	ErrUnknownVoice           = runtimeRooms.ErrUnknownVoice
	ErrDuplicateTool          = runtimeRooms.ErrDuplicateTool
	ErrInvalidRecording       = runtimeRooms.ErrInvalidRecording
	ErrInvalidDocument        = runtimeRooms.ErrInvalidDocument
	ErrNoRoomOpener           = runtimeRooms.ErrNoRoomOpener
)

type ParticipantKind = runtimeRooms.ParticipantKind

const (
	ParticipantKindAgent    = runtimeRooms.ParticipantKindAgent
	ParticipantKindHuman    = runtimeRooms.ParticipantKindHuman
	ParticipantKindCustomer = runtimeRooms.ParticipantKindCustomer
)

type ValidationError = runtimeRooms.ValidationError
type Manifest = runtimeRooms.Manifest
type Room = runtimeRooms.Room
type RoomRecordingConfig = runtimeRooms.RoomRecordingConfig
type RecordingConfig = runtimeRooms.RecordingConfig
type Participant = runtimeRooms.Participant
type ValidationOptions = runtimeRooms.ValidationOptions
type ValidationRegistry = runtimeRooms.ValidationRegistry

// NormalizeParticipantKind retains the CLI helper for existing callers while
// keeping the compatibility spelling at the document boundary.
func NormalizeParticipantKind(kind ParticipantKind) ParticipantKind {
	switch normalized := ParticipantKind(strings.ToLower(strings.TrimSpace(string(kind)))); normalized {
	case "", ParticipantKindAgent:
		return ParticipantKindAgent
	case ParticipantKindHuman, ParticipantKindCustomer:
		return ParticipantKindHuman
	default:
		return normalized
	}
}

func NewValidationRegistry(providers []string, models map[string][]string, tools []string, voices map[string][]string) ValidationRegistry {
	return runtimeWire.NewValidationRegistry(providers, models, tools, voices)
}

// ParseManifest preserves the historical CLI behavior of checking named
// credentials against the process environment. The runtime provider itself
// never reads host configuration implicitly.
func ParseManifest(data []byte, options ...ValidationOptions) (Manifest, error) {
	return newCLIManifestProvider().Parse(data, cliValidationOptions(options)...)
}

// ReadManifest preserves the historical CLI behavior for file admission while
// delegating all document handling to runtime rooms.
func ReadManifest(path string, options ...ValidationOptions) (Manifest, error) {
	return newCLIManifestProvider().Read(path, cliValidationOptions(options)...)
}

func cliValidationOptions(options []ValidationOptions) []ValidationOptions {
	if len(options) == 0 {
		return []ValidationOptions{{LookupCredential: os.LookupEnv}}
	}
	if len(options) != 1 || options[0].LookupCredential != nil {
		return options
	}
	resolved := options[0]
	resolved.LookupCredential = os.LookupEnv
	return []ValidationOptions{resolved}
}

func newCLIManifestProvider() runtimeWire.ManifestAdmission {
	return runtimeWire.NewManifestProvider(ValidationOptions{LookupCredential: os.LookupEnv})
}
