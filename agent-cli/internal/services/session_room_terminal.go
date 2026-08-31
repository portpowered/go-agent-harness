package services

import (
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// sessionTerminalObservation is the private bridge between the session
// diagnostic observer and the room lifecycle. It contains only the bounded,
// provider-neutral terminal fields that the room exposes in its result and
// evidence; Err is retained in-process solely so a genuine failure can keep
// its error identity until the existing room redaction boundary.
type sessionTerminalObservation struct {
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

type roomParticipantTerminalObservation struct {
	terminationTrigger     string
	terminationDisposition string
	classification         string
	terminalReason         string
	terminalProvenance     string
	outputState            string
	err                    error
	failure                bool
}

func defaultRoomTerminalProvenance(disposition, reason string) string {
	if disposition == ParticipantTerminationDispositionCancelledAfterGrace {
		return string(messages.TerminalProvenanceRoom)
	}
	switch messages.TerminalReason(reason) {
	case messages.TerminalReasonProviderAuthoredCompletion:
		return string(messages.TerminalProvenanceProvider)
	case messages.TerminalReasonLoopSynthesizedCompletion:
		return string(messages.TerminalProvenanceLoop)
	case messages.TerminalReasonProviderClose:
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
	case ParticipantTerminationDispositionCancelledAfterGrace,
		ParticipantTerminationDispositionStopped:
		return string(messages.TerminalProvenanceLoop)
	default:
		return string(messages.TerminalProvenanceSession)
	}
}

func (o *sessionProgressObserver) notifyFinalTerminalObservation(err error) {
	if o == nil || o.terminalObserver == nil {
		return
	}
	if o.failure != nil {
		o.notifyFailureObservation(sessionTerminalObservationFromFailure(o.failure, err))
		return
	}
	if o.roomBoundCancellation && roomCancellationOnly(err) {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), true))
		return
	}
	if o.userCancelled {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), false))
		return
	}
	if err != nil && !roomCancellationOnly(err) {
		facts := factsFromSessionRunError(err)
		if facts == nil {
			classification := providers.ErrorClassification(err)
			if classification == "" {
				classification = providers.ErrorClassUnknown
			}
			facts = &failureFacts{
				classification: classification,
				terminalReason: string(messages.TerminalReasonTerminalFailure),
				provenance:     string(messages.TerminalProvenanceCLI),
				outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
				failingEvent:   failingEventRun,
			}
		}
		o.acceptFailureObservation(facts, err)
		return
	}
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return
	}
	// A cancellation that did not carry a room-bound marker is still useful to
	// the room result, where it becomes an intentional stopped disposition.
	if roomCancellationOnly(err) {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), false))
	}
}

func sessionTerminalObservationFromFailure(facts *failureFacts, err error) sessionTerminalObservation {
	if facts == nil {
		return sessionTerminalObservation{}
	}
	if err == nil && facts.errorType != "" {
		err = errors.New(facts.errorType)
	}
	if err == nil {
		err = errors.New("session stream error")
	}
	return sessionTerminalObservation{
		Classification:     facts.classification,
		TerminalReason:     facts.terminalReason,
		TerminalProvenance: facts.provenance,
		OutputState:        facts.outputState,
		Err:                err,
		Failure:            true,
		Code:               facts.code,
		FailingEvent:       facts.failingEvent,
	}
}

func sessionTerminalObservationFromMessageEnd(responseID string, value *messages.MessageEndValue) sessionTerminalObservation {
	if value == nil {
		return sessionTerminalObservation{}
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
	return sessionTerminalObservation{
		ResponseID:         responseID,
		Classification:     "",
		TerminalReason:     string(reason),
		TerminalProvenance: string(provenance),
		OutputState:        string(outputState),
	}
}

func sessionTerminalObservationForCancellation(outputState messages.TerminalOutputState, roomBound bool) sessionTerminalObservation {
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	classification := providers.ErrorClassCancellation
	provenance := messages.TerminalProvenanceCLI
	if roomBound {
		classification = RoomBoundCancelledClassification
		provenance = messages.TerminalProvenanceRoom
	}
	return sessionTerminalObservation{
		Classification:     classification,
		TerminalReason:     string(messages.TerminalReasonCancellation),
		TerminalProvenance: string(provenance),
		OutputState:        string(outputState),
		RoomBound:          roomBound,
	}
}

func roomBoundTerminationTrigger(reason RoomTerminationReason, midResponse bool) string {
	switch reason {
	case RoomTerminationMaxTurnsReached:
		if midResponse {
			return ParticipantTerminationTriggerMaxTurnsReachedMidResponse
		}
		return ParticipantTerminationTriggerMaxTurnsReached
	case RoomTerminationMaxDurationReached:
		if midResponse {
			return ParticipantTerminationTriggerMaxDurationReachedMidResponse
		}
		return ParticipantTerminationTriggerMaxDurationReached
	default:
		return string(reason)
	}
}

func participantTerminalFields(result RoomParticipantResult) map[string]string {
	return map[string]string{
		"termination_trigger":     result.TerminationTrigger,
		"termination_disposition": result.TerminationDisposition,
		"classification":          result.Classification,
		"terminal_reason":         result.TerminalReason,
		"terminal_provenance":     result.TerminalProvenance,
		"output_state":            result.OutputState,
		"reason":                  string(result.TerminationReason),
	}
}

func participantTerminationDiagnostic(result RoomParticipantResult) SessionDiagnosticRecord {
	return SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventRoomBound,
		Fields: participantTerminalFields(result),
	}
}

func isRoomBoundParticipantTrigger(trigger string) bool {
	return strings.HasPrefix(trigger, "max_duration_reached") || strings.HasPrefix(trigger, "max_turns_reached")
}

func recordRoomParticipantBoundDiagnostic(opts RoomRunOptions, evidence *roomEvidence, result RoomParticipantResult) {
	if !isRoomBoundParticipantTrigger(result.TerminationTrigger) {
		return
	}
	record := participantTerminationDiagnostic(result)
	if evidence != nil {
		if participant := evidence.participant(result.ParticipantID); participant != nil {
			participant.RecordSessionDiagnostic(record)
		}
		evidence.recordTimelineEvent("room_bound_shutdown", result.ParticipantID, record.Fields)
	}
	if opts.Stream != nil {
		opts.Stream.RecordDiagnostic(result.ParticipantID, record)
	}
	if opts.OnDiagnostic != nil {
		opts.OnDiagnostic(result.ParticipantID, record)
	}
}
