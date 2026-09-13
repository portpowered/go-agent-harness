package service

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func (s *admissionState) awaitReadiness() error {
	readinessDone := timerFor(s.options.TimerFactory, s.options.AdmissionTimeout)
	for {
		if s.allParticipantsReady() {
			return nil
		}
		select {
		case <-s.options.Coordinator.Done():
			return s.options.Coordinator.RoomError()
		case <-s.ctx.Done():
			s.options.Coordinator.Stop(roomplanning.TerminationStopped)
			return nil
		case <-s.options.Timer:
			s.options.Coordinator.Stop(roomplanning.TerminationMaxDurationReached)
			return nil
		case <-readinessDone:
			if s.failUnreadyParticipants() {
				return nil
			}
		case <-s.options.Coordinator.Progress():
		}
	}
}

func (s *admissionState) allParticipantsReady() bool {
	allReady := true
	for _, participant := range s.options.Participants {
		if participant.ID == "" || !s.options.Coordinator.IsActive(participant.ID) {
			continue
		}
		ready, err := participantReady(s.options.Coordinator, participant, s.options.ParticipantError)
		if err != nil {
			s.options.Coordinator.FailParticipant(participant.ID, err)
			continue
		}
		if !ready {
			allReady = false
		}
	}
	return allReady
}

func (s *admissionState) failUnreadyParticipants() bool {
	outstanding := make([]string, 0, len(s.options.Participants))
	for _, participant := range s.options.Participants {
		if participant.ID == "" || !s.options.Coordinator.IsActive(participant.ID) {
			continue
		}
		snapshot := participantSnapshot(participant)
		if participant.Kind == rooms.ParticipantKindHuman {
			if !snapshot.DeviceReady {
				s.options.Coordinator.FailParticipant(participant.ID, s.options.ParticipantError(participant.ID, errors.New("human participant devices were not ready")))
			}
			continue
		}
		if !snapshot.Opened {
			outstanding = append(outstanding, participant.ID)
		}
	}
	for _, participantID := range outstanding {
		s.options.Coordinator.FailParticipant(participantID, s.options.ParticipantError(participantID, errors.New("session did not become ready before admission deadline")))
	}
	return s.options.Coordinator.IsStopping()
}

func participantReady(coordinator roomplanning.AdmissionCoordinator, participant roomplanning.AdmissionParticipant, failure func(string, error) error) (bool, error) {
	snapshot := participantSnapshot(participant)
	if participant.Kind == rooms.ParticipantKindHuman {
		if snapshot.DeviceReady {
			return true, nil
		}
		if snapshot.RunFinished || coordinator.IsStopping() {
			return false, failure(participant.ID, errors.New("human participant devices were not ready"))
		}
		return false, nil
	}
	if snapshot.Opened {
		return true, nil
	}
	if snapshot.TransportEnded && !snapshot.RunFinished {
		return false, nil
	}
	if snapshot.Closed || snapshot.TransportEnded || snapshot.RunFinished {
		if snapshot.TerminalObserved && snapshot.TerminalErr != nil {
			return false, failure(participant.ID, snapshot.TerminalErr)
		}
		return false, failure(participant.ID, errors.New("session ended before SESSION.OPEN"))
	}
	return false, nil
}

func participantSnapshot(participant roomplanning.AdmissionParticipant) roomplanning.LifecycleSnapshot {
	if participant.Snapshot == nil {
		return roomplanning.LifecycleSnapshot{}
	}
	return participant.Snapshot()
}
