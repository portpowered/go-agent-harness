package service

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
)

func (s *admissionState) awaitConnections() error {
	for s.remaining > 0 {
		select {
		case outcome, ok := <-s.options.Outcomes:
			if !ok {
				s.options.Outcomes = nil
				continue
			}
			s.recordOutcome(outcome)
		case <-s.ctxDone:
			s.options.Coordinator.Stop(roomplanning.TerminationStopped)
			s.ctxDone = nil
		case <-s.timerDone:
			s.options.Coordinator.Stop(roomplanning.TerminationMaxDurationReached)
			s.timerDone = nil
		case <-s.admissionDone:
			s.failAdmissionDeadline()
		case <-s.roomDone:
			s.roomDone = nil
			s.options.Cleanup.Start()
		case <-s.options.Cleanup.Done():
			return s.options.LifecycleError(s.admissionOutstanding()...)
		}
	}
	return nil
}

func (s *admissionState) recordOutcome(outcome roomplanning.ConnectionOutcome) {
	participant, ok := s.byTracker[outcome.Tracker]
	if !ok {
		return
	}
	if _, duplicate := s.seen[outcome.Tracker]; duplicate {
		return
	}
	s.seen[outcome.Tracker] = struct{}{}
	s.remaining--
	if participant.MarkConnected != nil {
		participant.MarkConnected(outcome.Err)
	}
	if outcome.Err != nil {
		cause := fmt.Errorf("connect live session: %w", outcome.Err)
		s.options.Coordinator.FailParticipant(participant.ID, s.options.ParticipantError(participant.ID, cause))
	}
}

func (s *admissionState) failAdmissionDeadline() {
	outstanding := make([]string, 0, s.remaining)
	for tracker, participant := range s.byTracker {
		if _, done := s.seen[tracker]; done {
			continue
		}
		outstanding = append(outstanding, s.options.LifecycleLabel(participant.ID, "connect"))
	}
	s.options.Coordinator.Fail(s.options.LifecycleError(outstanding...))
	s.admissionDone = nil
	s.options.Cleanup.Start()
}

func (s *admissionState) admissionOutstanding() []string {
	outstanding := make([]string, 0, len(s.byTracker))
	for tracker, participant := range s.byTracker {
		if _, done := s.seen[tracker]; !done {
			outstanding = append(outstanding, s.options.LifecycleLabel(participant.ID, "connect"))
		}
	}
	for _, participant := range s.options.Participants {
		if participant.OutstandingWork != nil {
			outstanding = append(outstanding, participant.OutstandingWork()...)
		}
	}
	return outstanding
}
