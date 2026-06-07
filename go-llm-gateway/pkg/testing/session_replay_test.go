package testing

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestSharedCommittedSessionFixtureReplaysDeterministically(t *testing.T) {
	replayer, err := NewSessionReplayer(SharedSessionFixturePath("session_text_reply.session.json"), WithReplayOutboundValidation(false))
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}
	defer replayer.Close()

	var received []messages.StreamMessage
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-replayer.Done():
			goto drain
		case msg := <-replayer.Receive().Chan():
			received = append(received, msg)
		case <-timeout:
			t.Fatal("timed out waiting for shared session fixture replay")
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
		t.Fatalf("received %d events, want %d", len(received), len(expectedTypes))
	}
	for i, want := range expectedTypes {
		if got := received[i].Type; got != want {
			t.Fatalf("event[%d] type = %q, want %q", i, got, want)
		}
	}

	firstDelta, ok := received[3].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[3] value type = %T, want *messages.TextDeltaValue", received[3].Value)
	}
	secondDelta, ok := received[4].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[4] value type = %T, want *messages.TextDeltaValue", received[4].Value)
	}
	if got := firstDelta.Content + secondDelta.Content; got != "Hello! How can I help you today?" {
		t.Fatalf("replayed text = %q", got)
	}
}

func TestSessionReplayer_ProducesServerToClientEvents(t *testing.T) {
	// Build a capture with both client-to-server and server-to-client events.
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeSessionUpdate, &messages.SessionUpdateValue{Type: "session_update"}),
		makeCapture(DirectionServerToClient, 50, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("hello")),
		makeCapture(DirectionServerToClient, 100, messages.StreamTypeTextEnd, messages.NewTextEndValue()),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer, err := NewSessionReplayer(path)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	var received []messages.StreamMessage
	received = append(received, readReplayMessage(t, replayer))

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdate,
		Value: &messages.SessionUpdateValue{Type: "session_update"},
	}) {
		t.Fatalf("Send returned false, expected true: %v", replayer.Err())
	}

	timeout := time.After(2 * time.Second)
	for len(received) < 3 {
		select {
		case msg := <-replayer.Receive().Chan():
			received = append(received, msg)
		case <-replayer.Done():
			for {
				msg, ok := replayer.Receive().Read()
				if !ok {
					break
				}
				received = append(received, msg)
			}
			if len(received) < 3 {
				t.Fatalf("expected 3 events before replay finished, got %d", len(received))
			}
		case <-timeout:
			t.Fatal("timed out waiting for replayer to finish")
		}
	}

	// Should only get the 3 server-to-client events (client-to-server is skipped).
	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d", len(received))
	}

	if received[0].Type != messages.StreamTypeSessionCreated {
		t.Errorf("event[0] type = %q, want %q", received[0].Type, messages.StreamTypeSessionCreated)
	}
	if received[1].Type != messages.StreamTypeTextDelta {
		t.Errorf("event[1] type = %q, want %q", received[1].Type, messages.StreamTypeTextDelta)
	}
	delta, ok := received[1].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[1] value type = %T, want *TextDeltaValue", received[1].Value)
	}
	if delta.Content != "hello" {
		t.Errorf("event[1] content = %q, want %q", delta.Content, "hello")
	}
	if received[2].Type != messages.StreamTypeTextEnd {
		t.Errorf("event[2] type = %q, want %q", received[2].Type, messages.StreamTypeTextEnd)
	}
}

func TestSessionReplayer_SendMatchesExpectedOutboundEvent(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("from client")),
		makeCapture(DirectionServerToClient, 20, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("from server")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer, err := NewSessionReplayer(path)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	first := readReplayMessage(t, replayer)
	if first.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("first replay event type = %q, want %q", first.Type, messages.StreamTypeSessionCreated)
	}

	ok := replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("from client"),
	})
	if !ok {
		t.Fatalf("Send returned false, expected true: %v", replayer.Err())
	}

	next := readReplayMessage(t, replayer)
	delta, ok := next.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("next value type = %T, want *TextDeltaValue", next.Value)
	}
	if delta.Content != "from server" {
		t.Fatalf("next delta = %q, want from server", delta.Content)
	}

	sent := replayer.SentLog()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}
}

func TestSessionReplayer_BlocksLaterInboundUntilExpectedOutbound(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("open the gate")),
		makeCapture(DirectionServerToClient, 20, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("after outbound")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer, err := NewSessionReplayer(path)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	_ = readReplayMessage(t, replayer)
	select {
	case msg := <-replayer.Receive().Chan():
		t.Fatalf("received %s before expected outbound event was sent", msg.Type)
	case <-time.After(50 * time.Millisecond):
	}

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("open the gate"),
	}) {
		t.Fatalf("Send returned false, expected true: %v", replayer.Err())
	}

	msg := readReplayMessage(t, replayer)
	delta, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("value type = %T, want *TextDeltaValue", msg.Value)
	}
	if delta.Content != "after outbound" {
		t.Fatalf("delta = %q, want after outbound", delta.Content)
	}
}

func TestSessionReplayer_FailsOnUnexpectedOutboundEvent(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("expected")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer, err := NewSessionReplayer(path)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	ok := replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("unexpected"),
	})
	if ok {
		t.Fatal("Send returned true for divergent outbound event")
	}
	if replayer.Err() == nil {
		t.Fatal("expected replay divergence error")
	}
}

func TestSessionReplayer_FailsWhenExpectedOutboundIsOmitted(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("required")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer, err := NewSessionReplayer(path)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	_ = readReplayMessage(t, replayer)
	if err := replayer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if replayer.Err() == nil {
		t.Fatal("expected omitted outbound event error")
	}
}

func TestSessionReplayer_SkipTimingDefault(t *testing.T) {
	// Create events with large gaps — they should replay instantly without WithReplayTiming.
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("a")),
		makeCapture(DirectionServerToClient, 5000, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("b")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	start := time.Now()
	replayer, err := NewSessionReplayer(path) // no WithReplayTiming
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	<-replayer.Done()
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("replay took %v, expected near-instant (no timing)", elapsed)
	}
}

func TestSessionReplayer_AcceptsLegacyEventArray(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("legacy")),
	}
	events[0].PayloadType = ""
	events[0].Data = events[0].Payload
	events[0].Payload = nil

	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}

	replayer, err := NewSessionReplayerFromBytes(data)
	if err != nil {
		t.Fatalf("NewSessionReplayerFromBytes: %v", err)
	}
	<-replayer.Done()

	msg, ok := replayer.Receive().Read()
	if !ok {
		t.Fatal("expected one replayed legacy event")
	}
	delta, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("value type = %T, want *TextDeltaValue", msg.Value)
	}
	if delta.Content != "legacy" {
		t.Errorf("content = %q, want legacy", delta.Content)
	}
}

// --- helpers ---

func makeCapture(dir SessionEventDirection, tsMs int64, msgType messages.StreamMessageType, val messages.StreamMessageValue) CapturedSessionEvent {
	msg := messages.StreamMessage{Type: msgType, Value: val}
	data, _ := MarshalStreamMessage(msg)
	return CapturedSessionEvent{
		Direction:   dir,
		TimestampMs: tsMs,
		Type:        string(msgType),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     data,
	}
}

func writeCapture(t *testing.T, path string, events []CapturedSessionEvent) {
	t.Helper()
	for i := range events {
		events[i].Sequence = i + 1
	}
	data, err := json.MarshalIndent(SessionCapture{
		Version:  SessionCaptureVersion,
		Provider: SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
		Session:  SessionMetadata{StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano)},
		Records:  events,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func readReplayMessage(t *testing.T, replayer *SessionReplayer) messages.StreamMessage {
	t.Helper()
	select {
	case msg := <-replayer.Receive().Chan():
		return msg
	case <-replayer.Done():
		t.Fatalf("replayer finished before next message: %v", replayer.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replay message")
	}
	return messages.StreamMessage{}
}
