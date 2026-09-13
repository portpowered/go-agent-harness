package agentruntime

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
)

func terminalService() sessionterminal.Service { return terminalwire.NewService() }

// terminalRequest only translates already-observed values; terminal policy is
// owned by sessionterminal.
func (o *sessionProgressObserver) terminalRequest(runErr error) sessionterminal.Request {
	if o == nil {
		return sessionterminal.Request{RunError: runErr}
	}
	c, d, n := o.scheduledAudioCounts()
	u := o.unresolvedToolCallIDs()
	p := o.pendingToolContinuationCallIDs()
	t := o.pendingNonImageToolContinuationCallIDs()
	i := o.pendingImageContinuationCallIDs()
	s, codes, details := o.pendingContinuationMetadata()
	_, scheduledCode, scheduledDetails := o.scheduledAudioFailureMetadata()
	var hints []string
	if len(u) > 0 && errors.Is(runErr, ErrSessionUnresolvedToolResults) {
		hints = append(hints, sessionterminal.FailureHintUnresolvedToolResults)
	}
	if len(i) > 0 && errors.Is(runErr, ErrSessionImageContinuationIncomplete) {
		hints = append(hints, sessionterminal.FailureHintImageContinuationIncomplete)
	}
	if len(t) > 0 && errors.Is(runErr, ErrSessionToolContinuationIncomplete) {
		hints = append(hints, sessionterminal.FailureHintToolContinuationIncomplete)
	}
	incomplete := o.scheduledAudioIncomplete()
	if incomplete && errors.Is(runErr, ErrSessionScheduledAudioIncomplete) {
		hints = append(hints, sessionterminal.FailureHintScheduledAudioIncomplete)
	}
	r := sessionterminal.Request{
		RunError: runErr, UserCancelled: o.userCancelled, RoomBoundCancellation: o.roomBoundCancellation, RoomCancellationOnly: roomCancellationOnly(runErr), Provider: o.provider, Model: o.model, TurnsCompleted: o.turnsCompleted,
		Output:    sessionterminal.OutputSnapshot{SawSessionOpen: o.sawSessionOpen, TurnsCompleted: o.turnsCompleted, TotalOutputAudioBytes: o.totals.outAudio, TotalOutputTextBytes: o.totals.outText, ResponseOutputAudioBytes: o.responseOutputAudioBytes, ResponseOutputTextBytes: o.responseOutputTextBytes, AssistantOutputObserved: o.assistantOutputObserved},
		Bytes:     sessionterminal.ByteSnapshot{InputAudioBytes: o.totals.inputAudio + o.roomAudioInputTotalBytes(), InputTextBytes: o.totals.inputText, OutputAudioBytes: o.totals.outAudio, OutputTextBytes: o.totals.outText, OutputToolBytes: o.totals.outTool},
		Failure:   terminalFailureFacts(o),
		Lifecycle: sessionterminal.LifecycleSnapshot{UnresolvedToolResultCallIDs: u, PendingContinuationCallIDs: p, PendingToolContinuationIDs: t, PendingImageContinuationIDs: i, PendingContinuations: sessionterminal.ContinuationSnapshot{Statuses: s, Codes: codes, Details: details}, Scheduled: sessionterminal.ScheduledSnapshot{Completed: c, Dispatched: d, Inputs: n, Incomplete: incomplete, FailureCode: scheduledCode, FailureDetails: scheduledDetails}, FailureHints: hints},
		Usage:     sessionterminal.TokenSnapshot{PromptTokens: o.usagePrompt, CompletionTokens: o.usageCompletion, TotalTokens: o.usageTotal, ReasoningTokens: o.usageReasoning, Seen: o.usageSeen},
	}
	if o.productionSink != nil {
		r.Metrics = o.productionSink.Snapshot()
	}
	return r
}

func terminalFailureFacts(o *sessionProgressObserver) *sessionterminal.FailureFacts {
	if o == nil {
		return nil
	}
	if f := o.failureSnapshot(); f != nil {
		return &sessionterminal.FailureFacts{Classification: f.classification, TerminalReason: messages.TerminalReason(f.terminalReason), Provenance: messages.TerminalProvenance(f.provenance), OutputState: messages.TerminalOutputState(f.outputState), ProviderErrorType: f.errorType, ProviderErrorCode: f.code, FailingEvent: f.failingEvent}
	}
	return nil
}

func (o *sessionProgressObserver) record(result sessionterminal.Result, events ...string) {
	if o == nil || o.sink == nil {
		return
	}
	for _, record := range result.Records {
		for _, event := range events {
			if record.Event == event {
				o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: record.Event, Fields: record.Fields})
				break
			}
		}
	}
}

func (o *sessionProgressObserver) emitTerminal(runErr error) {
	if o == nil || o.sink == nil {
		return
	}
	o.emitOnce.Do(func() {
		o.record(terminalService().Finalize(o.terminalRequest(runErr)), sessionterminal.EventFailure, sessionterminal.EventTerminal)
	})
}

func (o *sessionProgressObserver) userCancellationOutputState() messages.TerminalOutputState {
	if o == nil {
		return messages.TerminalOutputNone
	}
	return terminalService().CancellationOutputState(o.terminalRequest(nil).Output)
}

func (o *sessionProgressObserver) finish(err error) error {
	if o == nil {
		return err
	}
	o.finishMu.Lock()
	defer o.finishMu.Unlock()
	if livenessErr := o.livenessFailure(); livenessErr != nil && !errors.Is(err, livenessErr) {
		err = errors.Join(livenessErr, err)
	}
	if sessionSIGINTCleanForObserver(err, o.cancellationIntent, o) {
		o.userCancelled = true
		o.clearFailure()
		err = nil
	}
	if o.roomBoundCancellation && o.failure == nil && roomCancellationOnly(err) {
		err = nil
	}
	if !o.userCancelled && !o.roomBoundCancellation {
		err = withUnresolvedToolResults(err, o)
		err = withPendingToolContinuations(err, o)
		err = withPendingImageContinuations(err, o)
	}
	o.notifyFinalTerminalObservation(err)
	o.emitTerminal(err)
	o.emitMetricsMatrix()
	if o.runtime != nil {
		o.runtime.terminalWithAccounting(o.turnsCompleted, err, o.finalAccounting())
	}
	return err
}

func (o *sessionProgressObserver) finalAccounting() *SessionFinalAccounting {
	if o == nil {
		return nil
	}
	a := terminalService().Finalize(o.terminalRequest(nil)).Accounting
	if a == nil {
		return nil
	}
	return &SessionFinalAccounting{PromptTokens: a.PromptTokens, CompletionTokens: a.CompletionTokens, TotalTokens: a.TotalTokens, ReasoningTokens: a.ReasoningTokens, UsageSemantics: SessionTokenUsageIncremental, Metrics: a.Metrics}
}

func (o *sessionProgressObserver) emitMetricsMatrix() {
	if o == nil || o.sink == nil {
		return
	}
	o.metricsOnce.Do(func() { o.record(terminalService().Finalize(o.terminalRequest(nil)), sessionterminal.EventMetrics) })
}
