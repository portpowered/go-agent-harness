package integration

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

// TestRecordReplaySession exercises the session replay path by loading a
// .session.json fixture containing realistic bidirectional events (text deltas)
// and verifying that the replayer produces the same server-to-client event
// sequence.
//
// The fixture file contains a synthetic user text event, model text deltas, and
// session close. Client-to-server events are filtered out for this read-only
// fixture rendering check.
func TestRecordReplaySession(t *testing.T) {
	fixturePath := locateFixture(t, "session_text_reply.session.json")
	assertSanitizedSessionFixture(t, fixturePath)

	replayer, err := gwtesting.NewSessionReplayer(fixturePath, gwtesting.WithReplayOutboundValidation(false))
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	// Collect all server-to-client events from the replayer.
	var received []messages.StreamMessage
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-replayer.Done():
			goto drain
		case msg := <-replayer.Receive().Chan():
			received = append(received, msg)
		case <-timeout:
			t.Fatal("timed out waiting for replayer to finish")
		}
	}
drain:
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			break
		}
		received = append(received, msg)
	}

	expectedTypes := []messages.StreamMessageType{
		messages.StreamTypeSessionCreated,
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeSessionClose,
	}

	if len(received) != len(expectedTypes) {
		t.Fatalf("expected %d events, got %d", len(expectedTypes), len(received))
	}

	for i, want := range expectedTypes {
		if received[i].Type != want {
			t.Errorf("event[%d] type = %q, want %q", i, received[i].Type, want)
		}
	}

	// Verify text delta content was deserialized correctly.
	delta1, ok := received[3].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[3] value type = %T, want *TextDeltaValue", received[3].Value)
	}
	if delta1.Content != "Hello! How can I " {
		t.Errorf("event[3] content = %q, want %q", delta1.Content, "Hello! How can I ")
	}

	delta2, ok := received[4].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[4] value type = %T, want *TextDeltaValue", received[4].Value)
	}
	if delta2.Content != "help you today?" {
		t.Errorf("event[4] content = %q, want %q", delta2.Content, "help you today?")
	}

	closeValue, ok := received[7].Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("event[7] value type = %T, want *SessionCloseValue", received[7].Value)
	}
	if closeValue.Reason != "fixture_complete" {
		t.Errorf("session close reason = %q, want fixture_complete", closeValue.Reason)
	}
}

func TestSessionReplayFixture_InboundBeforeOutbound_UnblocksLaterInbound(t *testing.T) {
	fixturePath := locateFixture(t, "session_inbound_then_outbound.session.json")
	assertSanitizedSessionFixture(t, fixturePath)

	replayer, err := gwtesting.NewSessionReplayer(fixturePath)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}
	defer func() {
		_ = replayer.Close()
	}()

	first := readFixtureReplayMessage(t, replayer)
	if first.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("first event type = %q, want %q", first.Type, messages.StreamTypeSessionCreated)
	}

	assertNoFixtureReplayMessage(t, replayer)

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("continue after provider greeting"),
	}) {
		t.Fatalf("expected outbound user event to match fixture: %v", replayer.Err())
	}

	next := readFixtureReplayMessage(t, replayer)
	delta, ok := next.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("next event value type = %T, want *TextDeltaValue", next.Value)
	}
	if delta.Content != "provider response after client event" {
		t.Fatalf("next delta = %q, want provider response after client event", delta.Content)
	}
}

func TestSessionReplayFixture_OutboundBeforeInbound_StartsReplayAfterClientEvent(t *testing.T) {
	fixturePath := locateFixture(t, "session_outbound_then_inbound.session.json")
	assertSanitizedSessionFixture(t, fixturePath)

	replayer, err := gwtesting.NewSessionReplayer(fixturePath)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}
	defer func() {
		_ = replayer.Close()
	}()

	assertNoFixtureReplayMessage(t, replayer)

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("start with client input"),
	}) {
		t.Fatalf("expected initial outbound user event to match fixture: %v", replayer.Err())
	}

	next := readFixtureReplayMessage(t, replayer)
	delta, ok := next.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("next event value type = %T, want *TextDeltaValue", next.Value)
	}
	if delta.Content != "provider response after first client input" {
		t.Fatalf("next delta = %q, want provider response after first client input", delta.Content)
	}
}

func assertSanitizedSessionFixture(t *testing.T, path string) {
	t.Helper()

	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("load session fixture: %v", err)
	}
	if capture.Session.FixtureProvenance == "" {
		t.Fatalf("session fixture %s missing fixture_provenance metadata", path)
	}
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		if record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage {
			t.Fatalf("stream fixture %s should not contain raw websocket client traffic at sequence %d", path, record.Sequence)
		}
	}
}

func readFixtureReplayMessage(t *testing.T, replayer *gwtesting.SessionReplayer) messages.StreamMessage {
	t.Helper()

	select {
	case msg := <-replayer.Receive().Chan():
		return msg
	case <-replayer.Done():
		t.Fatalf("replayer finished before next fixture event: %v", replayer.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fixture replay message")
	}
	return messages.StreamMessage{}
}

func assertNoFixtureReplayMessage(t *testing.T, replayer *gwtesting.SessionReplayer) {
	t.Helper()

	select {
	case msg := <-replayer.Receive().Chan():
		t.Fatalf("received %s before expected outbound fixture event was sent", msg.Type)
	case <-replayer.Done():
		t.Fatalf("replayer finished while waiting for expected outbound fixture event: %v", replayer.Err())
	case <-time.After(50 * time.Millisecond):
	}
}
