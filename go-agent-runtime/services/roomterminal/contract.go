// Package roomterminal owns the provider-neutral terminal projection used by
// participant rooms. Hosts provide observations and callbacks; the service
// owns terminal classification, provenance defaults, room-bound trigger
// selection, and the bounded diagnostic projection.
package roomterminal

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

const (
	RoomTerminationMaxTurnsReached    = "max_turns_reached"
	RoomTerminationMaxDurationReached = "max_duration_reached"

	ParticipantTerminationTriggerMaxTurnsReached               = "max_turns_reached"
	ParticipantTerminationTriggerMaxTurnsReachedMidResponse    = "max_turns_reached_mid_response"
	ParticipantTerminationTriggerMaxDurationReached            = "max_duration_reached"
	ParticipantTerminationTriggerMaxDurationReachedMidResponse = "max_duration_reached_mid_response"
	ParticipantTerminationTriggerSessionFailure                = "session_failure"
	ParticipantTerminationTriggerParticipantCompletion         = "participant_completion"
	ParticipantTerminationTriggerProviderClose                 = "provider_close"

	ParticipantTerminationDispositionCompletedDuringGrace = "completed_during_grace"
	ParticipantTerminationDispositionCancelledAfterGrace  = "cancelled_after_grace"
	ParticipantTerminationDispositionCompleted            = "completed"
	ParticipantTerminationDispositionStopped              = "stopped"
	ParticipantTerminationDispositionFailed               = "failed"
	ParticipantTerminationDispositionDisconnected         = "disconnected"

	DiagnosticEventRoomBound = "room_bound_shutdown"
)

// Observation is the in-process terminal projection. Err is retained only so
// callers can preserve errors.Is/errors.As identity; it is never copied into a
// diagnostic field or external text by this service.
type Observation struct {
	ResponseID         string
	Classification     string
	TerminalReason     string
	TerminalProvenance string
	OutputState        string
	Err                error
	Failure            bool
	RoomBound          bool
	Code               string
	FailingEvent       string
}

// FailureFacts are the bounded fields recovered from a typed provider or
// session failure. ErrorType is used only as the compatibility fallback when
// the caller has no in-process error value.
type FailureFacts struct {
	Classification string
	TerminalReason string
	Provenance     string
	OutputState    string
	ErrorType      string
	Code           string
	FailingEvent   string
}

// FinalObservationRequest supplies one session's terminal state to the
// projection policy. CancellationOnly is computed by the host's error tree;
// the policy decides which supplied cause takes precedence.
type FinalObservationRequest struct {
	Failure                     *FailureFacts
	FailureError                error
	RunError                    error
	CancellationOnly            bool
	RoomBoundCancellation       bool
	UserCancelled               bool
	UserCancellationOutputState messages.TerminalOutputState
	SawSessionOpen              bool
	TurnsCompleted              int
	FallbackProvenance          string
	FallbackFailingEvent        string
}

// ParticipantResult is the credential-free terminal subset needed for room
// evidence and diagnostics. It deliberately contains no provider prose.
type ParticipantResult struct {
	TerminationTrigger     string
	TerminationDisposition string
	Classification         string
	TerminalReason         string
	TerminalProvenance     string
	OutputState            string
	Reason                 string
}

// DiagnosticRecord is the small host-neutral room-bound diagnostic value.
type DiagnosticRecord struct {
	Event  string
	Fields map[string]string
}

// BoundDiagnosticRequest supplies explicit host callbacks. The service has no
// knowledge of CLI evidence types and performs no hidden initialization.
type BoundDiagnosticRequest struct {
	ParticipantID     string
	Result            ParticipantResult
	RecordParticipant func(string, DiagnosticRecord)
	RecordTimeline    func(string, string, map[string]string)
	OnDiagnostic      func(string, DiagnosticRecord)
}

// Service is the room terminal policy contract. Implementations are
// stateless per-service values so independent consumers cannot share terminal
// state accidentally.
type Service interface {
	FinalObservation(FinalObservationRequest) (Observation, bool)
	FailureOptional(*FailureFacts, error) Observation
	Failure(FailureFacts, error) Observation
	MessageEnd(string, *messages.MessageEndValue) Observation
	Cancellation(messages.TerminalOutputState, bool) Observation
	Provenance(string, string) string
	BoundTrigger(string, bool) string
	Fields(ParticipantResult) map[string]string
	Diagnostic(ParticipantResult) DiagnosticRecord
	IsBoundTrigger(string) bool
	RecordBound(BoundDiagnosticRequest)
	Close() error
}
