package agentruntime

import (
	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
	w "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure/wire"
)

type failureFacts struct{ classification, terminalReason, provenance, outputState, errorType, code, failingEvent string } // Deprecated: forwarding compatibility only.
func failureFactsFromPublic(f sf.Facts) *failureFacts {
	return &failureFacts{f.Classification, f.TerminalReason, f.Provenance, f.OutputState, f.ErrorType, f.Code, f.FailingEvent}
}
func publicFailureFacts(f *failureFacts) sf.Facts {
	return sf.Facts{f.classification, f.terminalReason, f.provenance, f.outputState, f.errorType, f.code, f.failingEvent}
}
func (o *sessionProgressObserver) failureSnapshot() *failureFacts {
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	if o.failure == nil {
		return nil
	}
	copy := *o.failure
	return &copy
}
func (o *sessionProgressObserver) clearFailure() {
	o.livenessMu.Lock()
	o.failure = nil
	o.livenessMu.Unlock()
}
func (o *sessionProgressObserver) captureFailureFromError(v *m.ErrorValue) {
	f, err := newFailureService(o).NormalizeErrorValue(v)
	o.acceptFailureObservation(failureFactsFromPublic(f), err)
}
func factsFromSessionRunError(err error) *failureFacts {
	f := newFailureService(nil).FactsFromSessionRunError(err)
	if f == nil {
		return nil
	}
	return failureFactsFromPublic(*f)
}
func (o *sessionProgressObserver) acceptFailureObservation(f *failureFacts, err error) bool {
	return newFailureService(o).Accept(publicFailureFacts(f), err)
}
func (o *sessionProgressObserver) captureFailureFromClose(v *m.SessionCloseValue) {
	o.acceptFailureObservation(failureFactsFromPublic(newFailureService(o).NormalizeClose(v, sf.Progress{SessionOpened: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted})), nil)
}
func (o *sessionProgressObserver) unresolvedToolResultFailureFacts(e string) *failureFacts {
	return failureFactsFromPublic(newFailureService(nil).Projection(sf.ProjectionUnresolvedTool, e, sf.Progress{SessionOpened: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted}))
}
func (o *sessionProgressObserver) imageContinuationFailureFacts(e string) *failureFacts {
	return failureFactsFromPublic(newFailureService(nil).Projection(sf.ProjectionImageContinuation, e, sf.Progress{SessionOpened: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted}))
}
func (o *sessionProgressObserver) toolContinuationFailureFacts(e string) *failureFacts {
	return failureFactsFromPublic(newFailureService(nil).Projection(sf.ProjectionToolContinuation, e, sf.Progress{SessionOpened: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted}))
}
func (o *sessionProgressObserver) scheduledAudioFailureFacts(e string) *failureFacts {
	return failureFactsFromPublic(newFailureService(nil).Projection(sf.ProjectionScheduledAudio, e, sf.Progress{SessionOpened: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted}))
}
func (o *sessionProgressObserver) emitToolCallRecord(v *m.ToolCallEndValue) {
	if o.sink == nil || v == nil {
		return
	}
	newFailureService(o).EmitUnsupportedTool(sf.ToolCall{Name: v.Name, ID: v.ToolCallID, TurnIndex: o.turnsCompleted + 1})
}
func deriveOutputState(open bool, turns int) string {
	return newFailureService(nil).OutputState(sf.Progress{SessionOpened: open, TurnsCompleted: turns})
}
func publishFailure(o *sessionProgressObserver, ob sf.Observation) bool {
	f := failureFactsFromPublic(ob.Facts)
	o.livenessMu.Lock()
	accepted := o.failure == nil
	if accepted {
		o.failure = f
	}
	o.livenessMu.Unlock()
	ok := accepted && o.notifyFailureObservation(sessionTerminalObservationFromFailure(f, ob.Err))
	if accepted && !ok {
		o.clearFailure()
	}
	return ok
}
func newFailureService(o *sessionProgressObserver) sf.Service {
	return w.NewService(sf.Dependencies{Publish: func(ob sf.Observation) bool { return publishFailure(o, ob) }, Sink: sf.DiagnosticSinkFunc(func(r sf.DiagnosticRecord) {
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: r.Event, Fields: r.Fields})
	})})
}
