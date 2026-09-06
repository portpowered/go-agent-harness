package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestCaptureSnapshotsCannotMutateRetainedEvidence(t *testing.T) {
	stream := NewSessionRecorder(newFakeSession(), WithSessionRelayContext(t.Context()))
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Error(err)
		}
	})
	stream.recordMessage(DirectionClientToServer, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("original")})
	websocket := NewRecordingWebSocketDialer(nil, "fixture", "fixture")
	websocket.recordMessage(DirectionClientToServer, []byte(`{"type":"session.update"}`))
	for name, capture := range map[string]func() SessionCapture{"stream": stream.Capture, "websocket": websocket.Capture} {
		t.Run(name, func(t *testing.T) {
			first := capture()
			original := bytes.Clone(first.Records[0].Payload)
			first.Records[0].Payload[0] = '!'
			second := capture()
			if !bytes.Equal(second.Records[0].Payload, original) || first.Integrity != second.Integrity {
				t.Fatal("snapshot mutation changed retained payload or digest")
			}
		})
	}
	events := stream.Events()
	original := bytes.Clone(events[0].Payload)
	events[0].Payload[0] = '!'
	if !bytes.Equal(stream.Events()[0].Payload, original) {
		t.Fatal("Events exposes retained payload")
	}
	legacy := []CapturedSessionEvent{{Data: []byte(`{"type":"legacy"}`)}}
	cloned := cloneCapturedEvents(legacy)
	cloned[0].Data[0] = '!'
	if legacy[0].Data[0] != '{' {
		t.Fatal("legacy Data aliases original")
	}
}

func TestCaptureClocksUseInjectedDomain(t *testing.T) {
	base := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.FixedZone("test", 3600))
	source := clock.NewDeterministic(base, time.Millisecond)
	websocket := NewRecordingWebSocketDialer(nil, "fixture", "fixture", source)
	stream := NewSessionRecorder(newFakeSession(), WithSessionCaptureClock(source), WithSessionRelayContext(t.Context()))
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Error(err)
		}
	})
	source.AdvanceBy(17 * time.Millisecond)
	websocket.recordMessage(DirectionClientToServer, []byte(`{"type":"session.update"}`))
	if !stream.Send(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("clock")}) {
		t.Fatal("send failed")
	}
	for name, capture := range map[string]SessionCapture{"websocket": websocket.Capture(), "stream": stream.Capture()} {
		if capture.Session.StartedAtUTC != base.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("%s origin = %q", name, capture.Session.StartedAtUTC)
		}
		if len(capture.Records) != 1 || capture.Records[0].TimestampMs != 17 {
			t.Fatalf("%s timestamps = %+v", name, capture.Records)
		}
	}
}

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

	// Block until the relay goroutine forwards the message; the timeout is only
	// a diagnostic safety bound.
	got, ok := readRecordedMessage(t, rec)
	if !ok {
		t.Fatalf("expected to read a message from recorder's Receive buffer within %s", sessionTestSafetyTimeout)
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

func TestMarshalStreamMessage_RoundTripsResponseID(t *testing.T) {
	want := messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-round-trip",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	raw, err := MarshalStreamMessage(want)
	if err != nil {
		t.Fatalf("marshal stream message: %v", err)
	}
	got, err := UnmarshalStreamMessage(raw)
	if err != nil {
		t.Fatalf("unmarshal stream message: %v", err)
	}
	if got.ResponseID != want.ResponseID {
		t.Fatalf("response ID = %q, want %q; raw=%s", got.ResponseID, want.ResponseID, raw)
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
	ctx := newSessionTestContext(t)
	got, err := rec.Receive().ReadContext(ctx)
	if err != nil {
		return messages.StreamMessage{}, false
	}
	return got, true
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

func TestMarshalStreamMessageRetainsInputItemAttribution(t *testing.T) {
	want := messages.StreamMessage{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue("recorded-input-item")}
	raw, err := MarshalStreamMessage(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalStreamMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := got.Value.(*messages.InputItemAddedValue)
	if !ok || value.ItemID != "recorded-input-item" || got.Role != want.Role {
		t.Fatalf("input item attribution lost: %+v", got)
	}
}
