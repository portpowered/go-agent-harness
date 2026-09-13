package agentruntime

import (
	roomErrors "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
	roomErrorsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors/wire"
)

func roomErrorService() roomErrors.Service { return roomErrorsWire.NewService() }

// Deprecated: use the sessionroomerrors service; this adapter only maps the
// legacy CLI request shape to the host-neutral contract.
func roomParticipantFailure(participantID string, err error, secrets []string) error {
	return roomErrorService().ParticipantFailure(roomErrors.ParticipantFailureRequest{ParticipantID: participantID, Cause: err, Secrets: secrets})
}

// Deprecated: use sessionroomerrors.Service.ParticipantFailureReason.
func roomParticipantFailureReason(err error, terminationReason ParticipantTerminationReason, closeReason string, transportEnded bool, secrets []string) string {
	return roomErrorService().ParticipantFailureReason(roomErrors.ParticipantFailureReasonRequest{Error: err, TerminationReason: roomErrors.ParticipantTerminationReason(terminationReason), CloseReason: closeReason, TransportDisconnected: transportEnded, Secrets: secrets})
}

// Deprecated: use sessionroomerrors.Service.FailureResult.
func roomFailureResult(err error, secrets []string) RoomResult {
	result := roomErrorService().FailureResult(err, secrets)
	participants := make(map[string]RoomParticipantResult, len(result.Participants))
	for participantID := range result.Participants {
		participants[participantID] = RoomParticipantResult{ID: participantID, ParticipantID: participantID}
	}
	return RoomResult{TerminationReason: RoomTerminationReason(result.TerminationReason), Reason: RoomTerminationReason(result.Reason), Error: result.Error, Participants: participants}
}

// Deprecated: use sessionroomerrors.Service.Sanitize.
func sanitizeRoomError(err error, secrets []string) string {
	return roomErrorService().Sanitize(err, secrets)
}

// Deprecated: this only adapts the CLI plan's already-resolved secret.
func secretsForPlan(plan *roomParticipantPlan) []string {
	if plan == nil || plan.secret == "" {
		return nil
	}
	return []string{plan.secret}
}
