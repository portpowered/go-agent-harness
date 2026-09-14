// Package sessionroomerrors owns credential-free room failure projections.
// The contract is host-neutral: callers provide error facts and receive
// stable diagnostic values without importing a CLI, provider, or device.
package sessionroomerrors

// ParticipantFailure is the public identity projection carried by a wrapped
// participant error. Its concrete implementation remains private to the
// service package while errors.Is/errors.As still reach the original cause.
type ParticipantFailure interface {
	error
	ParticipantID() string
}

// ParticipantFailureRequest describes one participant-local failure.
type ParticipantFailureRequest struct {
	ParticipantID string
	Cause         error
	Secrets       []string
}

// ParticipantTerminationReason is the host-neutral participant terminal
// fallback used when no explicit cause or close reason is available.
type ParticipantTerminationReason string

const (
	ParticipantTerminationDisconnected ParticipantTerminationReason = "disconnected"
	ParticipantTerminationError        ParticipantTerminationReason = "error"
)

// ParticipantFailureReasonRequest contains only observable terminal facts.
type ParticipantFailureReasonRequest struct {
	Error                 error
	TerminationReason     ParticipantTerminationReason
	CloseReason           string
	TransportDisconnected bool
	Secrets               []string
}

// RoomTerminationReason is the host-neutral room terminal taxonomy needed by
// a failed projection.
type RoomTerminationReason string

const RoomTerminationFailed RoomTerminationReason = "failed"

// RoomFailureResult is the stable failed-room projection. Participants is
// deliberately non-nil even when no participant result is available.
type RoomFailureResult struct {
	TerminationReason RoomTerminationReason
	Reason            RoomTerminationReason
	Error             string
	Participants      map[string]struct{}
}

// Service owns participant wrapping, secret-aware cause projection,
// termination fallback, and stable failed-room results.
type Service interface {
	ParticipantFailure(ParticipantFailureRequest) error
	ParticipantFailureID(error) (string, bool)
	ParticipantFailureReason(ParticipantFailureReasonRequest) string
	Sanitize(error, []string) string
	FailureResult(error, []string) RoomFailureResult
}
