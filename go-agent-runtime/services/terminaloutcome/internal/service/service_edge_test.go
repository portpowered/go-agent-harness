package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
)

func TestErrorObservationNormalizesTerminalFieldsAndIgnoresNonTerminal(t *testing.T) {
	r := newReporter()
	r.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewNonTerminalErrorValue("informational", "notice"),
	}, true)
	if r.outcome.failure != nil || r.outcome.cancellation != nil {
		t.Fatal("non-terminal error changed terminal candidates")
	}
	r.ObserveStreamMessage(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")}, false)
	r.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValueWithTerminal("", "", "", "", ""),
	}, true)
	if r.outcome.failure == nil {
		t.Fatal("terminal error did not create a failure candidate")
	}
	value := r.outcome.failure.value
	if value.TerminalReason != messages.TerminalReasonTerminalFailure || value.Classification != string(messages.TerminalReasonTerminalFailure) ||
		value.TerminalProvenance != messages.TerminalProvenanceSession || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("normalized error value = %#v", value)
	}

	r = newReporter()
	r.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValueWithTerminal("", "cancelled", messages.TerminalReasonCancellation, messages.TerminalProvenanceCLI, messages.TerminalOutputPartial),
	}, false)
	if r.outcome.cancellation == nil || r.outcome.failure != nil {
		t.Fatalf("cancellation error candidates = cancellation:%v failure:%v", r.outcome.cancellation, r.outcome.failure)
	}
}

func TestSessionCloseObservationTracksReplayFailureAndSpecificCandidates(t *testing.T) {
	r := newReporter()
	r.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal("", "", "replay", messages.TerminalReasonReplayComplete, messages.TerminalProvenanceReplay, messages.TerminalOutputComplete),
	}, true)
	if !r.outcome.replayComplete || r.outcome.completion != completionComplete {
		t.Fatalf("replay close state = %#v", r.outcome)
	}

	r = newReporter()
	r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonSessionClose), "generic", messages.TerminalOutputNone), false)
	r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonProviderClose), "provider", messages.TerminalOutputNone), true)
	r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonProviderAuthoredCompletion), "later", messages.TerminalOutputComplete), true)
	if r.outcome.observedTerminal == nil || r.outcome.observedTerminal.value.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("specific observed terminal = %#v, want provider close", r.outcome.observedTerminal)
	}

	for _, reason := range []messages.TerminalReason{
		messages.TerminalReasonTerminalFailure,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete,
	} {
		r = newReporter()
		r.ObserveStreamMessage(terminalMessage(string(reason), "failure", messages.TerminalOutputPartial), true)
		if r.outcome.failure == nil || r.outcome.failure.value.TerminalReason != reason {
			t.Fatalf("failure reason %q was not retained: %#v", reason, r.outcome.failure)
		}
	}
}

func TestNormalizationDefaultsAndCandidateHelpers(t *testing.T) {
	r := startedReporter()
	r.ObserveStreamMessage(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")}, false)
	r.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal("", "", "", messages.TerminalReasonProviderClose, "", ""),
	}, true)
	got := publishText(t, r, nil)
	want := "classification=transport terminal_reason=provider_close terminal_provenance=session output_state=partial"
	if !strings.Contains(got, want) {
		t.Fatalf("default normalization output = %q, want %q", got, want)
	}
	if terminalReasonOrDefault(nil, messages.TerminalReasonCancellation) != messages.TerminalReasonCancellation ||
		terminalReasonOrDefault(&messages.SessionCloseValue{}, messages.TerminalReasonSessionClose) != messages.TerminalReasonSessionClose {
		t.Fatal("terminal reason fallback was not applied")
	}
	if cloneSessionCloseValue(nil) != nil {
		t.Fatal("nil close value was not preserved")
	}
	if candidateIsSpecific(nil) || candidateIsSpecific(&candidate{}) || candidateIsSpecific(&candidate{value: &messages.SessionCloseValue{TerminalReason: messages.TerminalReasonSessionClose}}) ||
		!candidateIsSpecific(&candidate{value: &messages.SessionCloseValue{TerminalReason: messages.TerminalReasonProviderClose}}) {
		t.Fatal("candidate specificity classification is incorrect")
	}
}

func TestFatalRetentionIsBoundedWhileSmallErrorIdentitySurvives(t *testing.T) {
	r := newReporter()
	first := errors.New("first artifact")
	r.RecordArtifactFinalization(true, first)
	for range maxFatalErrors + 3 {
		r.RecordArtifactFinalization(true, errors.New("additional artifact"))
	}
	if !errors.Is(r.outcome.fatalError, first) {
		t.Fatalf("small artifact identity was lost: %v", r.outcome.fatalError)
	}
	if r.outcome.fatalErrors != maxFatalErrors+4 {
		t.Fatalf("fatal error count = %d, want %d", r.outcome.fatalErrors, maxFatalErrors+4)
	}

	long := errors.New(strings.Repeat("x", maxRetainedErrorBytes+1))
	r = newReporter()
	r.RecordArtifactFinalization(true, long)
	if errors.Is(r.outcome.fatalError, long) || len(r.outcome.fatalError.Error()) > maxRetainedErrorBytes {
		t.Fatalf("long artifact error was retained unbounded: %q", r.outcome.fatalError)
	}
	deep := error(error(nil))
	for range maxErrorTraversalNodes + 2 {
		deep = errors.Join(deep, errors.New("deep"))
	}
	r = startedReporter()
	if err := r.Publish(io.Discard, deep); err != nil {
		t.Fatalf("publish deep failure: %v", err)
	}
	if !errors.Is(r.outcome.fatalError, errTerminalRunFailure) {
		t.Fatalf("deep run error retention = %v, want bounded marker", r.outcome.fatalError)
	}
}

type emptyJoinedError struct{}

func (emptyJoinedError) Error() string   { return "empty joined" }
func (emptyJoinedError) Unwrap() []error { return nil }

type valueError struct{}

func (valueError) Error() string { return "value error" }

func TestErrorTraversalHelpersHandleCyclesNilAndCancellation(t *testing.T) {
	if leaves, bounded := errorLeaves(emptyJoinedError{}); bounded || len(leaves) != 0 {
		t.Fatalf("empty joined leaves = %#v bounded:%v, want unbounded", leaves, bounded)
	}
	if sessionErrorIsCancellation(errors.New("ordinary")) || sessionErrorIsCancellation(&cyclicError{}) {
		t.Fatal("ordinary/cyclic errors were classified as cancellation")
	}
	if !sessionErrorIsCancellation(errors.Join(context.Canceled, terminaloutcome.ErrSessionMaxDurationExpired)) {
		t.Fatal("duration plus cancellation was not classified as cancellation")
	}
	if typedNilError(nil) || typedNilError(errors.New("value")) {
		t.Fatal("non-typed-nil errors were classified as typed nil")
	}
	var nilCyclic *cyclicError
	if !typedNilError(nilCyclic) {
		t.Fatal("typed nil pointer was not detected")
	}
	if _, ok := pointerErrorIdentity(valueError{}); ok {
		t.Fatal("value error unexpectedly received pointer identity")
	}
	if _, ok := pointerErrorIdentity(&cyclicError{}); !ok {
		t.Fatal("pointer error identity was not detected")
	}
}

func TestRenderNilAndInvalidUTF8BranchesRemainSafe(t *testing.T) {
	if err := writePublishedTerminal(io.Discard, nil, false); err != nil {
		t.Fatalf("nil published candidate: %v", err)
	}
	if err := writePublishedTerminal(io.Discard, &candidate{}, false); err != nil {
		t.Fatalf("empty published candidate: %v", err)
	}
	if err := writeSessionClose(io.Discard, nil, false); err != nil {
		t.Fatalf("nil close value: %v", err)
	}
	if err := writeTerminalString(nil, "discarded"); err != nil {
		t.Fatalf("nil terminal writer: %v", err)
	}
	invalid := strings.Repeat("a", maxTerminalFieldBytes-1) + "\xc3z"
	if got := boundTerminalText(invalid); !strings.HasSuffix(got, "a") || !strings.HasPrefix(got, strings.Repeat("a", maxTerminalFieldBytes-1)) {
		t.Fatalf("invalid UTF-8 bound = %q", got)
	}

	r := startedReporter()
	if err := r.Publish(bytes.NewBuffer(nil), nil); err != nil {
		t.Fatalf("empty terminal render: %v", err)
	}
}

type nilChildError struct{}

func (nilChildError) Error() string   { return "nil child" }
func (nilChildError) Unwrap() []error { return []error{nil} }

type valueCycleError struct{}

func (valueCycleError) Error() string { return "value cycle" }
func (valueCycleError) Unwrap() error { return valueCycleError{} }

func TestServiceFactoryAndUncoveredSafetyBranches(t *testing.T) {
	s := New()
	if s.NewReporter() == nil {
		t.Fatal("service factory returned nil reporter")
	}
	if sessionErrorIsCancellation(terminaloutcome.ErrSessionMaxDurationExpired) {
		t.Fatal("duration sentinel was classified as cancellation")
	}
	if leaves, bounded := errorLeaves(nilChildError{}); !bounded || len(leaves) != 0 {
		t.Fatalf("nil-child error leaves = %#v bounded:%v", leaves, bounded)
	}
	if leaves, bounded := errorLeaves(valueCycleError{}); bounded || len(leaves) != 0 {
		t.Fatalf("value-cycle error leaves = %#v bounded:%v", leaves, bounded)
	}
	if _, ok := pointerErrorIdentity(nil); ok {
		t.Fatal("nil error unexpectedly received pointer identity")
	}
	var nilCyclic *cyclicError
	if _, ok := pointerErrorIdentity(nilCyclic); ok {
		t.Fatal("typed nil pointer unexpectedly received pointer identity")
	}
	if streamMessageHasOutput(messages.StreamMessage{}) {
		t.Fatal("unknown stream message was classified as output")
	}
	var nilToolEnd *messages.ToolCallEndValue
	if streamMessageHasOutput(messages.StreamMessage{Value: nilToolEnd}) {
		t.Fatal("typed-nil tool end was classified as output")
	}
	if normalizeTerminalValue(nil, messages.TerminalReasonCancellation, messages.TerminalOutputNone) == nil {
		t.Fatal("nil terminal value was not normalized")
	}

	r := newReporter()
	r.rememberFatalError(nil)
	if retainFatalError(nil) != nil {
		t.Fatal("nil fatal error was retained")
	}
	var nilReporter *reporter
	nilReporter.markRunFailure()
}
