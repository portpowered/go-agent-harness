package grok

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestTranslateInbound_PreservesResponseID(t *testing.T) {
	tests := []struct {
		name  string
		type_ models.SessionEventType
		raw   string
		want  messages.StreamMessageType
	}{
		{name: "created", type_: models.SessionEventResponseCreated, raw: `{"response":{"id":"resp-created"}}`, want: messages.StreamTypeMessageStart},
		{name: "text", type_: grokSessionEventResponseTextDelta, raw: `{"response_id":"resp-text","delta":"hello"}`, want: messages.StreamTypeTextDelta},
		{name: "done", type_: models.SessionEventResponseDone, raw: `{"response":{"id":"resp-done","status":"completed"}}`, want: messages.StreamTypeMessageEnd},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := translateInbound(models.SessionEvent{Type: test.type_, Data: []byte(test.raw)})
			if len(got) != 1 {
				t.Fatalf("normalized messages = %d, want 1", len(got))
			}
			if got[0].Type != test.want {
				t.Fatalf("normalized type = %q, want %q", got[0].Type, test.want)
			}
			if got[0].ResponseID == "" {
				t.Fatalf("normalized response ID is empty: %#v", got[0])
			}
		})
	}
}

func TestTranslateOutbound_ResponseCancelMapsToProviderEvent(t *testing.T) {
	event, ok := translateOutbound(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	})
	if !ok {
		t.Fatal("RESPONSE.CANCEL was not accepted as an outbound event")
	}
	if event.Type != models.SessionEventResponseCancel {
		t.Fatalf("provider event type = %q, want %q", event.Type, models.SessionEventResponseCancel)
	}
}
