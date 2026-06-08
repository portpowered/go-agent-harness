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

// fakeSession is a minimal messages.Session for testing.
type fakeSession struct {
	sent    []messages.StreamMessage
	inbound *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		inbound: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

func (s *fakeSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.sent = append(s.sent, msg)
	return true
}

func (s *fakeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inbound
}

func (s *fakeSession) Done() <-chan struct{} {
	return s.done
}

func (s *fakeSession) Close() error {
	close(s.done)
	return nil
}

func TestSessionRecorder_CapturesEventsInOrder(t *testing.T) {
	fake := newFakeSession()
	rec := NewSessionRecorder(fake, WithSessionCaptureProvider("grok", "grok-realtime"))
	ctx := context.Background()

	// Simulate a server-to-client event arriving on the inbound buffer.
	serverMsg := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello from server"),
	}
	fake.inbound.Write(ctx, serverMsg)

	// Poll the recorder's Receive buffer until the relay goroutine forwards the message.
	var got messages.StreamMessage
	var ok bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok = rec.Receive().Read()
		if ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ok {
		t.Fatal("expected to read a message from recorder's Receive buffer")
	}
	delta, ok := got.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("unexpected value type: %T", got.Value)
	}
	if delta.Content != "hello from server" {
		t.Fatalf("unexpected content: %q", delta.Content)
	}

	// Send a client-to-server event.
	clientMsg := messages.StreamMessage{
		Type:  messages.StreamTypeToolCallDelta,
		Value: messages.NewTextDeltaValue("client request"),
	}
	rec.Send(ctx, clientMsg)

	// Verify the inner session received the sent message.
	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(fake.sent))
	}

	// Check recorded events: should be server_to_client first, then client_to_server.
	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Direction != DirectionServerToClient {
		t.Errorf("event[0] direction = %q, want %q", events[0].Direction, DirectionServerToClient)
	}
	if events[0].Sequence != 1 {
		t.Errorf("event[0] sequence = %d, want 1", events[0].Sequence)
	}
	if events[0].Type != string(messages.StreamTypeTextDelta) {
		t.Errorf("event[0] type = %q, want %q", events[0].Type, messages.StreamTypeTextDelta)
	}
	if events[0].PayloadType != SessionPayloadTypeStreamMessage {
		t.Errorf("event[0] payload_type = %q, want %q", events[0].PayloadType, SessionPayloadTypeStreamMessage)
	}
	if len(events[0].Payload) == 0 {
		t.Error("event[0] payload is empty")
	}

	if events[1].Direction != DirectionClientToServer {
		t.Errorf("event[1] direction = %q, want %q", events[1].Direction, DirectionClientToServer)
	}
	if events[1].Sequence != 2 {
		t.Errorf("event[1] sequence = %d, want 2", events[1].Sequence)
	}
	if events[1].Type != string(messages.StreamTypeToolCallDelta) {
		t.Errorf("event[1] type = %q, want %q", events[1].Type, messages.StreamTypeToolCallDelta)
	}

	// Timestamps should be non-negative and non-decreasing.
	if events[0].TimestampMs < 0 {
		t.Errorf("event[0] timestamp_ms = %d, want >= 0", events[0].TimestampMs)
	}
	if events[1].TimestampMs < events[0].TimestampMs {
		t.Errorf("event[1] timestamp_ms (%d) < event[0] (%d)", events[1].TimestampMs, events[0].TimestampMs)
	}

	capture := rec.Capture()
	if capture.Version != SessionCaptureVersion {
		t.Errorf("capture version = %d, want %d", capture.Version, SessionCaptureVersion)
	}
	if capture.Provider.Name != "grok" {
		t.Errorf("capture provider name = %q, want grok", capture.Provider.Name)
	}
	if capture.Provider.Model != "grok-realtime" {
		t.Errorf("capture provider model = %q, want grok-realtime", capture.Provider.Model)
	}
	if capture.Session.StartedAtUTC == "" {
		t.Error("capture session started_at_utc is empty")
	}
}

func TestSessionRecorder_FlushToFile(t *testing.T) {
	fake := newFakeSession()
	rec := NewSessionRecorder(fake, WithSessionCaptureProvider("grok", "grok-realtime"), WithSessionCaptureID("session-123"))
	ctx := context.Background()

	// Record a client-to-server event.
	rec.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("outbound"),
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "capture.session.json")
	if err := rec.FlushToFile(path); err != nil {
		t.Fatalf("FlushToFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var capture SessionCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if capture.Version != SessionCaptureVersion {
		t.Fatalf("version = %d, want %d", capture.Version, SessionCaptureVersion)
	}
	if capture.Provider.Name != "grok" {
		t.Fatalf("provider name = %q, want grok", capture.Provider.Name)
	}
	if capture.Provider.Model != "grok-realtime" {
		t.Fatalf("provider model = %q, want grok-realtime", capture.Provider.Model)
	}
	if capture.Session.ID != "session-123" {
		t.Fatalf("session id = %q, want session-123", capture.Session.ID)
	}
	events := capture.Records
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Direction != DirectionClientToServer {
		t.Errorf("direction = %q, want %q", events[0].Direction, DirectionClientToServer)
	}
	if events[0].Sequence != 1 {
		t.Errorf("sequence = %d, want 1", events[0].Sequence)
	}
	if events[0].PayloadType != SessionPayloadTypeStreamMessage {
		t.Errorf("payload_type = %q, want %q", events[0].PayloadType, SessionPayloadTypeStreamMessage)
	}
	if len(events[0].Payload) == 0 {
		t.Error("payload is empty")
	}
	if string(data) == "" || json.Valid(data) == false {
		t.Error("capture file is not valid JSON")
	}
	if containsAnyJSONKey(data, []string{"authorization", "api_key", "x-api-key"}) {
		t.Error("capture file includes credential-like keys")
	}
}

func TestSessionRecorder_RelayStopsWhenOwnedContextCanceled(t *testing.T) {
	fake := newFakeSession()
	ctx, cancel := context.WithCancel(context.Background())
	rec := NewSessionRecorder(fake, WithSessionRelayContext(ctx))

	fake.inbound.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("before cancel"),
	})
	got, ok := readRecordedMessage(t, rec)
	if !ok {
		t.Fatal("expected first relayed message before cancellation")
	}
	delta, ok := got.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("unexpected first value type: %T", got.Value)
	}
	if delta.Content != "before cancel" {
		t.Fatalf("first relay content = %q, want before cancel", delta.Content)
	}

	cancel()
	fake.inbound.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("dropped after cancel"),
	})

	select {
	case msg := <-rec.Receive().Chan():
		t.Fatalf("unexpected relayed message after cancellation: %s", msg.Type)
	case <-time.After(50 * time.Millisecond):
	}

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected only the pre-cancellation inbound event to be recorded, got %d events", len(events))
	}
	if events[0].Direction != DirectionServerToClient {
		t.Fatalf("event direction = %q, want %q", events[0].Direction, DirectionServerToClient)
	}
}

func readRecordedMessage(t *testing.T, rec *SessionRecorder) (messages.StreamMessage, bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := rec.Receive().Read()
		if ok {
			return got, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return messages.StreamMessage{}, false
}

func containsAnyJSONKey(data []byte, keys []string) bool {
	var walk func(any) bool
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[key] = struct{}{}
	}

	walk = func(v any) bool {
		switch typed := v.(type) {
		case map[string]any:
			for key, value := range typed {
				if _, ok := keySet[key]; ok {
					return true
				}
				if walk(value) {
					return true
				}
			}
		case []any:
			for _, value := range typed {
				if walk(value) {
					return true
				}
			}
		}
		return false
	}

	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return false
	}
	return walk(decoded)
}
