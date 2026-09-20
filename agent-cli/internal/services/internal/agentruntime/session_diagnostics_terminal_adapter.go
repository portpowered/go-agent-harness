package agentruntime

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessionduration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	sessiondurationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	SessionSilentProviderEmptyResponseClassification = "silent_provider_empty_response"
	SessionSilentProviderTimeoutClassification       = "silent_provider_timeout"
	sessionProviderLivenessTimeout                   = 10 * time.Second
)

const (
	ErrSilentProviderEmptyResponse = sessionduration.ErrProviderEmptyResponse
	ErrSilentProviderTimeout       = sessionduration.ErrProviderLivenessTimeout
)

func sessionLivenessClockFromSource(source platformclock.Source) sessionduration.TimerScheduler {
	source = platformclock.Ensure(source)
	timerSource, ok := source.(platformclock.TimerSource)
	if !ok {
		return nil
	}
	return timerSource
}

func sessionLivenessMetadata(err error) (classification string, terminalReason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) {
	var livenessErr *sessionduration.LivenessError
	if !errors.As(err, &livenessErr) || livenessErr == nil {
		return "", "", "", ""
	}
	return livenessErr.Classification, livenessErr.TerminalReason, livenessErr.TerminalProvenance, livenessErr.OutputState
}

func (o *sessionProgressObserver) ensureLivenessController(clock sessionduration.TimerScheduler) {
	if o == nil || o.livenessOwnedByDurationService || o.livenessController != nil || clock == nil {
		return
	}
	controller, err := sessiondurationwire.NewService().Begin(sessionduration.Options{
		Context: context.Background(),
		Clock:   clock,
		Liveness: sessionduration.LivenessOptions{
			Enabled: true,
			Timeout: sessionProviderLivenessTimeout,
		},
		FirstCause: o.recordLivenessFailure,
	})
	if err == nil {
		o.livenessController = controller
	}
}

func (o *sessionProgressObserver) recordLivenessFailure(err error) {
	if o == nil || err == nil {
		return
	}
	classification, reason, provenance, output := sessionLivenessMetadata(err)
	facts := &failureFacts{
		classification: classification,
		terminalReason: string(reason),
		provenance:     string(provenance),
		outputState:    string(output),
		failingEvent:   failingEventRun,
	}
	if classification == SessionSilentProviderEmptyResponseClassification {
		facts.failingEvent = string(messages.StreamTypeMessageEnd)
	}
	o.failureMu.Lock()
	if o.failure == nil {
		o.failure = facts
	}
	callback := o.livenessObserver
	o.failureMu.Unlock()
	if callback != nil {
		callback(err)
	}
	select {
	case o.livenessErrors <- err:
	default:
	}
}

func (o *sessionProgressObserver) livenessFailure() error {
	if o == nil {
		return nil
	}
	if o.durationController != nil {
		return o.durationController.LivenessFailure()
	}
	if o.livenessController == nil {
		return nil
	}
	return o.livenessController.LivenessFailure()
}

func (o *sessionProgressObserver) setDurationController(controller sessionduration.Controller) {
	if o == nil {
		return
	}
	o.durationController = controller
	o.livenessOwnedByDurationService = controller != nil
}

func (o *sessionProgressObserver) setLivenessClock(clock sessionduration.TimerScheduler) {
	if o == nil {
		return
	}
	if clock == nil {
		clock = platformclock.Real{}
	}
	o.ensureLivenessController(clock)
}

func (o *sessionProgressObserver) armProviderProgress() {
	if o == nil {
		return
	}
	o.ensureLivenessController(platformclock.Real{})
	if o.livenessController != nil {
		o.livenessController.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	}
}

func (o *sessionProgressObserver) beginLocalToolExecution() {
	if o == nil {
		return
	}
	if o.durationController != nil {
		o.durationController.BeginLocalToolExecution()
		return
	}
	o.ensureLivenessController(platformclock.Real{})
	if o.livenessController != nil {
		o.livenessController.BeginLocalToolExecution()
	}
}

func (o *sessionProgressObserver) endLocalToolExecution() {
	if o != nil && o.durationController != nil {
		o.durationController.EndLocalToolExecution()
		return
	}
	if o != nil && o.livenessController != nil {
		o.livenessController.EndLocalToolExecution()
	}
}

func (o *sessionProgressObserver) stopLiveness() {
	if o == nil || o.durationController != nil || o.livenessController == nil {
		return
	}
	if _, err := o.livenessController.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		o.recordLivenessFailure(err)
	}
}

func (o *sessionProgressObserver) observeProviderEvent(msg messages.StreamMessage) {
	if o == nil || msg.Role == messages.RoleTool {
		return
	}
	o.ensureLivenessController(platformclock.Real{})
	if o.livenessController != nil {
		if msg.Type == messages.StreamTypeMessageEnd {
			o.livenessController.SetToolObligation(o.responseHasToolLifecycleObligation())
		}
		o.livenessController.Observe(msg)
	}
}

func (o *sessionProgressObserver) observeProviderDispatch(msg messages.StreamMessage) {
	if o == nil || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return
	}
	o.ensureLivenessController(platformclock.Real{})
	if o.livenessController != nil {
		o.livenessController.Observe(msg)
	}
}

func applyRoomParticipantTerminalMetadata(result *RoomParticipantResult, lifecycle *roomParticipantLifecycle, err error) {
	if result == nil {
		return
	}
	classification, terminalReason, provenance, outputState := "", messages.TerminalReason(""), messages.TerminalProvenance(""), messages.TerminalOutputState("")
	if lifecycle != nil {
		classification, terminalReason, provenance, outputState = lifecycle.terminalMetadata()
	}
	if classification == "" {
		classification, terminalReason, provenance, outputState = sessionLivenessMetadata(err)
	}
	if classification == "" {
		return
	}
	result.Classification = classification
	result.TerminalReason = string(terminalReason)
	result.TerminalProvenance = string(provenance)
	result.OutputState = string(outputState)
}

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
	if observerCancellationIsClean(err, o.cancellationIntent, o) {
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
