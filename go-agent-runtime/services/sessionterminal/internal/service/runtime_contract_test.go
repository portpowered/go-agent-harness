package service

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func TestReporterPublishesPartialProviderFailureOnce(t *testing.T) {
	reporter := NewReporter()
	reporter.MarkRunStarted()
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("partial answer"),
	}, false)

	var out bytes.Buffer
	if err := reporter.Publish(&out, errors.New("provider failed")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for _, want := range []string{
		"terminal_reason=terminal_failure",
		"terminal_provenance=session",
		"output_state=partial",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("published terminal %q does not contain %q", out.String(), want)
		}
	}
	if err := reporter.Publish(&out, nil); !errors.Is(err, sessionterminal.ErrAlreadyPublished) {
		t.Fatalf("second Publish error = %v, want already-published identity", err)
	}
}

func TestReporterContextAndExpectedCancellationClassification(t *testing.T) {
	reporter := NewReporter()
	ctx := WithReporter(context.Background(), reporter)
	if got := ReporterFromContext(ctx); got != reporter {
		t.Fatal("reporter context did not preserve the invocation reporter")
	}
	if ReporterFromContext(nil) != nil {
		t.Fatal("nil context returned a reporter")
	}

	expectedStop := errors.Join(context.Canceled, sessionterminal.ErrDurationExpired)
	if HasIndependentFailure(expectedStop) || !IsCancellation(expectedStop) {
		t.Fatalf("expected cancellation classification = independent:%v cancellation:%v", HasIndependentFailure(expectedStop), IsCancellation(expectedStop))
	}
	providerFailure := errors.New("provider failure")
	if !HasIndependentFailure(errors.Join(context.Canceled, providerFailure)) || IsCancellation(errors.Join(context.Canceled, providerFailure)) {
		t.Fatal("provider failure joined with cancellation was misclassified as a clean cancellation")
	}
}

func TestReporterPublishesDurationBoundaryAfterRunCancellation(t *testing.T) {
	reporter := NewReporter()
	reporter.MarkRunStarted()
	reporter.ObserveStreamMessage(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("partial result"),
	}, false)
	reporter.MarkDurationExpiry(true, messages.TerminalOutputPartial)

	var out bytes.Buffer
	if err := reporter.Publish(&out, context.Canceled); err != nil {
		t.Fatalf("Publish after duration cancellation: %v", err)
	}
	for _, want := range []string{
		"terminal_reason=max_duration",
		"terminal_provenance=loop",
		"output_state=partial",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("duration terminal %q does not contain %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "terminal_reason=cancellation") || strings.Contains(out.String(), "session replay complete") {
		t.Fatalf("duration expiry was incorrectly reported as cancellation or replay completion: %q", out.String())
	}
}

func TestTerminationBoundaryOrdersAndJoinsCleanup(t *testing.T) {
	primary := errors.New("provider failure")
	quiesceErr := errors.New("quiesce failed")
	waitErr := errors.New("drain failed")
	stopErr := errors.New("stop failed")
	flushErr := errors.New("flush failed")
	var order []string
	boundary := NewTerminationBoundary(sessionterminal.TerminationOptions{
		QuiesceUpstream: func() error { order = append(order, "quiesce"); return quiesceErr },
		WaitForStragglers: func() error {
			order = append(order, "wait")
			return waitErr
		},
		StopOwnedResources: func() error { order = append(order, "stop"); return stopErr },
		FlushBuffered:      func() error { order = append(order, "flush"); return flushErr },
	})

	got := boundary.Terminate(primary)
	for _, cause := range []error{primary, quiesceErr, waitErr, stopErr, flushErr} {
		if !errors.Is(got, cause) {
			t.Errorf("termination error %v does not preserve %v", got, cause)
		}
	}
	if want := []string{"quiesce", "wait", "stop", "flush"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("cleanup order = %v, want %v", order, want)
	}
	second := errors.New("must not replace the first cause")
	if repeated := boundary.Terminate(second); !errors.Is(repeated, primary) || errors.Is(repeated, second) {
		t.Fatalf("repeated termination error = %v, want original result", repeated)
	}
	if !reflect.DeepEqual(order, []string{"quiesce", "wait", "stop", "flush"}) {
		t.Fatalf("termination callbacks ran more than once: %v", order)
	}
}

func TestTranscriptRendererSeparatesInterleavedSpeakers(t *testing.T) {
	var out bytes.Buffer
	var observed []messages.StreamMessageType
	renderer := New().NewTranscriptRenderer(&out, func(msg messages.StreamMessage, _ bool) {
		observed = append(observed, msg.Type)
	})
	messagesToWrite := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleUser, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("heard")},
		{Type: messages.StreamTypeTranscriptStart, Role: messages.RoleAssistant, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, Value: messages.NewTranscriptDeltaValue("answer")},
		{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleAssistant, Value: messages.NewTranscriptEndValue("answer")},
	}
	for _, msg := range messagesToWrite {
		if err := renderer.WriteMessage(msg); err != nil {
			t.Fatalf("WriteMessage(%s): %v", msg.Type, err)
		}
	}
	if err := renderer.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got, want := out.String(), "User: heard\nAssistant: answer\n"; got != want {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(observed, []messages.StreamMessageType{
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTranscriptEnd,
	}) {
		t.Fatalf("transcript observer messages = %v", observed)
	}
}
