package service

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// Service owns the room terminal projection without retaining session state.
// Every input that can affect the result is supplied explicitly by the host.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) FinalObservation(request roomterminal.FinalObservationRequest) (roomterminal.Observation, bool) {
	if request.Failure != nil {
		err := request.FailureError
		if err == nil {
			err = request.RunError
		}
		return s.Failure(*request.Failure, err), true
	}
	if request.RoomBoundCancellation && request.CancellationOnly {
		return s.Cancellation(request.UserCancellationOutputState, true), true
	}
	if request.UserCancelled {
		return s.Cancellation(request.UserCancellationOutputState, false), true
	}
	if request.RunError != nil && !request.CancellationOnly {
		return s.Failure(finalRunErrorFacts(request), request.RunError), true
	}
	if request.RunError == nil || strings.TrimSpace(request.RunError.Error()) == "" {
		return roomterminal.Observation{}, false
	}
	if request.CancellationOnly {
		return s.Cancellation(request.UserCancellationOutputState, false), true
	}
	return roomterminal.Observation{}, false
}

func finalRunErrorFacts(request roomterminal.FinalObservationRequest) roomterminal.FailureFacts {
	facts := roomterminal.FailureFacts{
		Classification: providers.ErrorClassification(request.RunError),
		TerminalReason: string(messages.TerminalReasonTerminalFailure),
		Provenance:     request.FallbackProvenance,
		OutputState:    deriveOutputState(request.SawSessionOpen, request.TurnsCompleted),
		FailingEvent:   request.FallbackFailingEvent,
	}
	if facts.Classification == "" {
		facts.Classification = providers.ErrorClassUnknown
	}
	if facts.Provenance == "" {
		facts.Provenance = string(messages.TerminalProvenanceCLI)
	}
	if facts.FailingEvent == "" {
		facts.FailingEvent = "SESSION.RUN"
	}
	return facts
}

func (s *Service) Failure(facts roomterminal.FailureFacts, err error) roomterminal.Observation {
	if err == nil && facts.ErrorType != "" {
		err = errors.New(facts.ErrorType)
	}
	if err == nil {
		err = errors.New("session stream error")
	}
	return roomterminal.Observation{
		Classification:     facts.Classification,
		TerminalReason:     facts.TerminalReason,
		TerminalProvenance: facts.Provenance,
		OutputState:        facts.OutputState,
		Err:                err,
		Failure:            true,
		Code:               facts.Code,
		FailingEvent:       facts.FailingEvent,
	}
}

func (s *Service) FailureOptional(facts *roomterminal.FailureFacts, err error) roomterminal.Observation {
	if facts == nil {
		return roomterminal.Observation{}
	}
	return s.Failure(*facts, err)
}

func (s *Service) MessageEnd(responseID string, value *messages.MessageEndValue) roomterminal.Observation {
	if value == nil {
		return roomterminal.Observation{}
	}
	reason := value.TerminalReason
	if reason == "" {
		reason = messages.TerminalReasonProviderAuthoredCompletion
	}
	provenance := value.TerminalProvenance
	if provenance == "" {
		provenance = messages.TerminalProvenanceProvider
	}
	outputState := value.OutputState
	if outputState == "" {
		outputState = messages.TerminalOutputComplete
	}
	return roomterminal.Observation{
		ResponseID:         responseID,
		TerminalReason:     string(reason),
		TerminalProvenance: string(provenance),
		OutputState:        string(outputState),
	}
}

func (s *Service) Cancellation(outputState messages.TerminalOutputState, roomBound bool) roomterminal.Observation {
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	classification := providers.ErrorClassCancellation
	provenance := messages.TerminalProvenanceCLI
	if roomBound {
		classification = providers.ErrorClassRoomBoundCancelled
		provenance = messages.TerminalProvenanceRoom
	}
	return roomterminal.Observation{
		Classification:     classification,
		TerminalReason:     string(messages.TerminalReasonCancellation),
		TerminalProvenance: string(provenance),
		OutputState:        string(outputState),
		RoomBound:          roomBound,
	}
}

func (s *Service) Provenance(disposition, reason string) string {
	if disposition == roomterminal.ParticipantTerminationDispositionCancelledAfterGrace {
		return string(messages.TerminalProvenanceRoom)
	}
	switch messages.TerminalReason(reason) {
	case messages.TerminalReasonProviderAuthoredCompletion:
		return string(messages.TerminalProvenanceProvider)
	case messages.TerminalReasonLoopSynthesizedCompletion:
		return string(messages.TerminalProvenanceLoop)
	case messages.TerminalReasonProviderClose:
		return string(messages.TerminalProvenanceSession)
	case messages.TerminalReasonSessionClose:
		return string(messages.TerminalProvenanceSession)
	case messages.TerminalReasonReplayComplete,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete:
		return string(messages.TerminalProvenanceReplay)
	case messages.TerminalReasonCancellation,
		messages.TerminalReasonPartialOutput:
		return string(messages.TerminalProvenanceLoop)
	case messages.TerminalReasonTerminalFailure:
		return string(messages.TerminalProvenanceSession)
	}
	switch disposition {
	case roomterminal.ParticipantTerminationDispositionCancelledAfterGrace,
		roomterminal.ParticipantTerminationDispositionStopped:
		return string(messages.TerminalProvenanceLoop)
	default:
		return string(messages.TerminalProvenanceSession)
	}
}

func (s *Service) BoundTrigger(reason string, midResponse bool) string {
	switch reason {
	case roomterminal.RoomTerminationMaxTurnsReached:
		if midResponse {
			return roomterminal.ParticipantTerminationTriggerMaxTurnsReachedMidResponse
		}
		return roomterminal.ParticipantTerminationTriggerMaxTurnsReached
	case roomterminal.RoomTerminationMaxDurationReached:
		if midResponse {
			return roomterminal.ParticipantTerminationTriggerMaxDurationReachedMidResponse
		}
		return roomterminal.ParticipantTerminationTriggerMaxDurationReached
	default:
		return reason
	}
}

func (s *Service) Fields(result roomterminal.ParticipantResult) map[string]string {
	return map[string]string{
		"termination_trigger":     result.TerminationTrigger,
		"termination_disposition": result.TerminationDisposition,
		"classification":          result.Classification,
		"terminal_reason":         result.TerminalReason,
		"terminal_provenance":     result.TerminalProvenance,
		"output_state":            result.OutputState,
		"reason":                  result.Reason,
	}
}

func (s *Service) Diagnostic(result roomterminal.ParticipantResult) roomterminal.DiagnosticRecord {
	return roomterminal.DiagnosticRecord{
		Event:  roomterminal.DiagnosticEventRoomBound,
		Fields: s.Fields(result),
	}
}

func (s *Service) IsBoundTrigger(trigger string) bool {
	return strings.HasPrefix(trigger, "max_duration_reached") || strings.HasPrefix(trigger, "max_turns_reached")
}

func (s *Service) RecordBound(request roomterminal.BoundDiagnosticRequest) {
	if !s.IsBoundTrigger(request.Result.TerminationTrigger) {
		return
	}
	record := s.Diagnostic(request.Result)
	if request.RecordParticipant != nil {
		request.RecordParticipant(request.ParticipantID, cloneDiagnostic(record))
	}
	if request.RecordTimeline != nil {
		request.RecordTimeline(roomterminal.DiagnosticEventRoomBound, request.ParticipantID, cloneDiagnostic(record).Fields)
	}
	if request.OnDiagnostic != nil {
		request.OnDiagnostic(request.ParticipantID, cloneDiagnostic(record))
	}
}

func (s *Service) Close() error { return nil }

func deriveOutputState(sawSessionOpen bool, turnsCompleted int) string {
	if !sawSessionOpen {
		return string(messages.TerminalOutputNone)
	}
	if turnsCompleted > 0 {
		return string(messages.TerminalOutputPartial)
	}
	return string(messages.TerminalOutputNone)
}

func cloneDiagnostic(record roomterminal.DiagnosticRecord) roomterminal.DiagnosticRecord {
	fields := make(map[string]string, len(record.Fields))
	for key, value := range record.Fields {
		fields[key] = value
	}
	record.Fields = fields
	return record
}

var _ roomterminal.Service = (*Service)(nil)
