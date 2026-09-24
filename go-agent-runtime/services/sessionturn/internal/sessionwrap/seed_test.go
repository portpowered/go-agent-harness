package sessionwrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const seedValue = "Say hello in one short sentence."

func TestWirePromptsAreUniqueSentinels(t *testing.T) {
	var prompts WirePrompts
	first, second := prompts.Next(), prompts.Next()
	if first == second || !strings.HasPrefix(first, sessionturn.TextSeedWirePrefix) || !strings.HasPrefix(second, sessionturn.TextSeedWirePrefix) {
		t.Fatalf("wire prompts = %q, %q", first, second)
	}
}

func TestSeedSessionSubstitutesOnlyTheFirstSentinel(t *testing.T) {
	var prompts WirePrompts
	wirePrompt := prompts.Next()
	inner := newPlainSession()
	session := NewSeedSession(inner, wirePrompt, seedValue)
	ctx := context.Background()
	sends := []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1})},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewErrorValue("not text")},
		textDelta(wirePrompt), textDelta("second runtime message"), textDelta(wirePrompt),
	}
	for _, msg := range sends {
		if !session.Send(ctx, msg) {
			t.Fatal("send rejected")
		}
	}
	sent := inner.sentSnapshot()
	if len(sent) != len(sends) || textOf(sent[2]) != seedValue || textOf(sent[3]) != "second runtime message" || textOf(sent[4]) != wirePrompt {
		t.Fatalf("forwarded = %#v", sent)
	}
}

func TestSeedInferencerRelaysIncomingAndForwardsLifecycle(t *testing.T) {
	inner := newRichSession()
	inferencer := NewSeedInferencer(&fixedInferencer{session: inner}, "sentinel", seedValue)
	session, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	inner.recv.Write(context.Background(), textDelta("from provider"))
	msg, err := readMessage(session)
	if err != nil || textOf(msg) != "from provider" {
		t.Fatalf("relayed = %#v, %v", msg, err)
	}
	if session.Done() != inner.Done() {
		t.Fatal("seed session did not expose the provider done channel")
	}
	if err := session.Close(); err != nil || inner.closeCount() != 1 {
		t.Fatalf("close = %v, closes=%d", err, inner.closeCount())
	}
	failure := errors.New("dial failed")
	if _, err := NewSeedInferencer(&fixedInferencer{err: failure}, "", "").ConnectSession(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("connect failure = %v", err)
	}
}

func TestSeedRelayClosesProviderWhenRelayRejects(t *testing.T) {
	inner := newPlainSession()
	session := NewSeedSession(inner, "sentinel", seedValue)
	session.receive = messages.NewTypedBuffer[messages.StreamMessage](1)
	session.receive.Write(context.Background(), textDelta("full"))
	inner.recv.Write(context.Background(), textDelta("dropped"))
	session.ForwardIncoming(context.Background())
	if inner.closeCount() != 1 {
		t.Fatalf("provider closes = %d, want one after relay rejection", inner.closeCount())
	}
}

func TestForwarderCapabilities(t *testing.T) {
	rich := newRichSession()
	rich.terminal = errors.New("provider failed")
	wrapped := forwarder{inner: rich}
	ctx := context.Background()
	if !wrapped.SendMessage(ctx, messages.Message{}) || !wrapped.SendMessageWithoutResponse(ctx, messages.Message{}) || !wrapped.RequestResponse(ctx).OK() {
		t.Fatal("rich capabilities were not forwarded")
	}
	if !wrapped.SupportsResponseRequests() || !wrapped.SupportsCompleteMessages() || !wrapped.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("rich capability reports were not forwarded")
	}
	wrapped.RTCMedia()
	if !errors.Is(wrapped.TerminalError(), rich.terminal) || rich.mediaRequests != 1 {
		t.Fatal("terminal error or media were not forwarded")
	}
	plain := forwarder{inner: newPlainSession()}
	if plain.SendMessage(ctx, messages.Message{}) || plain.SendMessageWithoutResponse(ctx, messages.Message{}) || plain.RequestResponse(ctx).OK() || plain.TerminalError() != nil {
		t.Fatal("stream-only session gained capabilities")
	}
	if plain.SupportsResponseRequests() || plain.SupportsCompleteMessages() || plain.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("stream-only session reported capabilities")
	}
	plain.RTCMedia()
}

func TestCompleteMessageSupportPrefersDeclaredCapabilities(t *testing.T) {
	wrapped := &SeedSession{forwarder: forwarder{inner: newPlainSession()}}
	if complete, deferred := CompleteMessageSupport(wrapped); complete || deferred {
		t.Fatal("wrapper methods were mistaken for provider capabilities")
	}
	if complete, deferred := CompleteMessageSupport(newRichSession()); !complete || !deferred {
		t.Fatal("provider capabilities were not detected")
	}
}

func TestOutputRetainsFirstFailure(t *testing.T) {
	var buffer bytes.Buffer
	output := NewOutput(&buffer)
	if n, err := output.Write([]byte("ok")); n != 2 || err != nil || output.Err() != nil {
		t.Fatalf("write = %d, %v", n, err)
	}
	short := NewOutput(shortWriter{})
	if _, err := short.Write([]byte("four")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write = %v", err)
	}
	if n, err := short.Write([]byte("more")); n != 0 || !errors.Is(err, io.ErrShortWrite) || !errors.Is(short.Err(), io.ErrShortWrite) {
		t.Fatalf("sticky failure = %d, %v", n, err)
	}
}
