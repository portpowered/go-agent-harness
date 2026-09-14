package rooms

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
)

var (
	// ErrInvalidManifest identifies a room document that cannot be admitted.
	ErrInvalidManifest = errors.New("invalid room manifest")
	// ErrUnsupportedSchema identifies a manifest schema this service cannot read.
	ErrUnsupportedSchema = errors.New("unsupported room manifest schema")
	// ErrMissingBound identifies a non-interactive room without a positive bound.
	ErrMissingBound = errors.New("room manifest requires a bound")
	// ErrInvalidBound identifies a negative or malformed room bound.
	ErrInvalidBound = errors.New("invalid room manifest bound")
	// ErrTooFewParticipants identifies a room with fewer than two participants.
	ErrTooFewParticipants = errors.New("room manifest requires at least two participants")
	// ErrInvalidParticipant identifies a participant with an invalid shape.
	ErrInvalidParticipant = errors.New("invalid room manifest participant")
	// ErrUnknownParticipantKind identifies an unsupported participant owner.
	ErrUnknownParticipantKind = errors.New("unknown room manifest participant kind")
	// ErrDuplicateParticipant identifies repeated participant identity.
	ErrDuplicateParticipant = errors.New("room manifest contains duplicate participant")
	// ErrCredential identifies an invalid credential reference.
	ErrCredential = errors.New("invalid room manifest credential")
	// ErrUnknownProvider identifies an unregistered provider.
	ErrUnknownProvider = errors.New("unknown room manifest provider")
	// ErrUnknownModel identifies an unregistered model.
	ErrUnknownModel = errors.New("unknown room manifest model")
	// ErrUnknownTool identifies an unregistered tool.
	ErrUnknownTool = errors.New("unknown room manifest tool")
	// ErrUnknownVoice identifies an unregistered voice.
	ErrUnknownVoice = errors.New("unknown room manifest voice")
	// ErrDuplicateTool identifies repeated tool identity for one participant.
	ErrDuplicateTool = errors.New("room manifest contains duplicate tool")
	// ErrInvalidRecording identifies malformed recording policy.
	ErrInvalidRecording = errors.New("invalid room manifest recording")
	// ErrInvalidDocument identifies malformed JSON or YAML.
	ErrInvalidDocument = errors.New("invalid room manifest document")
	// ErrNoRoomOpener identifies an all-agent room that cannot begin.
	ErrNoRoomOpener = errors.New("room manifest has no participant designated to speak first")
	// ErrInvalidBrowserTools identifies an invalid browser capability policy.
	ErrInvalidBrowserTools = errors.New("invalid room browser tools")
	// ErrInvalidBrowserEndpoint identifies an unsafe browser endpoint.
	ErrInvalidBrowserEndpoint = errors.New("invalid room browser endpoint")
	// ErrInvalidBrowserOption identifies an unsupported browser option.
	ErrInvalidBrowserOption = errors.New("invalid room browser option")

	// ErrInvalidReplayBundle identifies a bundle that does not match the
	// supported replay schema or integrity inventory.
	ErrInvalidReplayBundle = roomreplay.ErrInvalidRoomReplayBundle
	// ErrReplayBundleIncomplete identifies a recognizable but unfinished bundle.
	ErrReplayBundleIncomplete = roomreplay.ErrRoomReplayBundleIncomplete
	// ErrReplaySourceConflict identifies conflicting live and replay inputs.
	ErrReplaySourceConflict = errors.New("room replay source conflict")
	// ErrLaunchPathConflict identifies both config spellings with different paths.
	ErrLaunchPathConflict = errors.New("room launch config and manifest paths conflict")
	// ErrRoomServiceUnavailable identifies a service constructed without a live
	// session port. Replay admission and planning remain usable in that mode.
	ErrRoomServiceUnavailable = errors.New("room live session service is unavailable")
	// ErrRoomClockUnavailable identifies a live room without the canonical
	// scheduler required for bounds and diagnostic timestamps.
	ErrRoomClockUnavailable = errors.New("room lifecycle clock is unavailable")
)

// ValidationError identifies the exact non-secret manifest field that failed.
type ValidationError struct {
	Field   string
	Value   string
	Problem string
	Cause   error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "room manifest field " + quote(e.Field)
	if e.Value != "" {
		message += " " + quote(e.Value)
	}
	if e.Problem != "" {
		message += ": " + e.Problem
	}
	return message
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ValidationError) Is(target error) bool {
	if e == nil {
		return false
	}
	return target == ErrInvalidManifest || target == e.Cause
}

func quote(value string) string {
	return `"` + value + `"`
}

func validation(field, value, problem string, cause error) error {
	return &ValidationError{Field: field, Value: value, Problem: problem, Cause: cause}
}

// ParticipantFailure is the typed identity carried by a participant-local
// failure. The concrete wrapper remains private while errors.Is/errors.As
// preserve the original provider, media, or capability cause.
type ParticipantFailure interface {
	error
	ParticipantID() string
}

// ParticipantFailureRequest describes one participant-local failure at the
// room boundary. Secrets are copied and used only for the textual projection.
type ParticipantFailureRequest struct {
	ParticipantID string
	Cause         error
	Secrets       []string
}

// ParticipantFailureReasonRequest contains terminal facts used when a room
// must produce a credential-free participant diagnostic fallback.
type ParticipantFailureReasonRequest struct {
	Error                 error
	TerminationReason     ParticipantTerminationReason
	CloseReason           string
	TransportDisconnected bool
	Secrets               []string
}

// RoomFailureResult is the stable failed-room projection used by hosts that
// need a result before participant details are available.
type RoomFailureResult struct {
	TerminationReason RoomTerminationReason
	Reason            RoomTerminationReason
	Error             string
	Participants      map[string]struct{}
}

// FailureService owns participant wrapping, secret-aware cause projection,
// terminal fallback, and stable failed-room results. Implementations are
// constructed by the service-local Wire graph.
type FailureService interface {
	ParticipantFailure(ParticipantFailureRequest) error
	ParticipantFailureID(error) (string, bool)
	ParticipantFailureReason(ParticipantFailureReasonRequest) string
	Sanitize(error, []string) string
	FailureResult(error, []string) RoomFailureResult
}
