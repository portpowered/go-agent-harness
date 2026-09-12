package wire

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestNewServiceConstructsIndependentPublicReducer(t *testing.T) {
	service := NewService(nil)
	service.Observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("wired"),
	}, false, -1, -1)
	entries := service.Entries()
	if len(entries) != 1 || entries[0].Response.Text != "wired" {
		t.Fatalf("entries = %+v", entries)
	}
}
