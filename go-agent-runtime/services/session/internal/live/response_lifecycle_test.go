package live

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestAnonymousFiniteResponseTreatsRepeatedStartsAsOneLifecycleOwner(t *testing.T) {
	h := &handle{
		request:           session.LiveRequest{FinishAfterResponse: true},
		captureComplete:   true,
		responseStartWake: make(chan struct{}),
	}
	start := messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	}
	h.observeFiniteResponse(start)
	h.observeFiniteResponse(start)
	if h.anonymousResponses != 1 || !h.responseActive {
		t.Fatalf("anonymous response state after repeated starts = count:%d active:%t, want count:1 active:true", h.anonymousResponses, h.responseActive)
	}
	end := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	if !h.observeFiniteResponse(end) {
		t.Fatal("anonymous response did not finish after its terminal")
	}
	if h.anonymousResponses != 0 || h.responseActive || h.replayResponses != 1 || !h.gracefulStop {
		t.Fatalf("anonymous response final state = count:%d active:%t replay:%d graceful:%t, want 0/false/1/true", h.anonymousResponses, h.responseActive, h.replayResponses, h.gracefulStop)
	}
}
