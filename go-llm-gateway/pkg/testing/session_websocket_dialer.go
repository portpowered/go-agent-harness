package testing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-llm-gateway/pkg/providers/grok"
)

// RecordingWebSocketDialer records bidirectional Grok WebSocket traffic.
type RecordingWebSocketDialer struct {
	inner    grok.WebSocketDialer
	startAt  time.Time
	capture  SessionCapture
	events   []CapturedSessionEvent
	sequence int
	mu       sync.Mutex
}

var _ grok.WebSocketDialer = (*RecordingWebSocketDialer)(nil)

// NewRecordingWebSocketDialer wraps a live Grok WebSocket dialer and records
// raw JSON messages passing through the returned WebSocket connection.
func NewRecordingWebSocketDialer(inner grok.WebSocketDialer, providerName, model string) *RecordingWebSocketDialer {
	startAt := time.Now().UTC()
	d := &RecordingWebSocketDialer{
		inner:   inner,
		startAt: startAt,
		events:  make([]CapturedSessionEvent, 0),
		capture: SessionCapture{
			Version: SessionCaptureVersion,
			Provider: SessionProviderMetadata{
				Name:  providerName,
				Model: model,
			},
			Session: SessionMetadata{
				StartedAtUTC: startAt.Format(time.RFC3339Nano),
			},
			Records: make([]CapturedSessionEvent, 0),
		},
	}
	return d
}

// Dial connects through the wrapped dialer and returns a recording connection.
func (d *RecordingWebSocketDialer) Dial(url string, headers map[string]string) (grok.WebSocketConn, error) {
	if d.inner == nil {
		return nil, fmt.Errorf("recording websocket dialer requires an inner dialer")
	}
	conn, err := d.inner.Dial(url, headers)
	if err != nil {
		return nil, err
	}
	return &recordingWebSocketConn{inner: conn, recorder: d}, nil
}

// FlushToFile writes the recorded WebSocket traffic to path.
func (d *RecordingWebSocketDialer) FlushToFile(path string) error {
	capture := d.Capture()
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session captures: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write session capture file: %w", err)
	}
	return nil
}

// Capture returns a copy of the current capture envelope.
func (d *RecordingWebSocketDialer) Capture() SessionCapture {
	d.mu.Lock()
	defer d.mu.Unlock()

	events := make([]CapturedSessionEvent, len(d.events))
	copy(events, d.events)

	capture := d.capture
	capture.Records = events
	return capture
}

func (d *RecordingWebSocketDialer) recordMessage(dir SessionEventDirection, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sequence++
	d.events = append(d.events, CapturedSessionEvent{
		Sequence:    d.sequence,
		Direction:   dir,
		TimestampMs: time.Since(d.startAt).Milliseconds(),
		Type:        websocketPayloadType(payload),
		PayloadType: SessionPayloadTypeWebSocketMessage,
		Payload:     append([]byte(nil), payload...),
	})
}

type recordingWebSocketConn struct {
	inner    grok.WebSocketConn
	recorder *RecordingWebSocketDialer
}

var _ grok.WebSocketConn = (*recordingWebSocketConn)(nil)

func (c *recordingWebSocketConn) ReadMessage() (int, []byte, error) {
	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		c.recorder.recordMessage(DirectionServerToClient, payload)
	}
	return messageType, payload, err
}

func (c *recordingWebSocketConn) WriteMessage(messageType int, payload []byte) error {
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		return err
	}
	c.recorder.recordMessage(DirectionClientToServer, payload)
	return nil
}

func (c *recordingWebSocketConn) Close() error {
	return c.inner.Close()
}

// ReplayWebSocketDialer replays raw Grok WebSocket messages from a capture.
type ReplayWebSocketDialer struct {
	capture SessionCapture
	mu      sync.Mutex
	conn    *replayWebSocketConn
	done    chan struct{}
}

var _ grok.WebSocketDialer = (*ReplayWebSocketDialer)(nil)

// NewReplayWebSocketDialer loads a raw WebSocket session capture from path.
func NewReplayWebSocketDialer(path string) (*ReplayWebSocketDialer, error) {
	capture, err := LoadSessionCapture(path)
	if err != nil {
		return nil, err
	}
	for _, evt := range capture.Records {
		if evt.PayloadType != SessionPayloadTypeWebSocketMessage {
			return nil, fmt.Errorf("session capture contains %q payload; expected %q", evt.PayloadType, SessionPayloadTypeWebSocketMessage)
		}
	}
	return &ReplayWebSocketDialer{capture: capture, done: make(chan struct{})}, nil
}

// Model returns the model metadata from the replay capture, if present.
func (d *ReplayWebSocketDialer) Model() string {
	return d.capture.Provider.Model
}

// Dial returns an in-memory replay connection and never opens a live network connection.
func (d *ReplayWebSocketDialer) Dial(_ string, _ map[string]string) (grok.WebSocketConn, error) {
	events := make([]CapturedSessionEvent, len(d.capture.Records))
	copy(events, d.capture.Records)
	conn := newReplayWebSocketConn(events, d.done)
	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()
	return conn, nil
}

// Done is closed when the active replay connection closes or detects divergence.
func (d *ReplayWebSocketDialer) Done() <-chan struct{} {
	return d.done
}

// Err returns the replay divergence or incompletion error for the active connection.
func (d *ReplayWebSocketDialer) Err() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Err()
}

type replayWebSocketConn struct {
	events []CapturedSessionEvent
	index  int
	closed bool
	mu     sync.Mutex
	cond   *sync.Cond
	err    error
	done   chan struct{}
	once   sync.Once
}

var _ grok.WebSocketConn = (*replayWebSocketConn)(nil)

func newReplayWebSocketConn(events []CapturedSessionEvent, done chan struct{}) *replayWebSocketConn {
	conn := &replayWebSocketConn{events: events, done: done}
	conn.cond = sync.NewCond(&conn.mu)
	return conn
}

func (c *replayWebSocketConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		if c.err != nil {
			return 0, nil, c.err
		}
		if c.closed {
			return 0, nil, io.EOF
		}
		if c.index >= len(c.events) {
			c.cond.Wait()
			continue
		}
		evt := c.events[c.index]
		if evt.Direction == DirectionServerToClient {
			c.index++
			c.cond.Broadcast()
			return 1, eventPayload(evt), nil
		}
		c.cond.Wait()
	}
}

func (c *replayWebSocketConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return io.ErrClosedPipe
	}
	if c.index >= len(c.events) {
		return c.setErrLocked(fmt.Errorf("session replay divergence: unexpected outbound event %s after replay completed", websocketPayloadType(payload)))
	}
	evt := c.events[c.index]
	if evt.Direction != DirectionClientToServer {
		return c.setErrLocked(fmt.Errorf("session replay divergence at sequence %d: got outbound %s before expected %s event %s", evt.Sequence, websocketPayloadType(payload), evt.Direction, evt.Type))
	}
	if !rawJSONEqual(eventPayload(evt), payload) {
		return c.setErrLocked(fmt.Errorf("session replay divergence at sequence %d: expected outbound payload for %s does not match actual outbound event %s", evt.Sequence, evt.Type, websocketPayloadType(payload)))
	}
	c.index++
	c.cond.Broadcast()
	return nil
}

func (c *replayWebSocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err == nil && c.index < len(c.events) {
		evt := c.events[c.index]
		c.err = fmt.Errorf("session replay incomplete at sequence %d: expected %s event %s", evt.Sequence, evt.Direction, evt.Type)
	}
	c.closed = true
	c.closeDoneLocked()
	c.cond.Broadcast()
	return nil
}

func (c *replayWebSocketConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *replayWebSocketConn) setErrLocked(err error) error {
	c.err = err
	c.closeDoneLocked()
	c.cond.Broadcast()
	return err
}

func (c *replayWebSocketConn) closeDoneLocked() {
	c.once.Do(func() {
		close(c.done)
	})
}

// LoadSessionCapture reads a versioned session capture or a legacy event array.
func LoadSessionCapture(path string) (SessionCapture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionCapture{}, fmt.Errorf("read session capture file: %w", err)
	}

	var capture SessionCapture
	if err := json.Unmarshal(data, &capture); err == nil && capture.Version != 0 {
		return capture, nil
	}

	var events []CapturedSessionEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return SessionCapture{}, fmt.Errorf("parse session capture: %w", err)
	}
	return SessionCapture{
		Version: SessionCaptureVersion,
		Records: events,
	}, nil
}

func eventPayload(evt CapturedSessionEvent) []byte {
	if len(evt.Payload) > 0 {
		return evt.Payload
	}
	return evt.Data
}

func websocketPayloadType(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type == "" {
		return "websocket.message"
	}
	return envelope.Type
}

func rawJSONEqual(a, b []byte) bool {
	var av any
	var bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return bytes.Equal(a, b)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return bytes.Equal(a, b)
	}
	aj, err := json.Marshal(av)
	if err != nil {
		return bytes.Equal(a, b)
	}
	bj, err := json.Marshal(bv)
	if err != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(aj, bj)
}
