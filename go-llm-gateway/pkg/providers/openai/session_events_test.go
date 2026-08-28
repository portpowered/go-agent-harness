package openai

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestRealtimeOutboundEvents_ResponseCancelMapsToProviderEvent(t *testing.T) {
	events, ok := realtimeOutboundEvents(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	})
	if !ok {
		t.Fatal("RESPONSE.CANCEL was not accepted as an outbound event")
	}
	if len(events) != 1 {
		t.Fatalf("provider event count = %d, want 1", len(events))
	}
	if events[0].Type != models.SessionEventResponseCancel {
		t.Fatalf("provider event type = %q, want %q", events[0].Type, models.SessionEventResponseCancel)
	}
}
