package openai

import (
	"encoding/json"
	"strings"
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

func TestRealtimeInboundMessagesResponseDonePreservesFailureStatus(t *testing.T) {
	raw := json.RawMessage(`{"type":"response.done","response":{"status":"failed","status_details":{"reason":"max_output_tokens","error":{"code":"too_many_tokens","message":"response exceeded the model limit"}}}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventResponseDone, Data: raw})
	if len(got) != 1 {
		t.Fatalf("response.done messages = %d, want one", len(got))
	}
	value, ok := got[0].Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		t.Fatalf("response.done value = %#v, want *MessageEndValue", got[0].Value)
	}
	if value.Status != "failed" {
		t.Fatalf("response.done status = %q, want failed", value.Status)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("response.done terminal reason = %q, want terminal_failure", value.TerminalReason)
	}
	if !strings.Contains(value.StatusDetails, "reason=max_output_tokens") || !strings.Contains(value.StatusDetails, "message=response exceeded the model limit") {
		t.Fatalf("response.done details = %q, want bounded reason and message", value.StatusDetails)
	}
}

func TestRealtimeInboundMessagesResponseDonePreservesCompletedStatus(t *testing.T) {
	raw := json.RawMessage(`{"type":"response.done","response":{"status":"completed"}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventResponseDone, Data: raw})
	value, ok := got[0].Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		t.Fatalf("response.done value = %#v, want *MessageEndValue", got[0].Value)
	}
	if value.Status != "completed" || value.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion {
		t.Fatalf("completed response metadata = status %q reason %q", value.Status, value.TerminalReason)
	}
}
