package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
)

func startedReporter() *reporter {
	r := newReporter()
	r.MarkRunStarted()
	return r
}

func terminalMessage(reason, classification string, output messages.TerminalOutputState) messages.StreamMessage {
	return messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"", reason, classification, messages.TerminalReason(reason),
			messages.TerminalProvenanceProvider, output,
		),
	}
}

func publishText(t *testing.T, r *reporter, runErr error) string {
	t.Helper()
	var out bytes.Buffer
	if err := r.Publish(&out, runErr); err != nil {
		t.Fatalf("publish: %v", err)
	}
	return out.String()
}

func TestPrecedenceReconcilesEveryTerminalCandidate(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*reporter)
		want  string
	}{
		{
			name: "fatal beats replay cancellation observed and duration",
			setup: func(r *reporter) {
				r.MarkDurationExpiry(messages.TerminalOutputPartial)
				r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonCancellation), "cancel", messages.TerminalOutputPartial), true)
				r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonProviderAuthoredCompletion), "provider", messages.TerminalOutputComplete), true)
				r.MarkReplayComplete()
				r.RecordArtifactFinalization(true, errors.New("artifact failure"))
			},
			want: "terminal_reason=terminal_failure",
		},
		{
			name: "replay beats cancellation",
			setup: func(r *reporter) {
				r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonCancellation), "cancel", messages.TerminalOutputPartial), true)
				r.MarkReplayComplete()
			},
			want: "classification=replay_complete terminal_reason=replay_complete terminal_provenance=replay output_state=complete",
		},
		{
			name: "cancellation beats observed terminal",
			setup: func(r *reporter) {
				r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonProviderAuthoredCompletion), "provider", messages.TerminalOutputComplete), true)
				r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonCancellation), "cancel", messages.TerminalOutputPartial), true)
			},
			want: "terminal_reason=cancellation",
		},
		{
			name: "observed terminal beats duration",
			setup: func(r *reporter) {
				r.MarkDurationExpiry(messages.TerminalOutputPartial)
				r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonProviderAuthoredCompletion), "provider", messages.TerminalOutputComplete), true)
			},
			want: "terminal_reason=provider_authored_completion",
		},
		{
			name: "duration beats run cancellation",
			setup: func(r *reporter) {
				r.MarkDurationExpiry(messages.TerminalOutputPartial)
			},
			want: "terminal_reason=max_duration",
		},
		{
			name:  "run cancellation is fallback",
			setup: func(*reporter) {},
			want:  "terminal_reason=cancellation",
		},
		{
			name:  "empty started run fails closed",
			setup: func(*reporter) {},
			want:  "terminal_reason=session_close",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			r := startedReporter()
			testCase.setup(r)
			runErr := error(nil)
			if testCase.name == "run cancellation is fallback" {
				runErr = context.Canceled
			}
			got := publishText(t, r, runErr)
			if !strings.Contains(got, testCase.want) {
				t.Fatalf("output = %q, want %q", got, testCase.want)
			}
			if testCase.name == "fatal beats replay cancellation observed and duration" && strings.Contains(got, "replay complete]") {
				t.Fatalf("fatal evidence was overwritten by replay completion: %q", got)
			}
		})
	}
}

func TestOutputClassificationCoversSupportedValuesAndBoundaries(t *testing.T) {
	values := []struct {
		name  string
		typ   messages.StreamMessageType
		value messages.StreamMessageValue
	}{
		{"text", messages.StreamTypeTextDelta, messages.NewTextDeltaValue("x")},
		{"reasoning", messages.StreamTypeReasoningDelta, messages.NewReasoningDeltaValue("x")},
		{"audio", messages.StreamTypeAudioDelta, messages.NewAudioDeltaValue([]byte{1})},
		{"image", messages.StreamTypeImageDelta, messages.NewImageDeltaValue([]byte{1})},
		{"video", messages.StreamTypeVideoDelta, messages.NewVideoDeltaValue([]byte{1})},
		{"file", messages.StreamTypeFileDelta, messages.NewFileDeltaValue([]byte{1})},
		{"embedding", messages.StreamTypeEmbeddingDelta, messages.NewEmbeddingDeltaValue([]byte{1})},
		{"tool delta", messages.StreamTypeToolCallDelta, messages.NewToolCallDeltaValue("{}")},
		{"tool end", messages.StreamTypeToolCallEnd, messages.NewToolCallEndValue("id", "tool", "{}")},
		{"refusal", messages.StreamTypeRefusal, messages.NewRefusalValue("no")},
		{"transcript", messages.StreamTypeTranscriptDelta, messages.NewTranscriptDeltaValue("x")},
	}
	for _, testCase := range values {
		t.Run(testCase.name, func(t *testing.T) {
			r := newReporter()
			r.ObserveStreamMessage(messages.StreamMessage{Type: testCase.typ, Value: testCase.value}, false)
			if r.outcome.outputState != messages.TerminalOutputPartial {
				t.Fatalf("output state = %q, want partial", r.outcome.outputState)
			}
		})
	}

	emptyValues := []struct {
		name  string
		typ   messages.StreamMessageType
		value messages.StreamMessageValue
	}{
		{"empty text", messages.StreamTypeTextDelta, messages.NewTextDeltaValue("")},
		{"empty reasoning", messages.StreamTypeReasoningDelta, messages.NewReasoningDeltaValue("")},
		{"empty audio", messages.StreamTypeAudioDelta, messages.NewAudioDeltaValue(nil)},
		{"empty image", messages.StreamTypeImageDelta, messages.NewImageDeltaValue(nil)},
		{"empty video", messages.StreamTypeVideoDelta, messages.NewVideoDeltaValue(nil)},
		{"empty file", messages.StreamTypeFileDelta, messages.NewFileDeltaValue(nil)},
		{"empty embedding", messages.StreamTypeEmbeddingDelta, messages.NewEmbeddingDeltaValue(nil)},
		{"empty tool delta", messages.StreamTypeToolCallDelta, messages.NewToolCallDeltaValue("")},
		{"empty refusal", messages.StreamTypeRefusal, messages.NewRefusalValue("")},
		{"empty transcript", messages.StreamTypeTranscriptDelta, messages.NewTranscriptDeltaValue("")},
	}
	for _, testCase := range emptyValues {
		t.Run(testCase.name, func(t *testing.T) {
			r := newReporter()
			r.ObserveStreamMessage(messages.StreamMessage{Type: testCase.typ, Value: testCase.value}, false)
			if r.outcome.outputState != "" {
				t.Fatalf("empty value changed output state to %q", r.outcome.outputState)
			}
		})
	}

	r := newReporter()
	r.ObserveStreamMessage(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("user")}, false)
	if r.outcome.outputState != "" {
		t.Fatalf("user transcript was counted as model output: %q", r.outcome.outputState)
	}
	r.ObserveStreamMessage(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("assistant")}, false)
	r.ObserveStreamMessage(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, false)
	if r.outcome.outputState != messages.TerminalOutputComplete {
		t.Fatalf("message end state = %q, want complete", r.outcome.outputState)
	}
	r.ObserveStreamMessage(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}, false)
	if r.outcome.outputState != messages.TerminalOutputNone {
		t.Fatalf("message start state = %q, want none", r.outcome.outputState)
	}
	if !streamMessageHasOutput(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("", "", "")}) {
		t.Fatal("non-nil empty tool end was not treated as output")
	}
}

func TestArtifactFinalizationStatesPreserveFailurePrecedence(t *testing.T) {
	tests := []struct {
		name        string
		requested   bool
		artifactErr error
		wantState   artifactState
		wantReason  string
	}{
		{name: "not applicable", requested: false, wantState: artifactNotApplicable, wantReason: "cancellation"},
		{name: "valid", requested: true, wantState: artifactValid, wantReason: "max_duration"},
		{name: "invalid", requested: true, artifactErr: errors.New("artifact invalid"), wantState: artifactInvalid, wantReason: "terminal_failure"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			r := startedReporter()
			if testCase.requested {
				r.MarkDurationExpiry(messages.TerminalOutputPartial)
			}
			r.RecordArtifactFinalization(testCase.requested, testCase.artifactErr)
			if r.outcome.artifactState != testCase.wantState {
				t.Fatalf("artifact state = %d, want %d", r.outcome.artifactState, testCase.wantState)
			}
			got := publishText(t, r, context.Canceled)
			if !strings.Contains(got, "terminal_reason="+testCase.wantReason) {
				t.Fatalf("output = %q, want reason %q", got, testCase.wantReason)
			}
			if testCase.artifactErr != nil && !errors.Is(r.outcome.fatalError, testCase.artifactErr) {
				t.Fatalf("fatal error = %v, want artifact identity", r.outcome.fatalError)
			}
		})
	}
}

func TestDurationAndCancelBoundariesFailClosed(t *testing.T) {
	var before bytes.Buffer
	r := newReporter()
	if err := r.Publish(&before, context.Canceled); err != nil {
		t.Fatalf("publish before start: %v", err)
	}
	if before.Len() != 0 {
		t.Fatalf("pre-start publish emitted %q", before.String())
	}
	r.MarkRunStarted()
	if err := r.Publish(&before, nil); !errors.Is(err, terminaloutcome.ErrSessionTerminalAlreadyPublished) {
		t.Fatalf("second pre-start publication = %v, want sentinel", err)
	}

	r = startedReporter()
	r.MarkDurationExpiry(messages.TerminalOutputPartial)
	got := publishText(t, r, context.Canceled)
	if !strings.Contains(got, "terminal_reason=max_duration") || strings.Contains(got, "terminal_reason=cancellation") {
		t.Fatalf("duration/cancel output = %q", got)
	}
}

func TestReplayCompletionRendersExactMarkerBytes(t *testing.T) {
	r := startedReporter()
	r.MarkReplayComplete()
	got := publishText(t, r, nil)
	want := "\n[session terminal: classification=replay_complete terminal_reason=replay_complete terminal_provenance=replay output_state=complete]\n[session replay complete]\n"
	if got != want {
		t.Fatalf("replay output = %q, want %q", got, want)
	}
}

func TestRenderPreservesCloseAndLeadingNewlineBytes(t *testing.T) {
	r := startedReporter()
	r.ObserveStreamMessage(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"", "client_close", "transport", messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider, messages.TerminalOutputNone,
		),
	}, true)
	got := publishText(t, r, nil)
	want := "\n[session closed: client_close]\n[session terminal: classification=transport terminal_reason=provider_close terminal_provenance=provider output_state=none]\n"
	if got != want {
		t.Fatalf("close output = %q, want %q", got, want)
	}

	r = startedReporter()
	r.ObserveStreamMessage(terminalMessage(string(messages.TerminalReasonProviderAuthoredCompletion), "provider", messages.TerminalOutputComplete), false)
	got = publishText(t, r, nil)
	if strings.HasPrefix(got, "\n") {
		t.Fatalf("non-leading close unexpectedly began with newline: %q", got)
	}
}

type failingWriter struct {
	err   error
	short bool
}

func (w failingWriter) Write(value []byte) (int, error) {
	if w.short {
		return len(value) - 1, nil
	}
	return 0, w.err
}

type typedWriterError struct{}

func (*typedWriterError) Error() string { return "typed writer failure" }

func TestWriterFailuresPropagateShortWriteAndIdentity(t *testing.T) {
	r := startedReporter()
	errWriter := &typedWriterError{}
	if err := r.Publish(failingWriter{err: errWriter}, nil); !errors.Is(err, errWriter) {
		t.Fatalf("writer error = %v, want identity", err)
	} else {
		var typed *typedWriterError
		if !errors.As(err, &typed) {
			t.Fatalf("writer error %v lost errors.As identity", err)
		}
	}

	r = startedReporter()
	if err := r.Publish(failingWriter{short: true}, nil); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
	}
	if err := r.Publish(io.Discard, nil); !errors.Is(err, terminaloutcome.ErrSessionTerminalAlreadyPublished) {
		t.Fatalf("publication after writer failure = %v, want sentinel", err)
	}
}

func TestPublishIsExactlyOnceUnderConcurrentPublication(t *testing.T) {
	r := startedReporter()
	const callers = 24
	var group sync.WaitGroup
	errs := make(chan error, callers)
	outputs := make(chan string, callers)
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			var out bytes.Buffer
			errs <- r.Publish(&out, nil)
			outputs <- out.String()
		}()
	}
	group.Wait()
	close(errs)
	close(outputs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, terminaloutcome.ErrSessionTerminalAlreadyPublished) {
			t.Fatalf("concurrent loser error = %v, want sentinel", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful publishers = %d, want 1", successes)
	}
	blocks := 0
	for output := range outputs {
		blocks += strings.Count(output, "[session terminal:")
	}
	if blocks != 1 {
		t.Fatalf("published terminal blocks = %d, want 1", blocks)
	}
}

type cyclicError struct{}

func (*cyclicError) Error() string   { return "cyclic" }
func (e *cyclicError) Unwrap() error { return e }

type typedNilTestError struct{}

func (*typedNilTestError) Error() string { return "typed nil" }

func TestErrorTraversalPreservesCancellationAndFailsClosed(t *testing.T) {
	s := New()
	if s.HasIndependentFailure(errors.Join(context.Canceled, terminaloutcome.ErrSessionMaxDurationExpired)) {
		t.Fatal("duration plus cancellation was classified as independent failure")
	}
	if !s.HasIndependentFailure(errors.Join(context.Canceled, errors.New("independent"))) {
		t.Fatal("mixed cancellation and independent error was not fatal")
	}
	ignored := errors.New("host lifecycle sentinel")
	if s.HasIndependentFailure(errors.Join(context.Canceled, ignored), ignored) {
		t.Fatal("ignored host sentinel was treated as independent failure")
	}

	cyclic := &cyclicError{}
	if !s.HasIndependentFailure(cyclic) {
		t.Fatal("cyclic error was not fail-closed")
	}
	var typedNil *typedNilTestError
	var typedNilAsError error = typedNil
	if !s.HasIndependentFailure(typedNilAsError) {
		t.Fatal("typed-nil error was not fail-closed")
	}

	r := startedReporter()
	got := publishText(t, r, fmt.Errorf("wrapped: %w", context.Canceled))
	if !strings.Contains(got, "terminal_reason=cancellation") {
		t.Fatalf("wrapped cancellation output = %q", got)
	}
	r = startedReporter()
	got = publishText(t, r, errors.Join(context.Canceled, errors.New("provider failed")))
	if !strings.Contains(got, "terminal_reason=terminal_failure") {
		t.Fatalf("mixed failure output = %q", got)
	}
}

func TestRenderBoundsRetainedTerminalEvidence(t *testing.T) {
	long := strings.Repeat("x", maxTerminalFieldBytes*20)
	r := startedReporter()
	r.ObserveStreamMessage(messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"", long, long, messages.TerminalReason(long),
			messages.TerminalProvenance(long), messages.TerminalOutputState(long),
		),
	}, true)
	if got := len(r.outcome.observedTerminal.value.Reason); got > maxTerminalFieldBytes {
		t.Fatalf("retained reason bytes = %d, want <= %d", got, maxTerminalFieldBytes)
	}
	got := publishText(t, r, nil)
	if strings.Contains(got, long) || len(got) > 1800 {
		t.Fatalf("bounded render leaked/unbounded evidence: len=%d output=%q", len(got), got)
	}

	deep := error(error(nil))
	for range maxErrorTraversalNodes + 20 {
		deep = fmt.Errorf("deep: %w", deep)
	}
	if !New().HasIndependentFailure(deep) {
		t.Fatal("over-depth error traversal was not fail-closed")
	}
}

func TestNilReporterAndWriterAreSafe(t *testing.T) {
	var r *reporter
	r.MarkRunStarted()
	r.ObserveStreamMessage(messages.StreamMessage{}, false)
	r.MarkDurationExpiry(messages.TerminalOutputNone)
	r.MarkReplayComplete()
	r.RecordArtifactFinalization(true, nil)
	if err := r.Publish(nil, nil); err != nil {
		t.Fatalf("nil reporter publish = %v, want nil", err)
	}

	r = startedReporter()
	if err := r.Publish(nil, nil); err != nil {
		t.Fatalf("nil writer publish = %v, want nil", err)
	}
}
