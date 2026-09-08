package lifecycle

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func (s *runState) result(ctx context.Context) (rooms.RoomResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	participants := cloneParticipantResults(s.results)
	result := rooms.RoomResult{Participants: participants, TerminationReason: rooms.RoomTerminationStopped}
	runErr := s.terminationCause(ctx)
	applyRoomTermination(&result, s.boundReason, runErr)
	for id, value := range participants {
		appendParticipantResult(&result, id, value)
	}
	return result, runErr
}

func cloneParticipantResults(results map[string]rooms.RoomParticipantResult) map[string]rooms.RoomParticipantResult {
	participants := make(map[string]rooms.RoomParticipantResult, len(results))
	for id, value := range results {
		participants[id] = value
	}
	return participants
}

func applyRoomTermination(result *rooms.RoomResult, boundReason rooms.RoomTerminationReason, runErr error) {
	if result == nil {
		return
	}
	if boundReason != "" {
		result.TerminationReason, result.Reason = boundReason, boundReason
	} else {
		result.Reason = result.TerminationReason
	}
	if runErr != nil {
		result.TerminationReason, result.Reason = rooms.RoomTerminationFailed, rooms.RoomTerminationFailed
		result.Error = runErr.Error()
	}
}

func (s *runState) terminationCause(ctx context.Context) error {
	if s.boundReason == rooms.RoomTerminationFailed {
		return s.boundCause
	}
	if s.boundReason != "" || ctx == nil {
		return nil
	}
	err := ctx.Err()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func appendParticipantResult(result *rooms.RoomResult, id string, value rooms.RoomParticipantResult) {
	switch value.TerminationReason {
	case rooms.ParticipantTerminationEnded, rooms.ParticipantTerminationDisconnected, rooms.ParticipantTerminationError:
		return
	default:
		result.ActiveParticipants = append(result.ActiveParticipants, id)
	}
}
