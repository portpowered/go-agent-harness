package service

import (
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
)

func (r *reporter) Publish(out io.Writer, runErr error) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.consumed {
		r.mu.Unlock()
		return terminaloutcome.ErrSessionTerminalAlreadyPublished
	}
	r.consumed = true
	if !r.outcome.runStarted {
		r.mu.Unlock()
		return nil
	}
	if hasIndependentFailure(runErr) {
		r.markRunFailure()
	}
	selected, replayDone := r.reconcileLocked(runErr)
	r.mu.Unlock()
	if selected == nil {
		return nil
	}
	return writePublishedTerminal(writeDiscardIfNil(out), selected, replayDone)
}

func (r *reporter) reconcileLocked(runErr error) (*candidate, bool) {
	switch {
	case r.outcome.fatalError != nil:
		return r.reconcileFatalLocked()
	case r.outcome.replayComplete:
		return r.reconcileReplayLocked()
	case r.outcome.cancellation != nil:
		return r.reconcileCancellationLocked()
	case r.outcome.observedTerminal != nil:
		return r.reconcileObservedLocked()
	case r.outcome.durationTerminal != nil || r.outcome.durationExpired:
		return r.reconcileDurationLocked()
	case sessionErrorIsCancellation(runErr):
		return r.reconcileRunCancellationLocked()
	default:
		return r.reconcileFallbackLocked()
	}
}

func (r *reporter) reconcileFatalLocked() (*candidate, bool) {
	o := &r.outcome
	selected := o.failure
	if selected == nil {
		selected = &candidate{
			value: messages.NewSessionCloseValueWithTerminal(
				"", string(messages.TerminalReasonTerminalFailure), string(messages.TerminalReasonTerminalFailure),
				messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceSession, outputStateOrNone(o),
			),
			leadingNewline: true,
		}
	}
	selected.value = normalizeTerminalValue(selected.value, messages.TerminalReasonTerminalFailure, outputStateOrNone(o))
	o.cause = selected.value.TerminalReason
	o.completion = completionIncomplete
	return selected, false
}

func (r *reporter) reconcileReplayLocked() (*candidate, bool) {
	r.outcome.cause = messages.TerminalReasonReplayComplete
	r.outcome.outputState = messages.TerminalOutputComplete
	r.outcome.completion = completionComplete
	return &candidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"", "", replayComplete, messages.TerminalReasonReplayComplete,
			messages.TerminalProvenanceReplay, messages.TerminalOutputComplete,
		),
		leadingNewline: true,
	}, true
}

func (r *reporter) reconcileCancellationLocked() (*candidate, bool) {
	selected := r.outcome.cancellation
	selected.value = normalizeTerminalValue(selected.value, messages.TerminalReasonCancellation, outputStateOrNone(&r.outcome))
	r.outcome.cause = messages.TerminalReasonCancellation
	r.outcome.completion = completionIncomplete
	return selected, false
}

func (r *reporter) reconcileObservedLocked() (*candidate, bool) {
	selected := r.outcome.observedTerminal
	selected.value = normalizeTerminalValue(selected.value, terminalReasonOrDefault(selected.value, messages.TerminalReasonSessionClose), outputStateOrNone(&r.outcome))
	r.outcome.cause = selected.value.TerminalReason
	if successfulReason(r.outcome.cause) {
		r.outcome.completion = completionComplete
	} else {
		r.outcome.completion = completionIncomplete
	}
	return selected, false
}

func (r *reporter) reconcileDurationLocked() (*candidate, bool) {
	o := &r.outcome
	selected := o.durationTerminal
	if selected == nil {
		selected = &candidate{
			value: messages.NewSessionCloseValueWithTerminal(
				"", string(maxDurationReason), string(maxDurationReason), maxDurationReason,
				messages.TerminalProvenanceLoop, outputStateOrNone(o),
			),
			leadingNewline: true,
		}
	}
	selected.value = normalizeTerminalValue(selected.value, maxDurationReason, outputStateOrNone(o))
	o.cause = maxDurationReason
	o.completion = completionIncomplete
	return selected, false
}

func (r *reporter) reconcileRunCancellationLocked() (*candidate, bool) {
	r.outcome.cause = messages.TerminalReasonCancellation
	r.outcome.completion = completionIncomplete
	return &candidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"", "", string(messages.TerminalReasonCancellation), messages.TerminalReasonCancellation,
			messages.TerminalProvenanceSession, outputStateOrNone(&r.outcome),
		),
		leadingNewline: true,
	}, false
}

func (r *reporter) reconcileFallbackLocked() (*candidate, bool) {
	r.outcome.cause = messages.TerminalReasonSessionClose
	r.outcome.completion = completionIncomplete
	return &candidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"", string(messages.TerminalReasonSessionClose), string(messages.TerminalReasonSessionClose),
			messages.TerminalReasonSessionClose, messages.TerminalProvenanceSession, outputStateOrNone(&r.outcome),
		),
		leadingNewline: true,
	}, false
}

func terminalReasonOrDefault(value *messages.SessionCloseValue, fallback messages.TerminalReason) messages.TerminalReason {
	if value == nil || value.TerminalReason == "" {
		return fallback
	}
	return value.TerminalReason
}

func successfulReason(reason messages.TerminalReason) bool {
	return reason == messages.TerminalReasonProviderAuthoredCompletion ||
		reason == messages.TerminalReasonLoopSynthesizedCompletion ||
		reason == messages.TerminalReasonReplayComplete
}

func normalizeTerminalValue(value *messages.SessionCloseValue, fallback messages.TerminalReason, outputState messages.TerminalOutputState) *messages.SessionCloseValue {
	if value == nil {
		value = &messages.SessionCloseValue{}
	}
	value = cloneSessionCloseValue(value)
	if value.TerminalReason == "" {
		value.TerminalReason = boundReason(fallback)
	}
	if value.Classification == "" {
		if value.TerminalReason == messages.TerminalReasonProviderClose {
			value.Classification = "transport"
		} else {
			value.Classification = string(value.TerminalReason)
		}
	}
	if value.TerminalProvenance == "" {
		value.TerminalProvenance = messages.TerminalProvenanceSession
	}
	if value.OutputState == "" {
		value.OutputState = boundOutputState(outputState)
	}
	if value.OutputState == "" {
		value.OutputState = messages.TerminalOutputNone
	}
	value.Reason = boundTerminalText(value.Reason)
	value.Classification = boundTerminalText(value.Classification)
	return value
}
