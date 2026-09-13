package service

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func TestFinalizeNoRecordForCleanOrContextOnlyTermination(t *testing.T) {
	service := New()
	for _, request := range []sessionterminal.Request{
		{},
		{RunError: context.Canceled},
		{RunError: context.DeadlineExceeded},
	} {
		result := service.Finalize(request)
		if hasEvent(result, sessionterminal.EventFailure) {
			t.Fatalf("request %#v produced failure: %#v", request, result.Records)
		}
		if !hasEvent(result, sessionterminal.EventMetrics) {
			t.Fatalf("request %#v did not produce metrics", request)
		}
	}
}

func TestFinalizeHintsAndDefaults(t *testing.T) {
	cause := errors.New("continuation stopped")
	result := New().Finalize(sessionterminal.Request{
		RunError:       cause,
		TurnsCompleted: 1,
		Output:         sessionterminal.OutputSnapshot{SawSessionOpen: true},
		Lifecycle: sessionterminal.LifecycleSnapshot{
			PendingToolContinuationIDs: []string{"tool-call"},
			FailureHints:               []string{sessionterminal.FailureHintToolContinuationIncomplete},
		},
	})
	if !errors.Is(result.Error, cause) {
		t.Fatal("terminal cause was not retained")
	}
	failure := event(result, sessionterminal.EventFailure)
	if failure.Fields[sessionterminal.FieldClassification] != "tool_continuation" || failure.Fields[sessionterminal.FieldFailingEvent] != "SESSION.RUN" {
		t.Fatalf("unexpected failure fields: %#v", failure.Fields)
	}
	if failure.Fields[sessionterminal.FieldTerminalReason] != string(messages.TerminalReasonTerminalFailure) {
		t.Fatalf("missing terminal reason: %#v", failure.Fields)
	}
}

func TestCancellationOutputStateUsesObservedOutputOnly(t *testing.T) {
	service := New()
	if got := service.CancellationOutputState(sessionterminal.OutputSnapshot{SawSessionOpen: true}); got != messages.TerminalOutputNone {
		t.Fatalf("session without output = %q", got)
	}
	if got := service.CancellationOutputState(sessionterminal.OutputSnapshot{ResponseOutputTextBytes: 1}); got != messages.TerminalOutputPartial {
		t.Fatalf("response output = %q", got)
	}
}

func hasEvent(result sessionterminal.Result, name string) bool {
	for _, record := range result.Records {
		if record.Event == name {
			return true
		}
	}
	return false
}

func event(result sessionterminal.Result, name string) sessionterminal.Record {
	for _, record := range result.Records {
		if record.Event == name {
			return record
		}
	}
	panic("missing event " + name)
}
