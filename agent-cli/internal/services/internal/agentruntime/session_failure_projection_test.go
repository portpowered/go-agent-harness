package agentruntime

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

func TestSessionFailureProjection_AdapterPublishesBeforeReentrantCallback(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "provider", "model")
	original := errors.New("provider cause")
	var callbackSnapshot *failureFacts
	var callbackError error
	observer.terminalObserver = func(observation sessionTerminalObservation) bool {
		callbackSnapshot = observer.failureSnapshot()
		callbackError = observation.Err
		return true
	}

	observer.captureFailureFromError(&messages.ErrorValue{
		Message:        "provider failure",
		Err:            original,
		Classification: "transport",
	})

	if callbackSnapshot == nil || callbackSnapshot.classification != "transport" {
		t.Fatalf("reentrant callback snapshot = %#v, want accepted transport facts", callbackSnapshot)
	}
	if !errors.Is(callbackError, original) {
		t.Fatalf("callback error = %v, want original provider cause", callbackError)
	}
	if got := observer.failureSnapshot(); got == nil || got.failingEvent != string(messages.StreamTypeError) {
		t.Fatalf("accepted adapter snapshot = %#v", got)
	}
}

func TestSessionFailureProjection_AdapterRejectedPublicationRollsBack(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "provider", "model")
	allow := false
	observer.terminalObserver = func(sessionTerminalObservation) bool { return allow }
	observer.captureFailureFromError(&messages.ErrorValue{Message: "rejected", Classification: "transport"})
	if got := observer.failureSnapshot(); got != nil {
		t.Fatalf("rejected publication retained adapter state: %#v", got)
	}

	allow = true
	observer.captureFailureFromError(&messages.ErrorValue{Message: "accepted", Classification: "transport"})
	if got := observer.failureSnapshot(); got == nil {
		t.Fatal("accepted publication did not retain adapter state")
	}
	observer.clearFailure()
	if got := observer.failureSnapshot(); got != nil {
		t.Fatalf("clear retained adapter state: %#v", got)
	}
}

func TestSessionFailureProjection_AdapterExcludesCancellationAndCleanClose(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "provider", "model")
	for _, value := range []*messages.ErrorValue{
		{Classification: sf.ErrorClassCancellation},
		{Classification: sf.ErrorClassRoomBoundCancelled},
		{TerminalReason: messages.TerminalReasonCancellation},
		messages.NewNonTerminalErrorValue("notice", "response_cancel_not_active"),
	} {
		observer.captureFailureFromError(value)
	}
	observer.captureFailureFromClose(&messages.SessionCloseValue{
		Reason:         "client_close",
		TerminalReason: messages.TerminalReasonSessionClose,
	})
	if got := observer.failureSnapshot(); got != nil {
		t.Fatalf("excluded terminal values became failure facts: %#v", got)
	}
}

func TestSessionFailureProjection_AdapterCloseProjectionAndToolDiagnostic(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "provider", "model")
	observer.sawSessionOpen = true
	observer.turnsCompleted = 2
	observer.captureFailureFromClose(&messages.SessionCloseValue{
		Reason:         "provider_closed",
		TerminalReason: messages.TerminalReasonProviderClose,
	})
	failure := observer.failureSnapshot()
	if failure == nil || failure.outputState != string(messages.TerminalOutputPartial) || failure.provenance != string(messages.TerminalProvenanceSession) {
		t.Fatalf("provider close projection = %#v, want session partial failure", failure)
	}

	observer.emitToolCallRecord(&messages.ToolCallEndValue{Name: "lookup", ToolCallID: "call-1"})
	records := sink.events(SessionDiagnosticEventToolCall)
	if len(records) != 1 || records[0].Fields[fieldToolName] != "lookup" || records[0].Fields[fieldToolCallID] != "call-1" {
		t.Fatalf("unsupported tool diagnostic = %#v", records)
	}
}
