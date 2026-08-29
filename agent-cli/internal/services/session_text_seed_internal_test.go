package services

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// capturingSeedSession records every message forwarded past the seed
// substitution boundary so tests can assert on what would reach the wire.
type capturingSeedSession struct {
	sent []messages.StreamMessage
}

func (s *capturingSeedSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.sent = append(s.sent, msg)
	return true
}

func (s *capturingSeedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](1)
}

func (s *capturingSeedSession) Done() <-chan struct{} {
	done := make(chan struct{})
	return done
}

func (s *capturingSeedSession) Close() error { return nil }

func TestSessionTextSeedSessionSubstitutesEverySendPath(t *testing.T) {
	const seedValue = "Say hello in one short sentence."
	wirePrompt := nextSessionTextWirePrompt()
	capturing := &capturingSeedSession{}
	session := &sessionTextSeedSession{
		inner:      capturing,
		wirePrompt: wirePrompt,
		value:      seedValue,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](16),
	}
	go session.forwardIncoming()

	ctx := context.Background()
	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(wirePrompt),
	}) {
		t.Fatal("first send rejected")
	}
	followUp := "second runtime message"
	if !session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(followUp),
	}) {
		t.Fatal("second send rejected")
	}

	if len(capturing.sent) != 2 {
		t.Fatalf("forwarded message count = %d, want 2", len(capturing.sent))
	}
	first, ok := capturing.sent[0].Value.(*messages.TextDeltaValue)
	if !ok || first.Content != seedValue {
		t.Fatalf("connect-time prompt = %#v, want %q", capturing.sent[0].Value, seedValue)
	}
	second, ok := capturing.sent[1].Value.(*messages.TextDeltaValue)
	if !ok || second.Content != followUp {
		t.Fatalf("runtime Send text = %#v, want %q", capturing.sent[1].Value, followUp)
	}
	for i, msg := range capturing.sent {
		value, _ := msg.Value.(*messages.TextDeltaValue)
		if value != nil && strings.Contains(value.Content, sessionTextWirePrefix) {
			t.Fatalf("forwarded message %d still carries the wire sentinel: %q", i, value.Content)
		}
	}
}
