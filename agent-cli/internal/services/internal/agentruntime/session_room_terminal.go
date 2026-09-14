package agentruntime

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
