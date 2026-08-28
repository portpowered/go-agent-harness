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

// emitTerminal emits at most one canonical failure record per session run. A
// captured stream failure always wins; otherwise a non-cancellation run error
// becomes a synthesized connect/run-phase failure. Clean runs emit nothing.
func (o *sessionProgressObserver) emitTerminal(runErr error) {
	if o == nil || o.sink == nil {
		return
	}
	o.emitOnce.Do(func() {
		completedScheduled, dispatchedInputs, scheduledInputs := o.scheduledAudioCounts()
		scheduleIncomplete := o.scheduledAudioIncomplete()
		unresolvedIDs := o.unresolvedToolCallIDs()
		pendingContinuationIDs := o.pendingToolContinuationCallIDs()
		pendingToolContinuationIDs := o.pendingNonImageToolContinuationCallIDs()
		pendingImageContinuationIDs := o.pendingImageContinuationCallIDs()
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
		if f.code != "" {
			fields[fieldProviderErrorCode] = f.code
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
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventFailure,
			Fields: fields,
		})
	})
}
