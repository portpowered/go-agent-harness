package service

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
)

func TestAttachPublishesOneOrderedImageTurn(t *testing.T) {
	data := testPNG(t)
	parts := []messages.ImagePart{{Bytes: data, MediaType: "image/png"}}
	provider := newFakeSession()
	provider.complete = true
	attachment, err := New(nil).Attach(&fakeInferencer{session: provider}, parts, imageinput.TurnOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	wantData := cloneBytes(data)
	parts[0].Bytes = []byte("caller mutation")
	session, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}) {
		t.Fatal("forwarding earlier stream event failed")
	}
	if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("describe")}) {
		t.Fatal("image turn failed")
	}
	if got := len(provider.messagesCopy()); got != 1 {
		t.Fatalf("provider messages = %d, want one", got)
	}
	message := provider.messagesCopy()[0]
	if message.TextContent() != "describe" || len(message.ContentParts) != 2 {
		t.Fatalf("message = %#v, want ordered text and image", message)
	}
	imagePart, ok := message.ContentParts[1].(messages.ImagePart)
	if !ok || string(imagePart.Bytes) != string(wantData) {
		t.Fatalf("image part = %#v, want original immutable bytes", message.ContentParts[1])
	}
	if session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("later")}) == false {
		t.Fatal("later stream event was not forwarded")
	}
	if got := len(provider.messagesCopy()); got != 1 {
		t.Fatalf("provider messages after second delta = %d, want one", got)
	}
	select {
	case result := <-attachment.FirstTurn:
		if result != nil {
			t.Fatalf("first-turn result = %v, want success", result)
		}
	default:
		t.Fatal("first-turn result was not signaled")
	}
	if _, ok := session.(imageinput.ForwardingSession); !ok {
		t.Fatal("attached session did not expose the service forwarding contract")
	}
}

func TestAttachDefersImageOnlyResponse(t *testing.T) {
	provider := newFakeSession()
	provider.complete = true
	provider.without = true
	attachment, err := New(nil).Attach(&fakeInferencer{session: provider}, []messages.ImagePart{{Bytes: testPNG(t), MediaType: "image/png"}}, imageinput.TurnOptions{DeferResponse: true})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	session, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(imageinput.ImageOnlyPrompt)}) {
		t.Fatal("deferred image turn failed")
	}
	message := provider.messagesCopy()[0]
	if message.TextContent() != imageinput.DeferredInstruction {
		t.Fatalf("deferred text = %q, want instruction", message.TextContent())
	}
}

func TestAttachFailureKeepsSendAndCancellationIdentity(t *testing.T) {
	provider := newFakeSession()
	provider.complete = true
	provider.sendOK = false
	attachment, err := New(nil).Attach(&fakeInferencer{session: provider}, []messages.ImagePart{{Bytes: testPNG(t), MediaType: "image/png"}}, imageinput.TurnOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	session, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("cancelled")}) {
		t.Fatal("cancelled send unexpectedly succeeded")
	}
	first := <-attachment.FirstTurn
	if !errors.Is(first, imageinput.ErrSend) || !errors.Is(first, context.Canceled) {
		t.Fatalf("first-turn error = %v, want send and cancellation identities", first)
	}
	select {
	case extra := <-attachment.FirstTurn:
		t.Fatalf("unexpected second first-turn signal: %v", extra)
	default:
	}
}

func TestAttachMalformedFirstDeltaFailsClosedAndSignalsOnce(t *testing.T) {
	provider := newFakeSession()
	provider.complete = true
	attachment, err := New(nil).Attach(&fakeInferencer{session: provider}, []messages.ImagePart{{Bytes: testPNG(t), MediaType: "image/png"}}, imageinput.TurnOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	session, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta}) {
		t.Fatal("malformed first delta unexpectedly succeeded")
	}
	if got := len(provider.messagesCopy()); got != 0 {
		t.Fatalf("provider messages = %d, want no partial image message", got)
	}
	first := <-attachment.FirstTurn
	if !errors.Is(first, imageinput.ErrSend) {
		t.Fatalf("first-turn error = %v, want send identity", first)
	}
	if session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("later")}) == false {
		t.Fatal("later delta was not forwarded after the failed first turn")
	}
	select {
	case extra := <-attachment.FirstTurn:
		t.Fatalf("unexpected second first-turn signal: %v", extra)
	default:
	}
}

func TestAttachShutdownSignalsPendingFirstTurn(t *testing.T) {
	provider := newFakeSession()
	attachment, err := New(nil).Attach(&fakeInferencer{session: provider}, []messages.ImagePart{{Bytes: testPNG(t), MediaType: "image/png"}}, imageinput.TurnOptions{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	session, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case result := <-attachment.FirstTurn:
		if !errors.Is(result, imageinput.ErrSend) {
			t.Fatalf("shutdown result = %v, want send identity", result)
		}
	default:
		t.Fatal("shutdown did not signal pending first turn")
	}
}

func TestSendPreservesProviderErrorIdentity(t *testing.T) {
	providerCause := errors.New("provider queue stopped")
	provider := &errorSession{fakeSession: newFakeSession(), err: providerCause}
	provider.complete = true
	err := New(nil).Send(context.Background(), provider, "describe", []messages.ImagePart{{Bytes: testPNG(t), MediaType: "image/png"}}, imageinput.TurnOptions{})
	if !errors.Is(err, imageinput.ErrSend) || !errors.Is(err, providerCause) {
		t.Fatalf("send error = %v, want send and provider identities", err)
	}
	var typed *imageinput.SendError
	if !errors.As(err, &typed) || typed.Mode != "response" {
		t.Fatalf("send error = %T, want response SendError", err)
	}
}

func TestSendRejectsStreamOnlyWithoutPartialMessage(t *testing.T) {
	provider := &streamOnlySession{fakeSession: newFakeSession()}
	err := New(nil).Send(context.Background(), provider, "describe", []messages.ImagePart{{Bytes: testPNG(t), MediaType: "image/png"}}, imageinput.TurnOptions{})
	if !errors.Is(err, imageinput.ErrSend) {
		t.Fatalf("send error = %v, want send identity", err)
	}
	if len(provider.messagesCopy()) != 0 {
		t.Fatal("stream-only provider received a partial message")
	}
}

type streamOnlySession struct{ *fakeSession }

func (session *streamOnlySession) SupportsCompleteMessages() bool { return false }

func (session *streamOnlySession) SupportsCompleteMessagesWithoutResponse() bool { return false }
