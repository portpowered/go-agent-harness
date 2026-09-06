package agentruntime

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionProgressObserver_SyntheticToolEnvelopeBeforeAndAfterContinuationStart(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		syntheticAfterStart bool
	}{
		{name: "before continuation", syntheticAfterStart: false},
		{name: "after continuation", syntheticAfterStart: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
			observer.setToolResultsEnabled(true)
			observer.scheduledResponses = append(observer.scheduledResponses,
				scheduledAudioResponseLifecycle{}, scheduledAudioResponseLifecycle{})

			driveSyntheticToolEnvelopeTurn(observer, "response-a", "call-a", "response-b", testCase.syntheticAfterStart)
			driveSyntheticToolEnvelopeTurn(observer, "response-c", "call-c", "response-d", false)

			if observer.completedScheduled != 2 {
				t.Fatalf("completed scheduled responses = %d, want 2", observer.completedScheduled)
			}
		})
	}
}

func driveSyntheticToolEnvelopeTurn(observer *sessionProgressObserver, responseID, callID, continuationID string, syntheticAfterStart bool) {
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: responseID, Value: messages.NewMessageStartValue()})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ResponseID: responseID, ToolCallId: callID, Value: messages.NewToolCallStartValue(callID, "bash")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ResponseID: responseID, ToolCallId: callID, Value: messages.NewToolCallEndValue(callID, "bash", "{}")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	observer.noteToolResultAccepted(callID)
	observer.noteToolContinuationRequested()

	emitToolEnvelope := func() {
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, Value: messages.NewMessageStartValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextStartValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextDeltaValue("ok")})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextEndValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
	if !syntheticAfterStart {
		emitToolEnvelope()
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: continuationID, Value: messages.NewMessageStartValue()})
	if syntheticAfterStart {
		emitToolEnvelope()
	}
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: continuationID, Value: messages.NewTranscriptDeltaValue("done with the task")})
	observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: continuationID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func TestSessionProgressObserver_ThreeChainedToolCallsCreditOneScheduledTurn(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime-2.1-mini")
	observer.setToolResultsEnabled(true)
	observer.scheduledResponses = append(observer.scheduledResponses, scheduledAudioResponseLifecycle{})

	emitToolEnvelope := func(callID string) {
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, Value: messages.NewMessageStartValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextStartValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextDeltaValue("ok")})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleTool, ToolCallId: callID, Value: messages.NewTextEndValue()})
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
	emitChainStep := func(responseID, newCallID, narration string) {
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: responseID, Value: messages.NewMessageStartValue()})
		if narration != "" {
			observer.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTranscriptDeltaValue(narration)})
		}
		if newCallID != "" {
			observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ResponseID: responseID, ToolCallId: newCallID, Value: messages.NewToolCallStartValue(newCallID, "webmcp_invoke")})
			observer.observe(messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ResponseID: responseID, ToolCallId: newCallID, Value: messages.NewToolCallEndValue(newCallID, "webmcp_invoke", "{}")})
		}
		observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
		if newCallID != "" {
			observer.noteToolResultAccepted(newCallID)
			observer.noteToolContinuationRequested()
			emitToolEnvelope(newCallID)
		}
	}

	emitChainStep("response-a", "call-a", "checking the page")
	emitChainStep("response-b", "call-b", "")
	emitChainStep("response-c", "call-c", "queueing the moves")
	emitChainStep("response-d", "", "done, the task is complete")

	observer.toolStateMu.Lock()
	defer observer.toolStateMu.Unlock()
	for _, callID := range []string{"call-a", "call-b", "call-c"} {
		state := observer.toolContinuations[callID]
		if state == nil || !state.continuationComplete {
			t.Errorf("continuation for %s was not completed", callID)
		}
	}
	if observer.completedScheduled != 1 {
		t.Errorf("completed scheduled responses = %d, want 1", observer.completedScheduled)
	}
}
