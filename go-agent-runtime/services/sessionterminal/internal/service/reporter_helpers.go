package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func (r *terminalReporter) reconcileFailure(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := o.failure
	if candidate == nil {
		candidate = &sessionTerminalCandidate{value: messages.NewSessionCloseValueWithTerminal("", string(messages.TerminalReasonTerminalFailure), string(messages.TerminalReasonTerminalFailure), messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceSession, o.outputStateOrNone()), leadingNewline: true}
	}
	candidate.value = normalizeSessionTerminalValue(candidate.value, messages.TerminalReasonTerminalFailure, o.outputStateOrNone())
	o.cause, o.completion = candidate.value.TerminalReason, sessionTerminalCompletionIncomplete
	return candidate, false
}

func (r *terminalReporter) reconcileReplayComplete(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := &sessionTerminalCandidate{value: messages.NewSessionCloseValueWithTerminal("", "", SessionReplayCompleteClassification, messages.TerminalReasonReplayComplete, messages.TerminalProvenanceReplay, messages.TerminalOutputComplete), leadingNewline: true}
	o.cause, o.outputState, o.completion = messages.TerminalReasonReplayComplete, messages.TerminalOutputComplete, sessionTerminalCompletionComplete
	return candidate, true
}

func (r *terminalReporter) reconcileCancellation(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := o.cancellation
	candidate.value = normalizeSessionTerminalValue(candidate.value, messages.TerminalReasonCancellation, o.outputStateOrNone())
	o.cause, o.completion = messages.TerminalReasonCancellation, sessionTerminalCompletionIncomplete
	return candidate, false
}

func (r *terminalReporter) reconcileObserved(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := o.observedTerminal
	candidate.value = normalizeSessionTerminalValue(candidate.value, terminalReasonOrDefault(candidate.value, messages.TerminalReasonSessionClose), o.outputStateOrNone())
	o.cause = candidate.value.TerminalReason
	o.completion = sessionTerminalCompletionIncomplete
	if isSuccessfulTerminalReason(o.cause) {
		o.completion = sessionTerminalCompletionComplete
	}
	return candidate, false
}

func (r *terminalReporter) reconcileDuration(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := o.durationTerminal
	if candidate == nil {
		candidate = &sessionTerminalCandidate{value: messages.NewSessionCloseValueWithTerminal("", string(SessionMaxDurationReason), string(SessionMaxDurationReason), SessionMaxDurationReason, messages.TerminalProvenanceLoop, o.outputStateOrNone()), leadingNewline: true}
	}
	candidate.value = normalizeSessionTerminalValue(candidate.value, SessionMaxDurationReason, o.outputStateOrNone())
	o.cause, o.completion = SessionMaxDurationReason, sessionTerminalCompletionIncomplete
	return candidate, false
}

func (r *terminalReporter) reconcileRunCancellation(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := &sessionTerminalCandidate{value: messages.NewSessionCloseValueWithTerminal("", "", string(messages.TerminalReasonCancellation), messages.TerminalReasonCancellation, messages.TerminalProvenanceSession, o.outputStateOrNone()), leadingNewline: true}
	o.cause, o.completion = messages.TerminalReasonCancellation, sessionTerminalCompletionIncomplete
	return candidate, false
}

func (r *terminalReporter) reconcileDefault(o *sessionTerminalOutcome) (*sessionTerminalCandidate, bool) {
	candidate := &sessionTerminalCandidate{value: messages.NewSessionCloseValueWithTerminal("", string(messages.TerminalReasonSessionClose), string(messages.TerminalReasonSessionClose), messages.TerminalReasonSessionClose, messages.TerminalProvenanceSession, o.outputStateOrNone()), leadingNewline: true}
	o.cause, o.completion = messages.TerminalReasonSessionClose, sessionTerminalCompletionIncomplete
	return candidate, false
}

func (o *sessionTerminalOutcome) outputStateOrNone() messages.TerminalOutputState {
	if o == nil || o.outputState == "" {
		return messages.TerminalOutputNone
	}
	return o.outputState
}

func terminalReasonOrDefault(value *messages.SessionCloseValue, fallback messages.TerminalReason) messages.TerminalReason {
	if value == nil || value.TerminalReason == "" {
		return fallback
	}
	return value.TerminalReason
}

func isSuccessfulTerminalReason(reason messages.TerminalReason) bool {
	return reason == messages.TerminalReasonProviderAuthoredCompletion ||
		reason == messages.TerminalReasonLoopSynthesizedCompletion ||
		reason == messages.TerminalReasonReplayComplete
}

func normalizeSessionTerminalValue(value *messages.SessionCloseValue, fallback messages.TerminalReason, outputState messages.TerminalOutputState) *messages.SessionCloseValue {
	if value == nil {
		value = &messages.SessionCloseValue{}
	}
	value = cloneSessionCloseValue(value)
	if value.TerminalReason == "" {
		value.TerminalReason = fallback
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
		value.OutputState = outputState
	}
	if value.OutputState == "" {
		value.OutputState = messages.TerminalOutputNone
	}
	return value
}

func writePublishedSessionTerminal(out io.Writer, candidate *sessionTerminalCandidate, replayComplete bool) error {
	if candidate == nil || candidate.value == nil {
		return nil
	}
	value := candidate.value
	if value.Reason == "" && candidate.leadingNewline {
		if _, err := io.WriteString(out, "\n"); err != nil {
			return err
		}
		if err := writeSessionReplayClose(out, value, false); err != nil {
			return err
		}
	} else if err := writeSessionReplayClose(out, value, candidate.leadingNewline); err != nil {
		return err
	}
	if replayComplete {
		_, err := fmt.Fprintln(out, "[session replay complete]")
		return err
	}
	return nil
}

func sessionErrorHasIndependentFailure(err error) bool {
	if err == nil {
		return false
	}
	var leaves []error
	collectSessionErrorLeaves(err, &leaves)
	for _, leaf := range leaves {
		if leaf == nil || errors.Is(leaf, context.Canceled) || errors.Is(leaf, context.DeadlineExceeded) || errors.Is(leaf, sessionterminal.ErrDurationExpired) {
			continue
		}
		return true
	}
	return false
}

func sessionErrorIsCancellation(err error) bool {
	if err == nil || sessionErrorHasIndependentFailure(err) {
		return false
	}
	var leaves []error
	collectSessionErrorLeaves(err, &leaves)
	for _, leaf := range leaves {
		if errors.Is(leaf, context.Canceled) || errors.Is(leaf, context.DeadlineExceeded) {
			return true
		}
	}
	return false
}

func collectSessionErrorLeaves(err error, leaves *[]error) {
	if err == nil {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			collectSessionErrorLeaves(child, leaves)
		}
		return
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		collectSessionErrorLeaves(unwrapped, leaves)
		return
	}
	*leaves = append(*leaves, err)
}
