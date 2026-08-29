package services

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func (o *sessionProgressObserver) emitTerminal(runErr error) {
	if o == nil || o.sink == nil {
		return
	}
	o.emitOnce.Do(func() {
		if o.userCancelled {
			o.emitUserCancelledTerminal()
			return
		}
		completedScheduled, dispatchedInputs, scheduledInputs := o.scheduledAudioCounts()
		scheduleIncomplete := o.scheduledAudioIncomplete()
		unresolvedIDs := o.unresolvedToolCallIDs()
		pendingContinuationIDs := o.pendingToolContinuationCallIDs()
		pendingToolContinuationIDs := o.pendingNonImageToolContinuationCallIDs()
		pendingImageContinuationIDs := o.pendingImageContinuationCallIDs()
		continuationStatuses, continuationCodes, continuationDetails := o.pendingContinuationMetadata()
		_, scheduledCode, _ := o.scheduledAudioFailureMetadata()
		f := o.failure
		if f == nil && len(unresolvedIDs) == 0 && len(pendingContinuationIDs) == 0 && !scheduleIncomplete {
			if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				return
			}
		}
		if f == nil && len(unresolvedIDs) > 0 && errors.Is(runErr, ErrSessionUnresolvedToolResults) {
			f = o.unresolvedToolResultFailureFacts(failingEventRun)
		}
		if f == nil && len(pendingImageContinuationIDs) > 0 && errors.Is(runErr, ErrSessionImageContinuationIncomplete) {
			f = o.imageContinuationFailureFacts(failingEventRun)
		}
		if f == nil && len(pendingToolContinuationIDs) > 0 && errors.Is(runErr, ErrSessionToolContinuationIncomplete) {
			f = o.toolContinuationFailureFacts(failingEventRun)
		}
		if f == nil && scheduleIncomplete && errors.Is(runErr, ErrSessionScheduledAudioIncomplete) {
			f = o.scheduledAudioFailureFacts(failingEventRun)
		}
		if f == nil && runErr != nil {
			var deltaErr *engine.StreamDeltaError
			if errors.As(runErr, &deltaErr) && deltaErr.Value != nil {
				// The engine terminates the hot loop on ERROR deltas and the
				// typed value may never cross the consumer deltas; recover the
				// canonical facts from the run error itself.
				f = factsFromErrorValue(deltaErr.Value)
			}
		}
		if f == nil {
			if len(unresolvedIDs) > 0 {
				f = o.unresolvedToolResultFailureFacts(failingEventRun)
			} else if len(pendingToolContinuationIDs) > 0 {
				f = o.toolContinuationFailureFacts(failingEventRun)
			} else if len(pendingImageContinuationIDs) > 0 {
				f = o.imageContinuationFailureFacts(failingEventRun)
			} else {
				classification := providers.ErrorClassification(runErr)
				if classification == "" {
					classification = providers.ErrorClassUnknown
				}
				failingEvent := failingEventRun
				if !o.sawSessionOpen {
					failingEvent = failingEventConnect
				}
				f = &failureFacts{
					classification: classification,
					terminalReason: string(messages.TerminalReasonTerminalFailure),
					provenance:     string(messages.TerminalProvenanceCLI),
					outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
					failingEvent:   failingEvent,
				}
			}
		}
		fields := map[string]string{
			fieldClassification:     f.classification,
			fieldTerminalReason:     f.terminalReason,
			fieldTerminalProvenance: f.provenance,
			fieldOutputState:        f.outputState,
			fieldProvider:           o.provider,
			fieldModel:              o.model,
			fieldTurnsCompleted:     strconv.Itoa(o.turnsCompleted),
			fieldFailingEvent:       f.failingEvent,
		}
		if f.errorType != "" {
			fields[fieldProviderErrorType] = f.errorType
		}
		providerErrorCode := f.code
		if providerErrorCode == "" {
			providerErrorCode = scheduledCode
		}
		if providerErrorCode == "" {
			for _, id := range pendingContinuationIDs {
				if code := continuationCodes[id]; code != "" {
					providerErrorCode = code
					break
				}
			}
		}
		if providerErrorCode != "" {
			fields[fieldProviderErrorCode] = providerErrorCode
		}
		if len(unresolvedIDs) > 0 {
			fields[fieldUnresolvedToolResultCount] = strconv.Itoa(len(unresolvedIDs))
			fields[fieldUnresolvedToolCallIDs] = strings.Join(unresolvedIDs, ", ")
		}
		if len(pendingToolContinuationIDs) > 0 {
			fields[SessionDiagnosticFieldPendingToolContinuationCount] = strconv.Itoa(len(pendingToolContinuationIDs))
			fields[SessionDiagnosticFieldPendingToolContinuationIDs] = strings.Join(pendingToolContinuationIDs, ", ")
		}
		if len(pendingImageContinuationIDs) > 0 {
			fields[SessionDiagnosticFieldPendingImageContinuationCount] = strconv.Itoa(len(pendingImageContinuationIDs))
			fields[SessionDiagnosticFieldPendingImageContinuationIDs] = strings.Join(pendingImageContinuationIDs, ", ")
		}
		if scheduledInputs > 0 {
			fields[SessionDiagnosticFieldScheduledInputCount] = strconv.Itoa(scheduledInputs)
			fields[SessionDiagnosticFieldDispatchedInputCount] = strconv.Itoa(dispatchedInputs)
			fields[SessionDiagnosticFieldCompletedTurnCount] = strconv.Itoa(completedScheduled)
		}
		if formatted := formatContinuationMetadata(continuationStatuses); formatted != "" {
			fields[SessionDiagnosticFieldPendingContinuationStatuses] = formatted
		}
		if formatted := formatContinuationMetadata(continuationCodes); formatted != "" {
			fields[SessionDiagnosticFieldPendingContinuationCodes] = formatted
		}
		if formatted := formatContinuationMetadata(continuationDetails); formatted != "" {
			fields[SessionDiagnosticFieldPendingContinuationDetails] = formatted
		}
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventFailure,
			Fields: fields,
		})
	})
}

func (o *sessionProgressObserver) emitUserCancelledTerminal() {
	if o == nil || o.sink == nil {
		return
	}
	completedScheduled, dispatchedInputs, scheduledInputs := o.scheduledAudioCounts()
	unresolvedIDs := o.unresolvedToolCallIDs()
	pendingContinuationIDs := o.pendingToolContinuationCallIDs()
	cancelledScheduled := scheduledInputs - completedScheduled
	if cancelledScheduled < 0 {
		cancelledScheduled = 0
	}
	fields := map[string]string{
		fieldClassification:               SessionUserCancelledClassification,
		fieldTerminalReason:               string(messages.TerminalReasonCancellation),
		fieldTerminalProvenance:           string(messages.TerminalProvenanceCLI),
		fieldOutputState:                  string(o.userCancellationOutputState()),
		fieldProvider:                     o.provider,
		fieldModel:                        o.model,
		fieldTurnsCompleted:               strconv.Itoa(o.turnsCompleted),
		SessionDiagnosticFieldCancelledBy: "user",
	}
	if scheduledInputs > 0 {
		fields[SessionDiagnosticFieldScheduledInputCount] = strconv.Itoa(scheduledInputs)
		fields[SessionDiagnosticFieldDispatchedInputCount] = strconv.Itoa(dispatchedInputs)
		fields[SessionDiagnosticFieldCompletedTurnCount] = strconv.Itoa(completedScheduled)
		fields[SessionDiagnosticFieldCancelledScheduledInputCount] = strconv.Itoa(cancelledScheduled)
	}
	if len(unresolvedIDs) > 0 {
		fields[SessionDiagnosticFieldCancelledToolResultCount] = strconv.Itoa(len(unresolvedIDs))
		fields[SessionDiagnosticFieldCancelledToolResultCallIDs] = strings.Join(unresolvedIDs, ", ")
	}
	if len(pendingContinuationIDs) > 0 {
		fields[SessionDiagnosticFieldCancelledToolContinuationCount] = strconv.Itoa(len(pendingContinuationIDs))
		fields[SessionDiagnosticFieldCancelledToolContinuationCallIDs] = strings.Join(pendingContinuationIDs, ", ")
	}
	o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventTerminal,
		Fields: fields,
	})
}

func (o *sessionProgressObserver) userCancellationOutputState() messages.TerminalOutputState {
	if o == nil {
		return messages.TerminalOutputNone
	}
	if o.turnsCompleted > 0 || o.totals.outAudio > 0 || o.totals.outText > 0 || o.responseOutputAudioBytes > 0 || o.responseOutputTextBytes > 0 || o.assistantOutputObserved {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNone
}

func (o *sessionProgressObserver) finish(err error) error {
	if o == nil {
		return err
	}
	if sessionSIGINTCleanForObserver(err, o.cancellationIntent, o) {
		o.userCancelled = true
		o.failure = nil
		err = nil
	}
	if !o.userCancelled {
		err = withUnresolvedToolResults(err, o)
		err = withPendingToolContinuations(err, o)
		err = withPendingImageContinuations(err, o)
	}
	o.emitTerminal(err)
	o.emitMetricsMatrix()
	if o.runtime != nil {
		o.runtime.terminalWithAccounting(o.turnsCompleted, err, o.finalAccounting())
	}
	return err
}

// finalAccounting captures the runtime-owned production counters only after
// the session consumer has drained every accounted delta. The optional
// MetricsRecorder and SessionStreamObserver are deliberately not consulted.
func (o *sessionProgressObserver) finalAccounting() *SessionFinalAccounting {
	if o == nil {
		return nil
	}
	accounting := &SessionFinalAccounting{
		PromptTokens:     o.usagePrompt,
		CompletionTokens: o.usageCompletion,
		TotalTokens:      o.usageTotal,
		ReasoningTokens:  o.usageReasoning,
		UsageSemantics:   SessionTokenUsageIncremental,
	}
	if o.productionSink != nil {
		accounting.Metrics = o.productionSink.Snapshot()
	}
	return accounting
}

// emitMetricsMatrix emits the terminal per-direction/per-modality byte matrix
// exactly once per run, after every delta it summarizes has crossed. The
// provider-reported message-end token usage rides alongside so operators can
// compare both accounting sources; byte counts and token counts measure
// different units and are not expected to be numerically equal.
func (o *sessionProgressObserver) emitMetricsMatrix() {
	if o == nil || o.sink == nil {
		return
	}
	o.metricsOnce.Do(func() {
		fields := map[string]string{
			fieldProvider:         o.provider,
			fieldModel:            o.model,
			fieldTurnsCompleted:   strconv.Itoa(o.turnsCompleted),
			fieldInputAudioBytes:  strconv.FormatUint(o.totals.inputAudio, 10),
			fieldInputTextBytes:   strconv.FormatUint(o.totals.inputText, 10),
			fieldOutputAudioBytes: strconv.FormatUint(o.totals.outAudio, 10),
			fieldOutputTextBytes:  strconv.FormatUint(o.totals.outText, 10),
			fieldOutputToolBytes:  strconv.FormatUint(o.totals.outTool, 10),
		}
		if o.usageSeen {
			fields[fieldProviderPromptTokens] = strconv.FormatUint(o.usagePrompt, 10)
			fields[fieldProviderCompletionTokens] = strconv.FormatUint(o.usageCompletion, 10)
			fields[fieldProviderTotalTokens] = strconv.FormatUint(o.usageTotal, 10)
			fields[fieldProviderReasoningTokens] = strconv.FormatUint(o.usageReasoning, 10)
		}
		if o.scheduledInputs > 0 {
			fields[SessionDiagnosticFieldScheduledInputCount] = strconv.Itoa(o.scheduledInputs)
			fields[SessionDiagnosticFieldDispatchedInputCount] = strconv.Itoa(o.dispatchedInputs)
			fields[SessionDiagnosticFieldCompletedTurnCount] = strconv.Itoa(o.completedScheduled)
		}
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventMetrics,
			Fields: fields,
		})
	})
}
