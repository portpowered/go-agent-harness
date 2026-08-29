package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionSIGINTCancellationResolvesPendingObligations(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime")
	observer.sawSessionOpen = true
	observer.turnsCompleted = 1
	observer.scheduleAudioInputs([]ScheduledAudioInput{
		{AfterCompletedTurns: 0},
		{AfterCompletedTurns: 1},
	})
	observer.dispatchedInputs = 1
	observer.completedScheduled = 0
	observer.setToolResultsEnabled(true)
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue("call-unresolved", "lookup", `{}`),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue("call-pending", "lookup", `{}`),
	})
	observer.noteToolResultAccepted("call-pending")

	intent := NewSessionCancellationIntent()
	intent.MarkSIGINT()
	observer.cancellationIntent = intent
	err := errors.Join(
		context.Canceled,
		&SessionScheduledAudioIncompleteError{Completed: 0, Dispatched: 1, Scheduled: 2},
		&SessionUnresolvedToolResultsError{CallIDs: []string{"call-unresolved"}},
		&SessionToolContinuationError{CallIDs: []string{"call-pending"}},
	)
	if got := observer.finish(err); got != nil {
		t.Fatalf("SIGINT finish error = %v, want nil", got)
	}
	if got := observer.finish(context.Canceled); got != nil {
		t.Fatalf("second SIGINT finish error = %v, want nil", got)
	}

	terminal := sink.events(SessionDiagnosticEventTerminal)
	if len(terminal) != 1 {
		t.Fatalf("terminal records = %d, want exactly one: %#v", len(terminal), terminal)
	}
	if failures := sink.events(SessionDiagnosticEventFailure); len(failures) != 0 {
		t.Fatalf("SIGINT emitted failure records: %#v", failures)
	}
	fields := terminal[0].Fields
	for field, want := range map[string]string{
		fieldClassification:                                    SessionUserCancelledClassification,
		fieldTerminalReason:                                    string(messages.TerminalReasonCancellation),
		fieldTerminalProvenance:                                string(messages.TerminalProvenanceCLI),
		fieldOutputState:                                       string(messages.TerminalOutputPartial),
		SessionDiagnosticFieldCancelledBy:                      "user",
		SessionDiagnosticFieldCancelledScheduledInputCount:     "2",
		SessionDiagnosticFieldCancelledToolResultCount:         "1",
		SessionDiagnosticFieldCancelledToolResultCallIDs:       "call-unresolved",
		SessionDiagnosticFieldCancelledToolContinuationCount:   "1",
		SessionDiagnosticFieldCancelledToolContinuationCallIDs: "call-pending",
	} {
		if got := fields[field]; got != want {
			t.Fatalf("terminal field %q = %q, want %q; fields=%v", field, got, want, fields)
		}
	}

	var out bytes.Buffer
	if err := publishSessionUserCancellation(&out, sessionLoopOptions{observer: observer}, writeSessionReplayMessage); err != nil {
		t.Fatalf("publish cancellation terminal: %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"classification=user_cancelled",
		"terminal_reason=cancellation",
		"terminal_provenance=cli",
		"output_state=partial",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("cancellation output missing %q: %q", want, output)
		}
	}
}

func TestSessionSIGINTCancellationIgnoresLoopCancellationDiagnostic(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime")
	intent := NewSessionCancellationIntent()
	intent.MarkSIGINT()
	observer.cancellationIntent = intent
	value := messages.NewErrorValueWithTerminal(
		context.Canceled.Error(),
		string(messages.TerminalReasonCancellation),
		messages.TerminalReasonCancellation,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputNone,
	)
	value.Err = context.Canceled
	message := messages.StreamMessage{Type: messages.StreamTypeError, Value: value}
	observer.observe(message)

	writeErr := writeSessionReplayMessage(io.Discard, message)
	if !errors.Is(writeErr, context.Canceled) {
		t.Fatalf("rendered cancellation error = %v, want context.Canceled cause", writeErr)
	}
	if got := observer.finish(writeErr); got != nil {
		t.Fatalf("loop cancellation diagnostic finish error = %v, want nil", got)
	}
	if observer.failure != nil {
		t.Fatalf("loop cancellation diagnostic retained failure facts: %#v", observer.failure)
	}
	if got := len(sink.events(SessionDiagnosticEventFailure)); got != 0 {
		t.Fatalf("loop cancellation diagnostic emitted %d failure records, want 0", got)
	}
	if got := len(sink.events(SessionDiagnosticEventTerminal)); got != 1 {
		t.Fatalf("loop cancellation diagnostic terminal records = %d, want 1", got)
	}
}

func TestSessionSIGINTCancellationDoesNotSuppressIndependentFailure(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime")
	intent := NewSessionCancellationIntent()
	intent.MarkSIGINT()
	observer.cancellationIntent = intent
	want := errors.New("provider rejected request")

	err := observer.finish(errors.Join(context.Canceled, want))
	if !errors.Is(err, want) {
		t.Fatalf("SIGINT plus independent error = %v, want provider error", err)
	}
	if observer.userCancelled {
		t.Fatal("independent failure was classified as user cancellation")
	}
	if terminal := sink.events(SessionDiagnosticEventTerminal); len(terminal) != 0 {
		t.Fatalf("independent failure emitted clean terminal records: %#v", terminal)
	}
}

func TestSessionOrdinaryCancellationRetainsIncompleteToolContract(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue("call-ordinary-cancel", "lookup", `{}`),
	})

	err := observer.finish(context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ordinary cancellation = %v, want context.Canceled", err)
	}
	if !errors.Is(err, ErrSessionUnresolvedToolResults) {
		t.Fatalf("ordinary cancellation = %v, want unresolved tool result", err)
	}
	if observer.userCancelled {
		t.Fatal("ordinary context cancellation was marked as SIGINT")
	}
}

func TestSessionCancellationIntentIsRunScoped(t *testing.T) {
	first := NewSessionCancellationIntent()
	second := NewSessionCancellationIntent()
	first.MarkSIGINT()

	if !first.SIGINTReceived() {
		t.Fatal("marked intent did not report SIGINT")
	}
	if second.SIGINTReceived() {
		t.Fatal("separate run inherited SIGINT intent")
	}
}
