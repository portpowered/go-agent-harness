package service

import (
	"errors"
	"io"
	"sync"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
)

const (
	maxTerminalFieldBytes = 256
	maxFatalErrors        = 8

	maxDurationReason = messages.TerminalReason("max_duration")
	replayComplete    = "replay_complete"
)

type completionState uint8

const (
	completionPending completionState = iota
	completionComplete
	completionIncomplete
)

type artifactState uint8

const (
	artifactNotApplicable artifactState = iota
	artifactValid
	artifactInvalid
)

type candidate struct {
	value          *messages.SessionCloseValue
	leadingNewline bool
}

type outcome struct {
	cause         messages.TerminalReason
	completion    completionState
	outputState   messages.TerminalOutputState
	artifactState artifactState
	fatalError    error
	fatalErrors   int

	observedTerminal *candidate
	durationTerminal *candidate
	cancellation     *candidate
	failure          *candidate
	durationExpired  bool
	replayComplete   bool
	runStarted       bool
}

type reporter struct {
	mu       sync.Mutex
	outcome  outcome
	consumed bool
}

func newReporter() *reporter {
	return &reporter{outcome: outcome{
		artifactState: artifactNotApplicable,
		completion:    completionPending,
	}}
}

func (r *reporter) MarkRunStarted() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.runStarted = true
	r.mu.Unlock()
}

func (r *reporter) MarkDurationExpiry(outputState messages.TerminalOutputState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.durationExpired = true
	if outputState != "" {
		r.outcome.outputState = boundOutputState(outputState)
	}
	r.mu.Unlock()
}

func (r *reporter) MarkReplayComplete() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.outcome.replayComplete = true
	r.outcome.completion = completionComplete
	r.mu.Unlock()
}

func (r *reporter) RecordArtifactFinalization(requested bool, err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !requested {
		r.outcome.artifactState = artifactNotApplicable
		return
	}
	if err != nil {
		r.outcome.artifactState = artifactInvalid
		r.rememberFatalError(err)
		return
	}
	r.outcome.artifactState = artifactValid
}

func (r *reporter) rememberFatalError(err error) {
	if err == nil {
		return
	}
	r.outcome.fatalErrors++
	if r.outcome.fatalError == nil {
		r.outcome.fatalError = err
		return
	}
	if r.outcome.fatalErrors <= maxFatalErrors {
		r.outcome.fatalError = errors.Join(r.outcome.fatalError, err)
	}
}

func boundTerminalText(value string) string {
	if len(value) <= maxTerminalFieldBytes {
		return value
	}
	value = value[:maxTerminalFieldBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func boundReason(value messages.TerminalReason) messages.TerminalReason {
	return messages.TerminalReason(boundTerminalText(string(value)))
}

func boundProvenance(value messages.TerminalProvenance) messages.TerminalProvenance {
	return messages.TerminalProvenance(boundTerminalText(string(value)))
}

func boundOutputState(value messages.TerminalOutputState) messages.TerminalOutputState {
	return messages.TerminalOutputState(boundTerminalText(string(value)))
}

func cloneSessionCloseValue(value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Type = boundTerminalText(value.Type)
	clone.SessionID = boundTerminalText(value.SessionID)
	clone.Reason = boundTerminalText(value.Reason)
	clone.Classification = boundTerminalText(value.Classification)
	clone.TerminalReason = boundReason(value.TerminalReason)
	clone.TerminalProvenance = boundProvenance(value.TerminalProvenance)
	clone.OutputState = boundOutputState(value.OutputState)
	return &clone
}

func outputStateOrNone(o *outcome) messages.TerminalOutputState {
	if o == nil || o.outputState == "" {
		return messages.TerminalOutputNone
	}
	return boundOutputState(o.outputState)
}

func rememberCandidate(slot **candidate, value *candidate) {
	if *slot == nil {
		*slot = value
	}
}

func rememberObservedCandidate(slot **candidate, value *candidate) {
	if *slot == nil || (candidateIsSpecific(value) && !candidateIsSpecific(*slot)) {
		*slot = value
	}
}

func candidateIsSpecific(value *candidate) bool {
	if value == nil || value.value == nil {
		return false
	}
	return value.value.TerminalReason != "" && value.value.TerminalReason != messages.TerminalReasonSessionClose
}

func writeDiscardIfNil(out io.Writer) io.Writer {
	if out == nil {
		return io.Discard
	}
	return out
}

var _ terminaloutcome.Reporter = (*reporter)(nil)
