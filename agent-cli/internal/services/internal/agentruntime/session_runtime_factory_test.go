package agentruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Tests may customize the factory while production receives a fresh factory
// from the runtime constructor for each plan.
var defaultSessionRuntimeFactory = newDefaultSessionRuntimeFactory()

const (
	SessionScheduledAudioClassification       = "scheduled_audio_incomplete"
	SessionUnresolvedToolResultClassification = "unresolved_tool_result"
	SessionImageContinuationClassification    = "image_tool_continuation"
	SessionToolContinuationClassification     = "tool_continuation"
)

func TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "provider", "model")
	observer.sawSessionOpen = true
	observer.unresolvedToolCalls["call-z"] = struct{}{}
	observer.unresolvedToolCalls["call-a"] = struct{}{}

	if err := observer.finish(ErrSessionUnresolvedToolResults); !errors.Is(err, ErrSessionUnresolvedToolResults) {
		t.Fatalf("finish error = %v", err)
	}
	if err := observer.finish(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("second finish error = %v, want cancellation identity", err)
	}
	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 {
		t.Fatalf("failure records = %d, want one", len(failures))
	}
	if got := failures[0].Fields[fieldClassification]; got != SessionUnresolvedToolResultClassification {
		t.Fatalf("classification = %q", got)
	}
	if got := failures[0].Fields[fieldUnresolvedToolCallIDs]; got != "call-a, call-z" {
		t.Fatalf("call IDs = %q", got)
	}
	if got := len(sink.events(SessionDiagnosticEventMetrics)); got != 1 {
		t.Fatalf("metrics records = %d, want one", got)
	}
}

func TestSessionCancellation_RecordsTerminalDiagnosticOnce(t *testing.T) {
	sink := &diagnosticRecordSink{}
	intent := NewSessionCancellationIntent()
	intent.MarkSIGINT()
	observer := newSessionProgressObserver(sink, nil, "provider", "model")
	observer.cancellationIntent = intent
	observer.sawSessionOpen = true
	observer.turnsCompleted = 1

	if err := observer.finish(context.Canceled); err != nil {
		t.Fatalf("first finish error = %v", err)
	}
	if err := observer.finish(context.Canceled); err != nil {
		t.Fatalf("second finish error = %v", err)
	}
	terminals := sink.events(SessionDiagnosticEventTerminal)
	if len(terminals) != 1 {
		t.Fatalf("terminal records = %d, want one", len(terminals))
	}
	if got := terminals[0].Fields[fieldTerminalReason]; got != "cancellation" {
		t.Fatalf("terminal reason = %q", got)
	}
	if got := terminals[0].Fields[fieldOutputState]; got != "partial" {
		t.Fatalf("output state = %q", got)
	}
	if got := len(sink.events(SessionDiagnosticEventMetrics)); got != 1 {
		t.Fatalf("metrics records = %d, want one", got)
	}
}

func TestSessionTerminalFinalizationIsConcurrentAndExactlyOnce(t *testing.T) {
	sink := &diagnosticRecordSink{}
	intent := NewSessionCancellationIntent()
	intent.MarkSIGINT()
	observer := newSessionProgressObserver(sink, nil, "provider", "model")
	observer.cancellationIntent = intent

	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := observer.finish(context.Canceled); err != nil {
				t.Errorf("concurrent finish error = %v", err)
			}
		}()
	}
	group.Wait()
	if got := len(sink.events(SessionDiagnosticEventTerminal)); got != 1 {
		t.Fatalf("terminal records = %d, want one", got)
	}
	if got := len(sink.events(SessionDiagnosticEventMetrics)); got != 1 {
		t.Fatalf("metrics records = %d, want one", got)
	}
}
