package services

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionProgressObserver_RejectedResultRegistersBeforeCallObservation(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "grok", "grok-realtime")
	observer.setToolResultsEnabled(true)
	const callID = "call-rejected-before-observed"
	outcome := messages.SessionSendOutcome{
		Status: messages.SessionSendBufferFull,
		Err:    errors.New("result queue is full"),
	}

	// The provider result can race ahead of the outer delta consumer. Rejection
	// must create the obligation so terminal diagnostics cannot lose the ID.
	observer.noteToolResultRejected(callID, outcome)
	observer.observeProviderToolCall(messages.NewToolCallEndValue(callID, "slow_tool", "{}"))

	if got := observer.unresolvedToolCallIDs(); len(got) != 1 || got[0] != callID {
		t.Fatalf("unresolved IDs after rejected-before-observed call = %v, want [%s]", got, callID)
	}
	statuses := observer.unresolvedToolResultSendStatuses()
	if statuses[callID] != messages.SessionSendBufferFull {
		t.Fatalf("rejected send status = %q, want %q", statuses[callID], messages.SessionSendBufferFull)
	}

	// A later duplicate rejection must not replace the first observable status,
	// and acceptance of this ID must not affect any other obligation.
	observer.noteToolResultRejected(callID, messages.SessionSendOutcome{Status: messages.SessionSendClosed})
	observer.noteToolResultRejected("call-other", messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure})
	observer.noteToolResultAccepted(callID)

	got := observer.unresolvedToolCallIDs()
	if len(got) != 1 || got[0] != "call-other" {
		t.Fatalf("unresolved IDs after accepting one result = %v, want [call-other]", got)
	}
	if statuses := observer.unresolvedToolResultSendStatuses(); statuses[callID] != "" {
		t.Fatalf("accepted call retained rejection status %q", statuses[callID])
	}
}

func TestSessionProgressObserver_IncompleteResponsePreservesAcceptedResultID(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	const callID = "call-incomplete-response"
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue(callID, "get_weather", `{"city":"Lisbon"}`),
	})
	observer.noteToolResultAccepted(callID)

	err := audioResponseCompletionError(nil, sessionLoopOptions{
		RequireAssistantResponse: true,
		observer:                 observer,
	})
	if !errors.Is(err, ErrSessionAudioResponseIncomplete) {
		t.Fatalf("incomplete response error = %v, want ErrSessionAudioResponseIncomplete", err)
	}
	if got := observer.unresolvedToolCallIDs(); len(got) != 1 || got[0] != callID {
		t.Fatalf("unresolved IDs after incomplete response = %v, want [%s]", got, callID)
	}

	// A provider writer can report queue acceptance after the terminal path has
	// started. That late callback must not clear the preserved obligation.
	observer.noteToolResultAccepted(callID)
	if got := observer.unresolvedToolCallIDs(); len(got) != 1 || got[0] != callID {
		t.Fatalf("late acceptance cleared unresolved ID = %v, want [%s]", got, callID)
	}
}

func TestSessionProgressObserver_ImageContinuationWaitsForTerminalResponse(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	const callID = "call-image-continuation"

	// Provider acceptance may be observed before the outer stream consumer sees
	// the completed function call. The later duplicate call event must attach to
	// the same accepted obligation rather than creating a second one.
	observer.noteToolResultAccepted(callID)
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue(callID, "read_image", `{"path":"screen.png"}`),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	if !observer.hasPendingImageContinuations() {
		t.Fatal("accepted read_image result was complete before its continuation")
	}
	if got := observer.pendingImageContinuationCallIDs(); len(got) != 1 || got[0] != callID {
		t.Fatalf("pending image continuation IDs = %v, want [%s]", got, callID)
	}
	if observer.hasUnresolvedToolCalls() {
		t.Fatal("accepted read_image result remained a generic unresolved result")
	}

	// A non-tool terminal MESSAGE.END is the continuation boundary. Duplicate
	// acceptance and duplicate terminal events must remain idempotent.
	observer.noteToolResultAccepted(callID)
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	if observer.hasPendingImageContinuations() {
		t.Fatal("terminal image continuation remained pending after duplicate lifecycle events")
	}
	if got := observer.pendingImageContinuationCallIDs(); len(got) != 0 {
		t.Fatalf("pending image continuation IDs after terminal response = %v, want none", got)
	}
}

func TestShouldStopSessionLoopWaitsForReadImageResultAndContinuation(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	const callID = "call-read-image"

	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue(callID, "read_image", `{}`),
	})
	providerToolCallEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(providerToolCallEnd)
	if shouldStopSessionLoop(providerToolCallEnd, sessionLoopOptions{observer: observer}, false) {
		t.Fatal("provider read_image MESSAGE.END stopped before the tool result")
	}

	toolRunnerEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(toolRunnerEnd)
	if shouldStopSessionLoop(toolRunnerEnd, sessionLoopOptions{observer: observer}, false) {
		t.Fatal("ToolRunner MESSAGE.END stopped before the model continuation")
	}

	observer.noteToolResultAccepted(callID)
	finalAssistantEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(finalAssistantEnd)
	if !shouldStopSessionLoop(finalAssistantEnd, sessionLoopOptions{observer: observer}, false) {
		t.Fatal("completed read_image continuation did not stop the default session loop")
	}

	// The default stop rule remains unchanged for ordinary tools; only the
	// image continuation has a second provider response to wait for.
	genericObserver := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	genericObserver.setToolResultsEnabled(true)
	genericObserver.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue("call-generic", "lookup", `{}`),
	})
	if !shouldStopSessionLoop(providerToolCallEnd, sessionLoopOptions{observer: genericObserver}, false) {
		t.Fatal("ordinary tool MESSAGE.END changed the default stop rule")
	}
}

func TestSessionProgressObserver_ImageContinuationFailurePreservesPrimaryCause(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	const callID = "call-image-premature-close"
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue(callID, "read_image", `{"path":"missing.png"}`),
	})
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	observer.noteToolResultAccepted(callID)

	primary := errors.New("provider websocket closed")
	err := observer.finish(primary)
	if !errors.Is(err, primary) {
		t.Fatalf("finish error lost primary provider cause: %v", err)
	}
	if !errors.Is(err, ErrSessionImageContinuationIncomplete) {
		t.Fatalf("finish error = %v, want image continuation sentinel", err)
	}
	var continuationErr *SessionImageContinuationError
	if !errors.As(err, &continuationErr) {
		t.Fatalf("finish error = %v, want SessionImageContinuationError", err)
	}
	if len(continuationErr.CallIDs) != 1 || continuationErr.CallIDs[0] != callID {
		t.Fatalf("continuation error call IDs = %v, want [%s]", continuationErr.CallIDs, callID)
	}

	failures := sink.events(SessionDiagnosticEventFailure)
	if len(failures) != 1 {
		t.Fatalf("failure records = %d, want exactly one", len(failures))
	}
	if got := failures[0].Fields[SessionDiagnosticFieldPendingImageContinuationIDs]; got != callID {
		t.Fatalf("pending continuation diagnostic IDs = %q, want %s", got, callID)
	}
	if got := failures[0].Fields[fieldClassification]; got != SessionImageContinuationClassification {
		t.Fatalf("failure classification = %q, want %q", got, SessionImageContinuationClassification)
	}
}
