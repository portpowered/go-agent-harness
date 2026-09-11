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
		r.rememberFatalError(runErr)
	}
	candidate, replayDone := r.reconcileLocked(runErr)
	r.mu.Unlock()
	if candidate == nil {
		return nil
	}
	return writePublishedTerminal(writeDiscardIfNil(out), candidate, replayDone)
}

func (r *reporter) reconcileLocked(runErr error) (*candidate, bool) {
	o := &r.outcome
	if o.fatalError != nil {
		selected := o.failure
		if selected == nil {
			selected = &candidate{
				value: messages.NewSessionCloseValueWithTerminal(
					"",
					string(messages.TerminalReasonTerminalFailure),
					string(messages.TerminalReasonTerminalFailure),
					messages.TerminalReasonTerminalFailure,
					messages.TerminalProvenanceSession,
					outputStateOrNone(o),
				),
				leadingNewline: true,
			}
		}
		selected.value = normalizeTerminalValue(selected.value, messages.TerminalReasonTerminalFailure, outputStateOrNone(o))
		o.cause = selected.value.TerminalReason
		o.completion = completionIncomplete
		return selected, false
	}
	if o.replayComplete {
		selected := &candidate{
			value: messages.NewSessionCloseValueWithTerminal(
				"",
				"",
				replayComplete,
				messages.TerminalReasonReplayComplete,
				messages.TerminalProvenanceReplay,
				messages.TerminalOutputComplete,
			),
			leadingNewline: true,
		}
		o.cause = messages.TerminalReasonReplayComplete
		o.outputState = messages.TerminalOutputComplete
		o.completion = completionComplete
		return selected, true
	}
	if o.cancellation != nil {
		selected := o.cancellation
		selected.value = normalizeTerminalValue(selected.value, messages.TerminalReasonCancellation, outputStateOrNone(o))
		o.cause = messages.TerminalReasonCancellation
		o.completion = completionIncomplete
		return selected, false
	}
	if o.observedTerminal != nil {
		selected := o.observedTerminal
		selected.value = normalizeTerminalValue(selected.value, terminalReasonOrDefault(selected.value, messages.TerminalReasonSessionClose), outputStateOrNone(o))
		o.cause = selected.value.TerminalReason
		if successfulReason(o.cause) {
			o.completion = completionComplete
		} else {
			o.completion = completionIncomplete
		}
		return selected, false
	}
	if o.durationTerminal != nil || o.durationExpired {
		selected := o.durationTerminal
		if selected == nil {
			selected = &candidate{
				value: messages.NewSessionCloseValueWithTerminal(
					"",
					string(maxDurationReason),
					string(maxDurationReason),
					maxDurationReason,
					messages.TerminalProvenanceLoop,
					outputStateOrNone(o),
				),
				leadingNewline: true,
			}
		}
		selected.value = normalizeTerminalValue(selected.value, maxDurationReason, outputStateOrNone(o))
		o.cause = maxDurationReason
		o.completion = completionIncomplete
		return selected, false
	}
	if sessionErrorIsCancellation(runErr) {
		selected := &candidate{
			value: messages.NewSessionCloseValueWithTerminal(
				"",
				"",
				string(messages.TerminalReasonCancellation),
				messages.TerminalReasonCancellation,
				messages.TerminalProvenanceSession,
				outputStateOrNone(o),
			),
			leadingNewline: true,
		}
		o.cause = messages.TerminalReasonCancellation
		o.completion = completionIncomplete
		return selected, false
	}
	selected := &candidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(messages.TerminalReasonSessionClose),
			string(messages.TerminalReasonSessionClose),
			messages.TerminalReasonSessionClose,
			messages.TerminalProvenanceSession,
			outputStateOrNone(o),
		),
		leadingNewline: true,
	}
	o.cause = messages.TerminalReasonSessionClose
	o.completion = completionIncomplete
	return selected, false
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
