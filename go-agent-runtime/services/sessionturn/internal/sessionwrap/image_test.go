package sessionwrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	describePrompt = "describe these"
	pngType        = "image/png"
	jpegType       = "image/jpeg"
)

func testParts() []messages.ImagePart {
	return []messages.ImagePart{{Bytes: []byte{1, 2}, MediaType: pngType}, {Bytes: []byte{3}, MediaType: jpegType}}
}

func connectImage(t *testing.T, inner messages.Session, deferResponse bool) (messages.Session, *ImageInferencer) {
	t.Helper()
	inferencer := NewImageInferencer(&fixedInferencer{session: inner}, testParts(), deferResponse)
	session, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session, inferencer
}

func firstTurn(t *testing.T, inferencer *ImageInferencer) error {
	t.Helper()
	select {
	case err := <-inferencer.FirstTurn():
		return err
	case <-time.After(waitBound):
		t.Fatal("first image turn was not signaled")
		return nil
	}
}

func TestImageTurnReplacesFirstTextDeltaOnly(t *testing.T) {
	inner := newRichSession()
	session, inferencer := connectImage(t, inner, false)
	ctx := context.Background()
	audioDelta := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1})}
	if !session.Send(ctx, audioDelta) || !session.Send(ctx, textDelta(describePrompt)) || !session.Send(ctx, textDelta("later")) {
		t.Fatal("send rejected")
	}
	if err := firstTurn(t, inferencer); err != nil {
		t.Fatalf("first turn = %v", err)
	}
	if len(inner.messagesSent) != 1 || len(inner.deferred) != 0 {
		t.Fatalf("messages/deferred = %d/%d", len(inner.messagesSent), len(inner.deferred))
	}
	got := inner.messagesSent[0]
	if got.Role != messages.RoleUser || got.TextContent() != describePrompt || len(got.ContentParts) != len(testParts())+1 {
		t.Fatalf("image turn = %#v", got)
	}
	part, ok := got.ContentParts[2].(messages.ImagePart)
	if !ok || part.MediaType != jpegType {
		t.Fatalf("second image part = %#v", got.ContentParts[2])
	}
	if sent := inner.sentSnapshot(); len(sent) != 2 || textOf(sent[1]) != "later" {
		t.Fatalf("stream sends = %#v", sent)
	}
	if inferencer.Unwrap() == nil {
		t.Fatal("Unwrap lost the provider inferencer")
	}
}

func TestImageOnlyTurnHasNoPlaceholderText(t *testing.T) {
	inner := newRichSession()
	session, _ := connectImage(t, inner, false)
	if !session.Send(context.Background(), textDelta(sessionturn.ImageOnlyPrompt)) {
		t.Fatal("image-only send rejected")
	}
	if len(inner.messagesSent) != 1 || inner.messagesSent[0].TextContent() != "" || len(inner.messagesSent[0].ContentParts) != len(testParts()) {
		t.Fatalf("image-only message = %#v", inner.messagesSent)
	}
}

func TestDeferredImageTurnQueuesInstructionWithoutResponse(t *testing.T) {
	inner := newRichSession()
	session, _ := connectImage(t, inner, true)
	if !session.Send(context.Background(), textDelta(sessionturn.ImageOnlyPrompt)) {
		t.Fatal("deferred send rejected")
	}
	if len(inner.messagesSent) != 0 || len(inner.deferred) != 1 || inner.deferred[0].TextContent() != sessionturn.ImageDeferredInstruction {
		t.Fatalf("deferred = %#v, messages=%#v", inner.deferred, inner.messagesSent)
	}
}

func TestImageTurnRejectionsSignalFirstTurn(t *testing.T) {
	streamOnly := newPlainSession()
	session, inferencer := connectImage(t, streamOnly, false)
	if session.Send(context.Background(), textDelta(describePrompt)) {
		t.Fatal("stream-only session accepted an image turn")
	}
	if err := firstTurn(t, inferencer); !errors.Is(err, sessionturn.ErrImageSend) || len(streamOnly.sentSnapshot()) != 0 {
		t.Fatalf("first turn = %v, partial=%d", err, len(streamOnly.sentSnapshot()))
	}
	malformed, malformedInferencer := connectImage(t, newRichSession(), false)
	if malformed.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewErrorValue("bad")}) {
		t.Fatal("malformed text delta accepted")
	}
	if err := firstTurn(t, malformedInferencer); !errors.Is(err, sessionturn.ErrImageSend) {
		t.Fatalf("malformed first turn = %v", err)
	}
	failure := errors.New("dial failed")
	if _, err := NewImageInferencer(&fixedInferencer{err: failure}, nil, false).ConnectSession(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("connect failure = %v", err)
	}
}

func TestSendImageTurnOrdersAfterEarlierTurnAndCopiesParts(t *testing.T) {
	inner := newRichSession()
	ctx := context.Background()
	if !inner.SendMessage(ctx, messages.NewTextMessage(messages.RoleUser, "earlier text turn")) {
		t.Fatal("earlier turn rejected")
	}
	parts := testParts()
	if !SendImageTurn(ctx, inner, describePrompt, parts, true) {
		t.Fatal("image turn rejected")
	}
	parts[0].Bytes[0] = 0
	got := inner.messagesSent[1]
	part, ok := got.ContentParts[1].(messages.ImagePart)
	if len(inner.messagesSent) != 2 || !ok || part.Bytes[0] != 1 {
		t.Fatalf("image turn = %#v", inner.messagesSent)
	}
	if SendImageTurn(ctx, newPlainSession(), "", parts, false) {
		t.Fatal("stream-only session accepted a deferred image turn")
	}
	clone := CloneImageParts(parts)
	clone[0].Bytes[0] = 1
	if parts[0].Bytes[0] != 0 {
		t.Fatal("CloneImageParts shared image bytes")
	}
	var unsignaled imageSession
	unsignaled.signalFirstTurn(true)
}
