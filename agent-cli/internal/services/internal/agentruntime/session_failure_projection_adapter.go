package agentruntime

import (
	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
	sfw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure/wire"
)

type failureFacts struct{ classification, terminalReason, provenance, outputState, errorType, code, failingEvent string } // Deprecated: forwarding compatibility only.
type observer = sessionProgressObserver
type f = failureFacts

func ff(v sf.Facts) *f {
	if v.FailingEvent == "" {
		return nil
	}
	return &f{v.Classification, v.TerminalReason, v.Provenance, v.OutputState, v.ErrorType, v.Code, v.FailingEvent}
}
func pf(f *f) sf.Facts {
	if f == nil {
		return sf.Facts{}
	}
	return sf.Facts{Classification: f.classification, TerminalReason: f.terminalReason, Provenance: f.provenance, OutputState: f.outputState, ErrorType: f.errorType, Code: f.code, FailingEvent: f.failingEvent}
}
func progress(o *observer) sf.Progress { return sfw.Progress(o.sawSessionOpen, o.turnsCompleted) }
func (o *observer) failureSnapshot() *f {
	if o == nil {
		return nil
	}
	if observation := fi(o).Failure.Snapshot(); observation != nil {
		return ff(observation.Facts)
	}
	o.livenessMu.Lock()
	f := o.failure
	o.livenessMu.Unlock()
	return f
}
func (o *observer) clearFailure()                           { fi(o).Clear() }
func (o *observer) captureFailureFromError(v *m.ErrorValue) { fi(o).AcceptError(v) }
func factsFromSessionRunError(err error) *f                 { return ff(sfw.RunFacts(err)) }
func (o *observer) acceptFailureObservation(f *f, err error) bool {
	return fi(o).Accept(pf(f), err)
}
func (o *observer) captureFailureFromClose(v *m.SessionCloseValue) {
	if o == nil {
		return
	}
	fi(o).AcceptClose(v, progress(o))
}
func p(o *observer, k int, e string) *f {
	if o == nil {
		return nil
	}
	return ff(sfw.Facts(k, e, progress(o)))
}
func (o *observer) unresolvedToolResultFailureFacts(e string) *f { return p(o, 0, e) }
func (o *observer) imageContinuationFailureFacts(e string) *f    { return p(o, 1, e) }
func (o *observer) toolContinuationFailureFacts(e string) *f     { return p(o, 2, e) }
func (o *observer) scheduledAudioFailureFacts(e string) *f       { return p(o, 3, e) }
func (o *observer) emitToolCallRecord(v *m.ToolCallEndValue) {
	if o == nil || o.sink == nil || v == nil {
		return
	}
	fi(o).Failure.EmitUnsupportedTool(sf.ToolCall{Name: v.Name, ID: v.ToolCallID, TurnIndex: o.turnsCompleted + 1})
}
func deriveOutputState(open bool, turns int) string { return sfw.OutputStateForProgress(open, turns) }
func fi(o *observer) *sfw.Invocation {
	if o == nil {
		return sfw.NewInvocation(nil, sf.Dependencies{}, nil, nil)
	}
	o.lifecycleProjectionMu.Lock()
	defer o.lifecycleProjectionMu.Unlock()
	if holder, ok := o.lifecycle.(*sfw.Invocation); ok {
		return holder
	}
	holder := sfw.NewInvocation(o.ensureLifecycle(), sf.Dependencies{Sink: sf.DiagnosticSinkFunc(func(r sf.DiagnosticRecord) {
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: r.Event, Fields: r.Fields})
	})}, func(ob sf.Observation) bool {
		f := ff(ob.Facts)
		setFailure(o, f)
		return o.notifyFailureObservation(sessionTerminalObservationFromFailure(f, ob.Err))
	}, func() { setFailure(o, nil) })
	o.lifecycle = holder
	return holder
}
func setFailure(o *observer, f *f) { o.livenessMu.Lock(); defer o.livenessMu.Unlock(); o.failure = f }
