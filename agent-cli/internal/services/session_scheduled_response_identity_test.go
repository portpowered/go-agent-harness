package services

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

	if observer.activeResponse {
		t.Fatal("ToolRunner delivery opened an active provider response")
	}
	if observer.nextScheduledResponse != 1 || len(observer.scheduledResponses) != 1 {
		t.Fatalf("ToolRunner delivery changed scheduled slots: next=%d lifecycles=%d", observer.nextScheduledResponse, len(observer.scheduledResponses))
	}
	if !observer.logicalScheduledResponseSet || observer.logicalScheduledResponseIndex != 0 || observer.logicalScheduledResponseID != providerResponseID {
		t.Fatalf("ToolRunner delivery changed logical owner: set=%t index=%d id=%q", observer.logicalScheduledResponseSet, observer.logicalScheduledResponseIndex, observer.logicalScheduledResponseID)
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
	if !observer.activeResponse || observer.activeResponseID != continuationResponse || !observer.activeScheduledResponseSet || observer.activeScheduledResponseID != continuationResponse {
		t.Fatalf("ToolRunner delivery after continuation start changed owner: response=%t/%q scheduled=%t/%q", observer.activeResponse, observer.activeResponseID, observer.activeScheduledResponseSet, observer.activeScheduledResponseID)
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
	observer.scheduledResponses = append(observer.scheduledResponses, scheduledAudioResponseLifecycle{})

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

	observer.toolStateMu.Lock()
	firstState := observer.toolContinuations[firstCallID]
	firstComplete := firstState != nil && firstState.continuationComplete
	observer.toolStateMu.Unlock()
	if !firstComplete {
		t.Fatal("chained tool response stranded the predecessor continuation")
	}

	observer.noteToolResultAccepted(secondCallID)
	observer.noteToolContinuationRequested()
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: secondContinuationID, Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: secondContinuationID, Value: messages.NewTextDeltaValue("all done")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: secondContinuationID, Value: &messages.MessageEndValue{Type: "message_end", Status: "completed"}})

	observer.toolStateMu.Lock()
	secondState := observer.toolContinuations[secondCallID]
	secondComplete := secondState != nil && secondState.continuationComplete
	observer.toolStateMu.Unlock()
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
	observer.scheduledResponses = []scheduledAudioResponseLifecycle{{bound: true}, {}}
	observer.nextScheduledResponse = 1
	if !observer.bindScheduledResponseID(0, "response-current") || !observer.setActiveScheduledResponseWithID(0, "response-current") {
		t.Fatal("failed to establish current scheduled owner")
	}

	observer.noteScheduledResponseDisposition("response-foreign", scheduledAudioResponseCompleted)
	if observer.completedScheduled != 0 {
		t.Fatalf("foreign response completed %d scheduled lifecycles, want 0", observer.completedScheduled)
	}
	if !observer.activeScheduledResponseSet || observer.activeScheduledResponseID != "response-current" {
		t.Fatalf("foreign response changed active owner: set=%t id=%q", observer.activeScheduledResponseSet, observer.activeScheduledResponseID)
	}
	if observer.scheduledResponses[1].bound {
		t.Fatal("foreign response consumed a later scheduled lifecycle")
	}
}

func TestSessionProgressObserver_LateDispositionCannotClearNewerScheduledOwner(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.scheduledResponses = []scheduledAudioResponseLifecycle{{}}
	if !observer.bindScheduledResponseID(0, "response-old") || !observer.setActiveScheduledResponseWithID(0, "response-old") {
		t.Fatal("failed to establish initial scheduled owner")
	}
	observer.noteScheduledResponseDisposition("response-old", scheduledAudioResponseCancelled)
	if observer.completedScheduled != 1 {
		t.Fatalf("cancelled lifecycle count = %d, want 1", observer.completedScheduled)
	}

	if !observer.bindScheduledResponseID(0, "response-new") || !observer.setActiveScheduledResponseWithID(0, "response-new") {
		t.Fatal("failed to establish replacement scheduled owner")
	}
	observer.noteScheduledResponseDisposition("response-old", scheduledAudioResponseCompleted)
	if !observer.activeScheduledResponseSet || observer.activeScheduledResponseID != "response-new" {
		t.Fatalf("late old disposition cleared replacement owner: set=%t id=%q", observer.activeScheduledResponseSet, observer.activeScheduledResponseID)
	}
	if observer.completedScheduled != 1 {
		t.Fatalf("late old disposition changed completed count to %d, want 1", observer.completedScheduled)
	}

	observer.noteScheduledResponseDisposition("response-new", scheduledAudioResponseCompleted)
	if observer.activeScheduledResponseSet || observer.logicalScheduledResponseSet {
		t.Fatal("current disposition did not clear its own owner")
	}
	if observer.completedScheduled != 1 {
		t.Fatalf("duplicate resolved disposition changed completed count to %d, want 1", observer.completedScheduled)
	}
}
