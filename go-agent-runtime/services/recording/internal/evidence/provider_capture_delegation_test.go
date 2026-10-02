package evidence

import (
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Recorded evidence keeps a delegation and its context appends as the values
// the session saw, a null delegation id included.
func TestEvidenceRoundTripsDelegationMessages(t *testing.T) {
	t.Parallel()
	for _, want := range delegationStreamMessages() {
		payload, err := marshalEvidenceStreamMessage(want)
		if err != nil {
			t.Fatalf("marshal %s: %v", want.Type, err)
		}
		got, err := unmarshalEvidenceStreamMessage(payload)
		if err != nil {
			t.Fatalf("unmarshal %s: %v", want.Type, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip of %s = %#v, want %#v", want.Type, got.Value, want.Value)
		}
	}
}

func delegationStreamMessages() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeDelegationCreated, Value: &messages.DelegationCreatedValue{
			Type: "delegation_created", ID: "del_abc123", Target: messages.DelegationTargetClient, OffsetMS: 3600, Task: "book a table",
			Transcript: []messages.TranscriptFragment{{Speaker: messages.RoleUser, Text: "A table for two at seven, please.", StartMS: 1600, EndMS: 3400}},
		}},
		{Type: messages.StreamTypeContextAppend, Value: messages.NewDelegationContextAppendValue(messages.ContextAppendCommentary, "del_abc123", "Booked for seven.")},
		{Type: messages.StreamTypeContextAppend, Value: messages.NewContextAppendValue(messages.ContextAppendInstructions, "Greet the caller.")},
	}
}
