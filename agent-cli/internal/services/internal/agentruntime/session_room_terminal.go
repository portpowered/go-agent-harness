package agentruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// sessionTerminalObservation is the bounded, provider-neutral bridge between
// session diagnostics and the owner of a session lifecycle. It carries error
// identity only in-process; public result and evidence boundaries sanitize it.
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

const RoomBoundCancelledClassification = providers.ErrorClassRoomBoundCancelled

func roomCancellationOnly(err error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !roomCancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return roomCancellationOnly(cause)
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
	if o.roomBoundCancellation && o.failure == nil && roomCancellationOnly(err) {
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
	if roomCancellationOnly(err) {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), false))
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
	if opts.OnDiagnostic != nil {
		opts.OnDiagnostic(result.ParticipantID, record)
	}
}
