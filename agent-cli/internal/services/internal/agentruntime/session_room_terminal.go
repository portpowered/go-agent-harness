package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	rt "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal"
	rtw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal/wire"
)

type sessionTerminalObservation = rt.Observation // Deprecated: compatibility alias; terminal policy lives in roomterminal.
type roomParticipantTerminalObservation struct {
	terminationTrigger, terminationDisposition, classification, terminalReason, terminalProvenance, outputState string
	err                                                                                                         error
	failure                                                                                                     bool
}

func s() rt.Service { return rtw.NewService() } // Deprecated: decision-free adapter; policy lives in roomterminal.
func runtimeFailureFacts(f *failureFacts) *rt.FailureFacts {
	if f == nil {
		return nil
	}
	return &rt.FailureFacts{Classification: f.classification, TerminalReason: f.terminalReason, Provenance: f.provenance, OutputState: f.outputState, ErrorType: f.errorType, Code: f.code, FailingEvent: f.failingEvent}
}
func (o *sessionProgressObserver) notifyFinalTerminalObservation(err error) {
	if o == nil || o.terminalObserver == nil {
		return
	}
	facts := o.failure
	if facts == nil && err != nil && !roomCancellationOnly(err) {
		facts = factsFromSessionRunError(err)
	}
	observation, ok := s().FinalObservation(rt.FinalObservationRequest{
		Failure: runtimeFailureFacts(facts), FailureError: err, RunError: err, CancellationOnly: roomCancellationOnly(err), RoomBoundCancellation: o.roomBoundCancellation,
		UserCancelled: o.userCancelled, UserCancellationOutputState: o.userCancellationOutputState(), SawSessionOpen: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted,
		FallbackProvenance: string(messages.TerminalProvenanceCLI), FallbackFailingEvent: failingEventRun,
	})
	if !ok {
		return
	}
	if observation.Failure {
		o.notifyFailureObservation(observation)
		return
	}
	o.notifyTerminalObservation(observation)
}
func sessionTerminalObservationFromFailure(f *failureFacts, e error) sessionTerminalObservation {
	return s().FailureOptional(runtimeFailureFacts(f), e)
}
func sessionTerminalObservationFromMessageEnd(i string, v *messages.MessageEndValue) sessionTerminalObservation {
	return s().MessageEnd(i, v)
}
func sessionTerminalObservationForCancellation(o messages.TerminalOutputState, b bool) sessionTerminalObservation {
	return s().Cancellation(o, b)
}
func defaultRoomTerminalProvenance(d, r string) string { return s().Provenance(d, r) }
func roomBoundTerminationTrigger(r RoomTerminationReason, m bool) string {
	return s().BoundTrigger(string(r), m)
}
func runtimeParticipantResult(r RoomParticipantResult) rt.ParticipantResult {
	return rt.ParticipantResult{TerminationTrigger: r.TerminationTrigger, TerminationDisposition: r.TerminationDisposition, Classification: r.Classification, TerminalReason: r.TerminalReason, TerminalProvenance: r.TerminalProvenance, OutputState: r.OutputState, Reason: string(r.TerminationReason)}
}
func participantTerminalFields(r RoomParticipantResult) map[string]string {
	return s().Fields(runtimeParticipantResult(r))
}
func participantTerminationDiagnostic(r RoomParticipantResult) SessionDiagnosticRecord {
	v := s().Diagnostic(runtimeParticipantResult(r))
	return SessionDiagnosticRecord{Event: v.Event, Fields: v.Fields}
}
func isRoomBoundParticipantTrigger(t string) bool { return s().IsBoundTrigger(t) }
func recordRoomParticipantBoundDiagnostic(opts RoomRunOptions, evidence *roomEvidence, result RoomParticipantResult) {
	s().RecordBound(rt.BoundDiagnosticRequest{
		ParticipantID: result.ParticipantID, Result: runtimeParticipantResult(result),
		RecordParticipant: func(id string, record rt.DiagnosticRecord) {
			if participant := evidence.participant(id); participant != nil && isRoomBoundParticipantTrigger(result.TerminationTrigger) {
				participant.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: record.Event, Fields: record.Fields})
			}
		},
		RecordTimeline: func(event, id string, fields map[string]string) { evidence.recordTimelineEvent(event, id, fields) },
		OnDiagnostic: func(id string, record rt.DiagnosticRecord) {
			if opts.OnDiagnostic != nil {
				opts.OnDiagnostic(id, participantTerminationDiagnostic(result))
			}
		},
	})
}
