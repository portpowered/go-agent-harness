package observer

import (
	"context"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// terminalRequest only translates already-observed values; terminal policy is
// owned by sessionterminal.
func (o *observerState) terminalRequest(runErr error) sessionterminal.Request {
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
	if len(i) > 0 && errors.Is(runErr, session.ErrLiveImageContinuationIncomplete) {
		hints = append(hints, sessionterminal.FailureHintImageContinuationIncomplete)
	}
	if len(t) > 0 && errors.Is(runErr, session.ErrLiveToolContinuationIncomplete) {
		hints = append(hints, sessionterminal.FailureHintToolContinuationIncomplete)
	}
	incomplete := o.scheduledAudioIncomplete()
	if incomplete && errors.Is(runErr, session.ErrLiveScheduledAudioIncomplete) {
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

func terminalFailureFacts(o *observerState) *sessionterminal.FailureFacts {
	if o == nil {
		return nil
	}
	if f := o.failureSnapshot(); f != nil {
		return &sessionterminal.FailureFacts{Classification: f.classification, TerminalReason: messages.TerminalReason(f.terminalReason), Provenance: messages.TerminalProvenance(f.provenance), OutputState: messages.TerminalOutputState(f.outputState), ProviderErrorType: f.errorType, ProviderErrorCode: f.code, FailingEvent: f.failingEvent}
	}
	return nil
}

func (o *observerState) record(result sessionterminal.Result, events ...string) {
	if o == nil || o.sink == nil {
		return
	}
	for _, record := range result.Records {
		for _, event := range events {
			if record.Event == event {
				o.sink.RecordSessionDiagnostic(sessiontrace.DiagnosticRecord{Event: record.Event, Fields: record.Fields})
				break
			}
		}
	}
}

func (o *observerState) emitTerminal(runErr error) {
	if o == nil || o.sink == nil || o.terminal == nil {
		return
	}
	o.emitOnce.Do(func() {
		o.record(o.terminal.Finalize(o.terminalRequest(runErr)), sessionterminal.EventFailure, sessionterminal.EventTerminal)
	})
}

func (o *observerState) userCancellationOutputState() messages.TerminalOutputState {
	if o == nil {
		return messages.TerminalOutputNone
	}
	if o.terminal == nil {
		return messages.TerminalOutputNone
	}
	return o.terminal.CancellationOutputState(o.terminalRequest(nil).Output)
}

func (o *observerState) finish(err error) error {
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
		o.runtime.TerminalWithAccounting(o.turnsCompleted, err, o.finalAccounting())
	}
	return err
}

func (o *observerState) finalAccounting() *SessionFinalAccounting {
	if o == nil {
		return nil
	}
	if o.terminal == nil {
		return nil
	}
	a := o.terminal.Finalize(o.terminalRequest(nil)).Accounting
	if a == nil {
		return nil
	}
	return &SessionFinalAccounting{PromptTokens: a.PromptTokens, CompletionTokens: a.CompletionTokens, TotalTokens: a.TotalTokens, ReasoningTokens: a.ReasoningTokens, UsageSemantics: SessionTokenUsageIncremental, Metrics: a.Metrics}
}

func (o *observerState) emitMetricsMatrix() {
	if o == nil || o.sink == nil || o.terminal == nil {
		return
	}
	o.metricsOnce.Do(func() { o.record(o.terminal.Finalize(o.terminalRequest(nil)), sessionterminal.EventMetrics) })
}

func (o *observerState) notifyFinalTerminalObservation(err error) {
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
	if o.notifyRunFailureObservation(err) {
		return
	}
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return
	}
	if roomCancellationOnly(err) {
		o.notifyTerminalObservation(sessionTerminalObservationForCancellation(o.userCancellationOutputState(), false))
	}
}

func (o *observerState) notifyRunFailureObservation(err error) bool {
	if err == nil || roomCancellationOnly(err) {
		return false
	}
	facts := factsFromSessionRunError(err)
	if facts == nil {
		classification := providers.ErrorClassification(err)
		if classification == "" {
			classification = providers.ErrorClassUnknown
		}
		facts = &failureFacts{classification: classification, terminalReason: string(messages.TerminalReasonTerminalFailure), provenance: string(messages.TerminalProvenanceCLI), outputState: deriveOutputState(o.sawSessionOpen, o.turnsCompleted), failingEvent: failingEventRun}
	}
	o.acceptFailureObservation(facts, err)
	return true
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
	return sessionTerminalObservation{Classification: facts.classification, TerminalReason: messages.TerminalReason(facts.terminalReason), TerminalProvenance: messages.TerminalProvenance(facts.provenance), OutputState: messages.TerminalOutputState(facts.outputState), Err: err, Failure: true, Code: facts.code, FailingEvent: facts.failingEvent}
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
	return sessionTerminalObservation{ResponseID: responseID, TerminalReason: reason, TerminalProvenance: provenance, OutputState: outputState}
}

func sessionTerminalObservationForCancellation(outputState messages.TerminalOutputState, roomBound bool) sessionTerminalObservation {
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	classification := providers.ErrorClassCancellation
	provenance := messages.TerminalProvenanceCLI
	if roomBound {
		classification = "room_bound_cancelled"
		provenance = messages.TerminalProvenanceRoom
	}
	return sessionTerminalObservation{Classification: classification, TerminalReason: messages.TerminalReasonCancellation, TerminalProvenance: provenance, OutputState: outputState, RoomBound: roomBound}
}

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
