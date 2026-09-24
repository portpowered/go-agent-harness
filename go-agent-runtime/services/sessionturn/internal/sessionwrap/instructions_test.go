package sessionwrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const instructionsText = "customer instructions"

func openMessage() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(sessionID, "test")}
}

func createdMessage() messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue(sessionID, "test")}
}

func connectInstructions(t *testing.T, inner messages.Session) messages.Session {
	t.Helper()
	definitions := []messages.ToolDefinition{{Name: "zeta"}, {Name: "alpha"}}
	session, err := NewInstructionsInferencer(&fixedInferencer{session: inner}, instructionsText, definitions).ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil && !strings.Contains(err.Error(), "close") {
			t.Errorf("close: %v", err)
		}
	})
	return session
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(waitBound):
		t.Fatal("relay did not finish")
	}
}

func TestInstructionsConfigureOnceBeforeForwardingOpen(t *testing.T) {
	inner := newRichSession()
	session := connectInstructions(t, inner)
	for _, msg := range []messages.StreamMessage{openMessage(), createdMessage(), textDelta("hello")} {
		inner.recv.Write(context.Background(), msg)
	}
	for _, want := range []messages.StreamMessageType{messages.StreamTypeSessionOpen, messages.StreamTypeSessionCreated, messages.StreamTypeTextDelta} {
		msg, err := readMessage(session)
		if err != nil || msg.Type != want {
			t.Fatalf("relayed = %#v, %v, want %s", msg, err, want)
		}
	}
	sent := inner.sentSnapshot()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("provider sends = %#v, want exactly one update", sent)
	}
	update, ok := sent[0].Value.(*messages.SessionUpdateValue)
	if !ok || update.Instructions != instructionsText || len(update.Tools) != 2 || update.Tools[0].Name != "alpha" {
		t.Fatalf("update = %#v", sent[0].Value)
	}
}

func TestInstructionsSendFailureClosesProviderAndStopsRelay(t *testing.T) {
	inner := newRichSession()
	inner.reject = true
	inner.closeErr = errors.New("close failed")
	session := connectInstructions(t, inner)
	inner.recv.Write(context.Background(), openMessage())
	msg, err := readMessage(session)
	if err != nil || msg.Type != messages.StreamTypeError {
		t.Fatalf("relayed = %#v, %v", msg, err)
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	if !ok || !strings.Contains(value.Message, "send session instructions") || !strings.Contains(value.Message, "close session after instruction failure") {
		t.Fatalf("error = %#v", msg.Value)
	}
	waitDone(t, session.Done())
	if inner.closeCount() == 0 {
		t.Fatal("provider session was not closed after instruction failure")
	}
}

func TestInstructionsForwardSendsAndCapabilities(t *testing.T) {
	inner := newRichSession()
	session := connectInstructions(t, inner)
	ctx := context.Background()
	if !session.Send(ctx, textDelta("direct")) {
		t.Fatal("send rejected")
	}
	sender, ok := session.(messages.SessionSendOutcomeSender)
	if !ok {
		t.Fatal("instructions wrapper hid SendWithOutcome")
	}
	outcome := sender.SendWithOutcome(ctx, textDelta("outcome"))
	if !outcome.OK() || len(inner.sentSnapshot()) != 2 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if complete, deferred := CompleteMessageSupport(session); !complete || !deferred {
		t.Fatal("instructions wrapper hid complete-message capabilities")
	}
}

func TestInstructionsDrainAfterProviderDone(t *testing.T) {
	inner := newPlainSession()
	session := connectInstructions(t, inner)
	inner.recv.Write(context.Background(), textDelta("first"))
	inner.recv.Write(context.Background(), textDelta("second"))
	if err := inner.Close(); err != nil {
		t.Fatalf("close provider: %v", err)
	}
	waitDone(t, session.Done())
	var got []string
	for {
		msg, ok := session.Receive().Read()
		if !ok {
			break
		}
		got = append(got, textOf(msg))
	}
	if strings.Join(got, ",") != "first,second" {
		t.Fatalf("drained = %v", got)
	}
}

func TestInstructionsCloseAndConnectFailure(t *testing.T) {
	inner := newPlainSession()
	session := connectInstructions(t, inner)
	if err := session.Close(); err != nil || inner.closeCount() != 1 {
		t.Fatalf("close = %v", err)
	}
	waitDone(t, session.Done())
	failure := errors.New("dial failed")
	if _, err := NewInstructionsInferencer(&fixedInferencer{err: failure}, "", nil).ConnectSession(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("connect failure = %v", err)
	}
}
