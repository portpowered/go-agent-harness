package testing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
)

func TestReplayWebSocketDialer_ReplaysInboundAndValidatesOutbound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grok.session.json")
	writeWebSocketCapture(t, path, []CapturedSessionEvent{
		websocketCapture(DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"grok-replay"}}`),
		websocketCapture(DirectionServerToClient, 2, `{"type":"session.created","session_id":"sess-1","model":"grok-replay"}`),
	})

	dialer, err := NewReplayWebSocketDialer(path)
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	conn, err := dialer.Dial("wss://live.example.invalid", map[string]string{"Authorization": "Bearer live-key"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if err := conn.WriteMessage(1, []byte(`{"session":{"model":"grok-replay"},"type":"session.update"}`)); err != nil {
		t.Fatalf("WriteMessage should accept semantically equal JSON: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if !strings.Contains(string(payload), "session.created") {
		t.Fatalf("expected replayed session.created payload, got %s", payload)
	}
}

func TestReplayWebSocketDialer_FailsOnUnexpectedOutbound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grok.session.json")
	writeWebSocketCapture(t, path, []CapturedSessionEvent{
		websocketCapture(DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"grok-replay"}}`),
	})

	dialer, err := NewReplayWebSocketDialer(path)
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	err = conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"wrong-model"}}`))
	if err == nil {
		t.Fatal("expected replay divergence error")
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("error should match replay mismatch classification, got %v", err)
	}
	if errors.Is(err, gateway.ErrReplayIncomplete) {
		t.Fatal("replay mismatch should not match replay incomplete classification")
	}
	if errors.Is(err, gateway.ErrTransport) {
		t.Fatal("replay mismatch should not match transport classification")
	}
	if errors.Is(err, gateway.ErrProviderHTTPStatus) {
		t.Fatal("replay mismatch should not match provider HTTP status classification")
	}
	if !errors.Is(err, providers.ErrReplayMismatch) {
		t.Fatalf("error = %v, want ErrReplayMismatch", err)
	}
	select {
	case <-dialer.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("dialer Done did not close after replay divergence")
	}
}

func TestReplayWebSocketDialer_ReadBlocksUntilExpectedOutboundIsWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grok.session.json")
	writeWebSocketCapture(t, path, []CapturedSessionEvent{
		websocketCapture(DirectionServerToClient, 1, `{"type":"session.created","session_id":"sess-1","model":"grok-replay"}`),
		websocketCapture(DirectionClientToServer, 2, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
		websocketCapture(DirectionServerToClient, 3, `{"type":"response.text.delta","delta":"hi"}`),
	})

	dialer, err := NewReplayWebSocketDialer(path)
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage first inbound: %v", err)
	}
	if !strings.Contains(string(payload), "session.created") {
		t.Fatalf("expected first inbound session.created payload, got %s", payload)
	}

	readResult := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			readErr <- err
			return
		}
		readResult <- string(payload)
	}()

	select {
	case got := <-readResult:
		t.Fatalf("ReadMessage returned before expected outbound write: %s", got)
	case err := <-readErr:
		t.Fatalf("ReadMessage errored before expected outbound write: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if err := conn.WriteMessage(1, []byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`)); err != nil {
		t.Fatalf("WriteMessage expected outbound: %v", err)
	}

	select {
	case got := <-readResult:
		if !strings.Contains(got, "response.text.delta") {
			t.Fatalf("expected later inbound response after outbound write, got %s", got)
		}
	case err := <-readErr:
		t.Fatalf("ReadMessage after outbound write: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("ReadMessage did not unblock after expected outbound write")
	}
}

func TestReplayWebSocketDialer_ReplaysInboundEventsBeforeNextExpectedOutbound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openai.session.json")
	writeWebSocketCapture(t, path, []CapturedSessionEvent{
		websocketCapture(DirectionServerToClient, 1, `{"type":"session.created","session_id":"sess-1","model":"gpt-realtime"}`),
		websocketCapture(DirectionServerToClient, 2, `{"type":"session.updated","session_id":"sess-1"}`),
		websocketCapture(DirectionClientToServer, 3, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
	})

	dialer, err := NewReplayWebSocketDialer(path)
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage first inbound: %v", err)
	}
	if !strings.Contains(string(first), "session.created") {
		t.Fatalf("expected session.created before outbound, got %s", first)
	}
	_, second, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage second inbound: %v", err)
	}
	if !strings.Contains(string(second), "session.updated") {
		t.Fatalf("expected session.updated before outbound, got %s", second)
	}
}

func TestReplayWebSocketDialer_ReportsIncompleteExpectedOutboundOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grok.session.json")
	writeWebSocketCapture(t, path, []CapturedSessionEvent{
		websocketCapture(DirectionServerToClient, 1, `{"type":"session.created","session_id":"sess-1","model":"grok-replay"}`),
		websocketCapture(DirectionClientToServer, 2, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
	})

	dialer, err := NewReplayWebSocketDialer(path)
	if err != nil {
		t.Fatalf("NewReplayWebSocketDialer: %v", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("ReadMessage first inbound: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = dialer.Err()
	if err == nil {
		t.Fatalf("expected incomplete replay error, got %v", err)
	}
	if !errors.Is(dialer.Err(), providers.ErrReplayIncomplete) {
		t.Fatalf("dialer error = %v, want ErrReplayIncomplete", dialer.Err())
	}
	if errors.Is(dialer.Err(), providers.ErrReplayMismatch) {
		t.Fatal("incomplete replay should not match replay mismatch classification")
	}
	if got := providers.ErrorClassification(err); got != providers.ErrorClassReplayIncomplete {
		t.Fatalf("dialer error classification = %q, want %q", got, providers.ErrorClassReplayIncomplete)
	}
	if !errors.Is(err, gateway.ErrReplayIncomplete) {
		t.Fatalf("incomplete replay should match replay incomplete classification, got %v", err)
	}
	if errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatal("incomplete replay should not match replay mismatch classification")
	}
	if errors.Is(err, gateway.ErrTransport) {
		t.Fatal("incomplete replay should not match transport classification")
	}
	if errors.Is(err, gateway.ErrProviderHTTPStatus) {
		t.Fatal("incomplete replay should not match provider HTTP status classification")
	}
	var incompleteErr *gateway.ReplayIncompleteError
	if !errors.As(err, &incompleteErr) {
		t.Fatal("incomplete replay should expose typed replay incomplete details")
	}
}

func TestRecordingWebSocketDialer_RecordsInboundAndOutboundWireMessages(t *testing.T) {
	live := &testWebSocketDialer{
		conn: &testWebSocketConn{
			inbound: [][]byte{[]byte(`{"type":"session.created","session_id":"sess-1"}`)},
		},
	}
	dialer := NewRecordingWebSocketDialer(live, "grok", "grok-record")

	conn, err := dialer.Dial("wss://live.example.invalid", map[string]string{"Authorization": "Bearer secret"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"grok-record"}}`)); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	capture := dialer.Capture()
	if capture.Provider.Name != "grok" || capture.Provider.Model != "grok-record" {
		t.Fatalf("provider metadata = %+v", capture.Provider)
	}
	if len(capture.Records) != 2 {
		t.Fatalf("expected two records, got %d", len(capture.Records))
	}
	if capture.Records[0].Direction != DirectionClientToServer {
		t.Fatalf("first record direction = %s", capture.Records[0].Direction)
	}
	if capture.Records[1].Direction != DirectionServerToClient {
		t.Fatalf("second record direction = %s", capture.Records[1].Direction)
	}

	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatalf("capture should not include authorization headers: %s", data)
	}
}

func writeWebSocketCapture(t *testing.T, path string, records []CapturedSessionEvent) {
	t.Helper()
	data, err := json.MarshalIndent(SessionCapture{
		Version: SessionCaptureVersion,
		Provider: SessionProviderMetadata{
			Name:  "grok",
			Model: "grok-replay",
		},
		Session: SessionMetadata{
			StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		},
		Records: records,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
}

func websocketCapture(direction SessionEventDirection, sequence int, payload string) CapturedSessionEvent {
	return CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: int64(sequence),
		Type:        websocketPayloadType([]byte(payload)),
		PayloadType: SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(payload),
	}
}

type testWebSocketDialer struct {
	conn grok.WebSocketConn
}

func (d *testWebSocketDialer) Dial(string, map[string]string) (grok.WebSocketConn, error) {
	return d.conn, nil
}

type testWebSocketConn struct {
	inbound [][]byte
	writes  [][]byte
}

func (c *testWebSocketConn) ReadMessage() (int, []byte, error) {
	if len(c.inbound) == 0 {
		return 0, nil, os.ErrClosed
	}
	next := c.inbound[0]
	c.inbound = c.inbound[1:]
	return 1, next, nil
}

func (c *testWebSocketConn) WriteMessage(_ int, payload []byte) error {
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *testWebSocketConn) Close() error {
	return nil
}
