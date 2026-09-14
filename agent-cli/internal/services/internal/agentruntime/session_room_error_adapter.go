package agentruntime

import (
	roomErrors "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
	roomErrorsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors/wire"
)

// Deprecated: these compatibility functions forward the CLI shape to the host-neutral contract.
func roomParticipantFailure(participantID string, err error, secrets []string) error {
	return roomErrorsWire.NewService().ParticipantFailure(roomErrors.ParticipantFailureRequest{ParticipantID: participantID, Cause: err, Secrets: secrets})
}
func roomParticipantFailureReason(err error, terminationReason ParticipantTerminationReason, closeReason string, transportEnded bool, secrets []string) string {
	return roomErrorsWire.NewService().ParticipantFailureReason(roomErrors.ParticipantFailureReasonRequest{Error: err, TerminationReason: roomErrors.ParticipantTerminationReason(terminationReason), CloseReason: closeReason, TransportDisconnected: transportEnded, Secrets: secrets})
}
func roomFailureResult(err error, secrets []string) RoomResult {
	result := roomErrorsWire.NewService().FailureResult(err, secrets)
	participants := make(map[string]RoomParticipantResult, len(result.Participants))
	for participantID := range result.Participants {
		participants[participantID] = RoomParticipantResult{ID: participantID, ParticipantID: participantID}
	}
	return RoomResult{TerminationReason: RoomTerminationReason(result.TerminationReason), Reason: RoomTerminationReason(result.Reason), Error: result.Error, Participants: participants}
}
func sanitizeRoomError(err error, secrets []string) string {
	return roomErrorsWire.NewService().Sanitize(err, secrets)
}
func roomParticipantFailureID(err error) (string, bool) {
	return roomErrorsWire.NewService().ParticipantFailureID(err)
}
func preserveRoomObservationError(err, observation error) error {
	if observation == nil {
		return err
	}
	if _, ok := roomParticipantFailureID(err); ok {
		return err
	}
	return observation
}
func secretsForPlan(plan *roomParticipantPlan) []string {
	if plan == nil || plan.secret == "" {
		return nil
	}
	return []string{plan.secret}
}
