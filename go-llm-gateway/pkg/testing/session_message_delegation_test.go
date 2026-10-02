package testing

import (
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// A captured delegation and its context appends replay as the values that
// were recorded, a null delegation id included.
func TestSessionCaptureRoundTripsDelegationMessages(t *testing.T) {
	for _, want := range delegationStreamMessages() {
		payload, err := MarshalStreamMessage(want)
		if err != nil {
			t.Fatalf("MarshalStreamMessage(%s): %v", want.Type, err)
		}
		got, err := UnmarshalStreamMessage(payload)
		if err != nil {
			t.Fatalf("UnmarshalStreamMessage(%s): %v", want.Type, err)
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
