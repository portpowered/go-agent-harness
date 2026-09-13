package agentruntime

import (
	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
	sfw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure/wire"
)

type failureFacts struct{ classification, terminalReason, provenance, outputState, errorType, code, failingEvent string } // Deprecated: forwarding compatibility only.
type observer = sessionProgressObserver

func ff(v sf.Facts) *failureFacts {
	if v.FailingEvent == "" {
		return nil
	}
	return &failureFacts{v.Classification, v.TerminalReason, v.Provenance, v.OutputState, v.ErrorType, v.Code, v.FailingEvent}
}
func pf(f *failureFacts) sf.Facts {
	return sf.Facts{Classification: f.classification, TerminalReason: f.terminalReason, Provenance: f.provenance, OutputState: f.outputState, ErrorType: f.errorType, Code: f.code, FailingEvent: f.failingEvent}
}
func progress(o *observer) sf.Progress { return sfw.Progress(o.sawSessionOpen, o.turnsCompleted) }
func (o *observer) failureSnapshot() *failureFacts {
	if o == nil {
		return nil
	}
	if facts := ff(fi(o).Failure.SnapshotFacts()); facts != nil {
		return facts
	}
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	return o.failure
}
func (o *observer) clearFailure()                           { fi(o).Failure.Clear(); setFailure(o, nil) }
func (o *observer) captureFailureFromError(v *m.ErrorValue) { fi(o).Failure.AcceptError(v) }
func factsFromSessionRunError(err error) *failureFacts      { return ff(sfw.RunFacts(err)) }
func (o *observer) acceptFailureObservation(f *failureFacts, err error) bool {
	return o != nil && f != nil && fi(o).Failure.Accept(pf(f), err)
}
func (o *observer) captureFailureFromClose(v *m.SessionCloseValue) {
	if o != nil {
		fi(o).Failure.AcceptClose(v, progress(o))
	}
}

const u, i, t, a = sf.ProjectionUnresolvedTool, sf.ProjectionImageContinuation, sf.ProjectionToolContinuation, sf.ProjectionScheduledAudio

func p(o *observer, kind sf.Projection, e string) *failureFacts {
	if o == nil {
		return nil
	}
	return ff(sfw.Facts(kind, e, progress(o)))
}
func (o *observer) unresolvedToolResultFailureFacts(e string) *failureFacts { return p(o, u, e) }
func (o *observer) imageContinuationFailureFacts(e string) *failureFacts    { return p(o, i, e) }
func (o *observer) toolContinuationFailureFacts(e string) *failureFacts     { return p(o, t, e) }
func (o *observer) scheduledAudioFailureFacts(e string) *failureFacts       { return p(o, a, e) }
func (o *observer) emitToolCallRecord(v *m.ToolCallEndValue) {
	if o != nil && o.sink != nil && v != nil {
		fi(o).Failure.EmitUnsupportedTool(sf.ToolCall{Name: v.Name, ID: v.ToolCallID, TurnIndex: o.turnsCompleted + 1})
	}
}
func deriveOutputState(open bool, turns int) string { return sfw.OutputStateForProgress(open, turns) }
func fi(o *observer) *sfw.Invocation {
	if o == nil {
		return sfw.NewInvocation(nil, sf.Dependencies{})
	}
	o.lifecycleProjectionMu.Lock()
	defer o.lifecycleProjectionMu.Unlock()
	if holder, ok := o.lifecycle.(*sfw.Invocation); ok {
		return holder
	}
	holder := sfw.NewInvocation(o.ensureLifecycle(), sf.Dependencies{Publish: func(ob sf.Observation) bool {
		f := ff(ob.Facts)
		setFailure(o, f)
		return o.notifyFailureObservation(sessionTerminalObservationFromFailure(f, ob.Err))
	}, Rollback: func() { setFailure(o, nil) }, Sink: sf.DiagnosticSinkFunc(func(r sf.DiagnosticRecord) {
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: r.Event, Fields: r.Fields})
	})})
	o.lifecycle = holder
	return holder
}
func setFailure(o *observer, f *failureFacts) {
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	o.failure = f
}
