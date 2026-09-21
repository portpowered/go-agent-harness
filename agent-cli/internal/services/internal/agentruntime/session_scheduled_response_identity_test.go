package agentruntime

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionProgressObserver_ToolRoleDeliveryCannotClaimScheduledContinuation(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	probe := &scheduledInputDispatchProbe{}
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}

	const (
		providerResponseID   = "provider-tool-response"
		continuationResponse = "provider-continuation"
		toolResultResponseID = "loop-tool-result"
		callID               = "call-owned-by-first-slot"
	)
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: providerResponseID,
		Value:      messages.NewMessageStartValue(),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ResponseID: providerResponseID,
		ToolCallId: callID,
		Value:      messages.NewToolCallEndValue(callID, "lookup", `{}`),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: providerResponseID,
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	observer.noteToolResultAccepted(callID)
	observer.noteToolContinuationRequested()

	// ToolRunner emits its result through the same stream observer as provider
	// output. Its envelope must remain visible to callers without becoming a
	// response boundary or consuming the next scheduled lifecycle.
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, ResponseID: toolResultResponseID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ResponseID: toolResultResponseID, Value: messages.NewTextDeltaValue("lookup result")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, ResponseID: toolResultResponseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		observer.observe(msg)
	}

	if lifecycleSnapshotForTest(observer).ActiveResponse {
		t.Fatal("ToolRunner delivery opened an active provider response")
	}
	snapshot := lifecycleSnapshotForTest(observer)
	if snapshot.NextScheduledResponse != 1 || len(snapshot.Scheduled) != 1 {
		t.Fatalf("ToolRunner delivery changed scheduled slots: next=%d lifecycles=%d", snapshot.NextScheduledResponse, len(snapshot.Scheduled))
	}
	if !snapshot.LogicalScheduledSet || snapshot.LogicalScheduledIndex != 0 || snapshot.LogicalScheduledID != providerResponseID {
		t.Fatalf("ToolRunner delivery changed logical owner: set=%t index=%d id=%q", snapshot.LogicalScheduledSet, snapshot.LogicalScheduledIndex, snapshot.LogicalScheduledID)
	}

	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		Role:       messages.RoleAssistant,
		ResponseID: continuationResponse,
		Value:      messages.NewMessageStartValue(),
	})
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextDeltaValue("late lookup result")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		observer.observe(msg)
	}
	snapshot = lifecycleSnapshotForTest(observer)
	if !snapshot.ActiveResponse || snapshot.ActiveResponseID != continuationResponse || !snapshot.ActiveScheduledSet || snapshot.ActiveScheduledID != continuationResponse {
		t.Fatalf("ToolRunner delivery after continuation start changed owner: response=%t/%q scheduled=%t/%q", snapshot.ActiveResponse, snapshot.ActiveResponseID, snapshot.ActiveScheduledSet, snapshot.ActiveScheduledID)
	}
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		Role:       messages.RoleAssistant,
		ResponseID: continuationResponse,
		Value:      messages.NewTextDeltaValue("lookup complete"),
	})
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: continuationResponse,
		Value:      &messages.MessageEndValue{Type: "message_end", Status: "completed"},
	})

	if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 1 || dispatched != 1 || scheduled != 1 {
		t.Fatalf("scheduled lifecycle after continuation = %d/%d/%d, want 1/1/1", completed, dispatched, scheduled)
	}
	if observer.hasToolLifecycleObligation() {
		t.Fatal("completed tool continuation remained pending")
	}
}

func TestSessionProgressObserver_ChainedToolContinuationCreditsPredecessor(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0}})
	ensureTestLifecycleScheduled(t, observer, 1)

	const (
		firstResponseID      = "chain-initial"
		firstCallID          = "chain-call-one"
		firstContinuationID  = "chain-continuation-one"
		secondCallID         = "chain-call-two"
		secondContinuationID = "chain-continuation-two"
	)
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: firstResponseID, Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: firstResponseID, ToolCallId: firstCallID, Value: messages.NewToolCallStartValue(firstCallID, "first_tool")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: firstResponseID, ToolCallId: firstCallID, Value: messages.NewToolCallEndValue(firstCallID, "first_tool", `{}`)})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: firstResponseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	observer.noteToolResultAccepted(firstCallID)
	observer.noteToolContinuationRequested()

	// The first continuation emits another tool call. Its terminal boundary is
	// a tool turn for secondCallID, but it is also the terminal continuation for
	// firstCallID and must credit the same scheduled lifecycle.
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: firstContinuationID, Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: firstContinuationID, Value: messages.NewTranscriptDeltaValue("starting the second tool")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: firstContinuationID, ToolCallId: secondCallID, Value: messages.NewToolCallStartValue(secondCallID, "second_tool")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: firstContinuationID, ToolCallId: secondCallID, Value: messages.NewToolCallEndValue(secondCallID, "second_tool", `{}`)})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: firstContinuationID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})

	firstState, firstOK := observer.continuationState(firstCallID)
	firstComplete := firstOK && firstState.ContinuationComplete
	if !firstComplete {
		t.Fatal("chained tool response stranded the predecessor continuation")
	}

	observer.noteToolResultAccepted(secondCallID)
	observer.noteToolContinuationRequested()
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: secondContinuationID, Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: secondContinuationID, Value: messages.NewTextDeltaValue("all done")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: secondContinuationID, Value: &messages.MessageEndValue{Type: "message_end", Status: "completed"}})

	secondState, secondOK := observer.continuationState(secondCallID)
	secondComplete := secondOK && secondState.ContinuationComplete
	if !secondComplete {
		t.Fatal("final chained continuation did not complete")
	}
	if completed, dispatched, scheduled := observer.scheduledAudioCounts(); completed != 1 || dispatched != 0 || scheduled != 1 {
		t.Fatalf("chained scheduled lifecycle = %d/%d/%d, want 1/0/1", completed, dispatched, scheduled)
	}
}

func TestSessionProgressObserver_UnknownScheduledResponseIDCannotFallbackToCurrentOwner(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0}, {AfterCompletedTurns: 1}})
	ensureTestLifecycleScheduled(t, observer, 2)
	if !observer.bindScheduledResponseID(0, "response-current") || !observer.setActiveScheduledResponseWithID(0, "response-current") {
		t.Fatal("failed to establish current scheduled owner")
	}

	observer.noteScheduledResponseDisposition("response-foreign", scheduledAudioResponseCompleted)
	snapshot := lifecycleSnapshotForTest(observer)
	if snapshot.CompletedScheduled != 0 {
		t.Fatalf("foreign response completed %d scheduled lifecycles, want 0", snapshot.CompletedScheduled)
	}
	if !snapshot.ActiveScheduledSet || snapshot.ActiveScheduledID != "response-current" {
		t.Fatalf("foreign response changed active owner: set=%t id=%q", snapshot.ActiveScheduledSet, snapshot.ActiveScheduledID)
	}
	if snapshot.Scheduled[1].Bound {
		t.Fatal("foreign response consumed a later scheduled lifecycle")
	}
}

func TestSessionProgressObserver_LateDispositionCannotClearNewerScheduledOwner(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	ensureTestLifecycleScheduled(t, observer, 2)
	if !observer.bindScheduledResponseID(0, "response-old") || !observer.setActiveScheduledResponseWithID(0, "response-old") {
		t.Fatal("failed to establish initial scheduled owner")
	}
	observer.noteScheduledResponseDisposition("response-old", scheduledAudioResponseCancelled)
	if got := lifecycleSnapshotForTest(observer).CompletedScheduled; got != 1 {
		t.Fatalf("cancelled lifecycle count = %d, want 1", got)
	}

	if !observer.bindScheduledResponseID(1, "response-new") || !observer.setActiveScheduledResponseWithID(1, "response-new") {
		t.Fatal("failed to establish replacement scheduled owner")
	}
	observer.noteScheduledResponseDisposition("response-old", scheduledAudioResponseCompleted)
	snapshot := lifecycleSnapshotForTest(observer)
	if !snapshot.ActiveScheduledSet || snapshot.ActiveScheduledID != "response-new" {
		t.Fatalf("late old disposition cleared replacement owner: set=%t id=%q", snapshot.ActiveScheduledSet, snapshot.ActiveScheduledID)
	}
	if snapshot.CompletedScheduled != 1 {
		t.Fatalf("late old disposition changed completed count to %d, want 1", snapshot.CompletedScheduled)
	}

	observer.noteScheduledResponseDisposition("response-new", scheduledAudioResponseCompleted)
	snapshot = lifecycleSnapshotForTest(observer)
	if snapshot.ActiveScheduledSet || snapshot.LogicalScheduledSet {
		t.Fatal("current disposition did not clear its own owner")
	}
	if snapshot.CompletedScheduled != 2 {
		t.Fatalf("duplicate resolved disposition changed completed count to %d, want 2", snapshot.CompletedScheduled)
	}
}

func TestSessionProgressObserver_SessionOpenResetsReducerBeforeUntaggedResponse(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: "response-old", Value: messages.NewMessageStartValue()})
	snapshot := lifecycleSnapshotForTest(observer)
	if !snapshot.ActiveResponse || snapshot.ActiveResponseID != "response-old" {
		t.Fatalf("initial response owner = active=%t id=%q, want response-old", snapshot.ActiveResponse, snapshot.ActiveResponseID)
	}

	observer.observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session-new", "openai")})
	if snapshot := observer.lifecycle.Snapshot(); snapshot.ActiveResponse || len(snapshot.CompletedResponseIDs) != 0 || len(snapshot.RetiredResponseIDs) != 0 {
		t.Fatalf("SESSION.OPEN left prior reducer state: %+v", snapshot)
	}

	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()})
	snapshot = lifecycleSnapshotForTest(observer)
	if !snapshot.ActiveResponse || snapshot.ActiveResponseID != "" {
		t.Fatalf("untagged response owner = active=%t id=%q, want a fresh untagged response", snapshot.ActiveResponse, snapshot.ActiveResponseID)
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("fresh response")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	if observer.turnsCompleted != 1 {
		t.Fatalf("fresh untagged response completed turns = %d, want 1", observer.turnsCompleted)
	}
}
