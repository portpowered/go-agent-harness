package services

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionProgressObserver_ToolAcknowledgementDoesNotAdmitOrConsumeScheduledTurn(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	observer.scheduleAudioInputs([]ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1}, EndOfTurn: true}})
	probe := &scheduledInputDispatchProbe{}
	if err := observer.dispatchScheduledInputs(context.Background(), probe); err != nil {
		t.Fatalf("dispatch scheduled input: %v", err)
	}

	for _, event := range []messages.StreamMessage{
		{
			Type:            messages.StreamTypeMessageStart,
			Role:            messages.RoleAssistant,
			ResponseID:      "response-ack",
			ResponsePurpose: messages.ResponsePurposeToolAcknowledgement,
			Value:           messages.NewMessageStartValue(),
		},
		{
			Type:            messages.StreamTypeAudioDelta,
			Role:            messages.RoleAssistant,
			ResponseID:      "response-ack",
			ResponsePurpose: messages.ResponsePurposeToolAcknowledgement,
			Value:           messages.NewAudioDeltaValue([]byte{1, 2}),
		},
		{
			Type:            messages.StreamTypeMessageEnd,
			Role:            messages.RoleAssistant,
			ResponseID:      "response-ack",
			ResponsePurpose: messages.ResponsePurposeToolAcknowledgement,
			Value:           messages.NewMessageEndValue(messages.TokenUsage{}),
		},
	} {
		observer.observe(event)
	}

	if observer.turnsCompleted != 0 || observer.completedScheduled != 0 {
		t.Fatalf("acknowledgement advanced lifecycle: turns=%d completed=%d", observer.turnsCompleted, observer.completedScheduled)
	}
	if observer.nextScheduledResponse != 0 || observer.activeScheduledResponseSet {
		t.Fatalf("acknowledgement consumed scheduled response: next=%d active=%t", observer.nextScheduledResponse, observer.activeScheduledResponseSet)
	}
	if observer.assistantResponseCompleted() {
		t.Fatal("acknowledgement was admitted as the final assistant response")
	}

	for _, event := range []messages.StreamMessage{
		{
			Type:       messages.StreamTypeMessageStart,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewMessageStartValue(),
		},
		{
			Type:       messages.StreamTypeAudioDelta,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewAudioDeltaValue([]byte{3, 4}),
		},
		{
			Type:       messages.StreamTypeMessageEnd,
			Role:       messages.RoleAssistant,
			ResponseID: "response-final",
			Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
		},
	} {
		observer.observe(event)
	}

	if observer.turnsCompleted != 1 || observer.completedScheduled != 1 {
		t.Fatalf("final response lifecycle = turns:%d completed:%d, want one each", observer.turnsCompleted, observer.completedScheduled)
	}
}
