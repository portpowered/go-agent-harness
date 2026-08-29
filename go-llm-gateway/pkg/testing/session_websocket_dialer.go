package testing

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// RecordingWebSocketDialer records bidirectional WebSocket traffic.
type RecordingWebSocketDialer struct {
	inner    transport.Dialer
	startAt  time.Time
	capture  SessionCapture
	events   []CapturedSessionEvent
	sequence int
	mu       sync.Mutex
}

var _ transport.Dialer = (*RecordingWebSocketDialer)(nil)

// NewRecordingWebSocketDialer wraps a live WebSocket dialer and records
// raw JSON messages passing through the returned WebSocket connection.
func NewRecordingWebSocketDialer(inner transport.Dialer, providerName, model string) *RecordingWebSocketDialer {
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
func (d *RecordingWebSocketDialer) Dial(url string, headers map[string]string) (transport.Conn, error) {
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

	events := make([]CapturedSessionEvent, 0, len(d.events))
	for _, event := range d.events {
		// An outbound reservation is made before the wrapped connection is
		// called so a synchronous provider acknowledgement cannot be recorded
		// ahead of the client event that caused it. Failed writes are marked and
		// omitted here, keeping captures representative of accepted traffic.
		if event.PayloadType == "" {
			continue
		}
		events = append(events, event)
	}

	capture := d.capture
	capture.Records = events
	return capture
}

func (d *RecordingWebSocketDialer) recordMessage(dir SessionEventDirection, payload []byte) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sequence++
	sequence := d.sequence
	d.events = append(d.events, CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   dir,
		TimestampMs: time.Since(d.startAt).Milliseconds(),
		Type:        websocketPayloadType(payload),
		PayloadType: SessionPayloadTypeWebSocketMessage,
		Payload:     append([]byte(nil), payload...),
	})
	return sequence
}

func (d *RecordingWebSocketDialer) discardMessage(sequence int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for index := range d.events {
		if d.events[index].Sequence != sequence {
			continue
		}
		d.events[index].PayloadType = ""
		d.events[index].Payload = nil
		return
	}
}

type recordingWebSocketConn struct {
	inner    transport.Conn
	recorder *RecordingWebSocketDialer
}

var _ transport.Conn = (*recordingWebSocketConn)(nil)

func (c *recordingWebSocketConn) ReadMessage() (int, []byte, error) {
	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		c.recorder.recordMessage(DirectionServerToClient, payload)
	}
	return messageType, payload, err
}

func (c *recordingWebSocketConn) WriteMessage(messageType int, payload []byte) error {
	// Reserve the outbound event before invoking the wrapped connection. A
	// hermetic provider may synchronously enqueue a response while processing
	// this write; recording after the call lets that response appear before
	// its causal client event in the capture.
	sequence := c.recorder.recordMessage(DirectionClientToServer, payload)
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		c.recorder.discardMessage(sequence)
		return err
	}
	return nil
}

func (c *recordingWebSocketConn) Close() error {
	return c.inner.Close()
}

// ReplayWebSocketDialer replays raw WebSocket messages from a capture.
type ReplayWebSocketDialer struct {
	capture SessionCapture
	mu      sync.Mutex
	conn    *replayWebSocketConn
	done    chan struct{}
}

var _ transport.Dialer = (*ReplayWebSocketDialer)(nil)

// NewReplayWebSocketDialer loads a raw WebSocket session capture from path.
func NewReplayWebSocketDialer(path string) (*ReplayWebSocketDialer, error) {
	capture, err := LoadSessionCapture(path)
	if err != nil {
		return nil, err
	}
	return NewReplayWebSocketDialerFromCapture(capture)
}

// NewReplayWebSocketDialerFromCapture builds a replay dialer from an already
// decoded capture. Callers that construct an ephemeral capture may use this
// seam after validating the source fixture; unlike the path-based constructor,
// it cannot apply file-level fixture hygiene to data that is not on disk.
func NewReplayWebSocketDialerFromCapture(capture SessionCapture) (*ReplayWebSocketDialer, error) {
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
func (d *ReplayWebSocketDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	events := make([]CapturedSessionEvent, len(d.capture.Records))
	copy(events, d.capture.Records)
	conn := newReplayWebSocketConn(events, d.done, d.capture.EndsWithDisconnect)
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
	events             []CapturedSessionEvent
	index              int
	closed             bool
	endsWithDisconnect bool
	mu                 sync.Mutex
	cond               *sync.Cond
	err                error
	done               chan struct{}
	once               sync.Once
}

var _ transport.Conn = (*replayWebSocketConn)(nil)

func newReplayWebSocketConn(events []CapturedSessionEvent, done chan struct{}, endsWithDisconnect bool) *replayWebSocketConn {
	conn := &replayWebSocketConn{events: events, done: done, endsWithDisconnect: endsWithDisconnect}
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
			if c.endsWithDisconnect {
				c.closeDoneLocked()
				return 0, nil, io.EOF
			}
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
		return c.setErrLocked(newReplayMismatchError("replay completed", websocketPayloadType(payload), fmt.Errorf("unexpected outbound event after replay completed")))
	}
	evt := c.events[c.index]
	if evt.Direction != DirectionClientToServer {
		return c.setErrLocked(newReplayMismatchError(
			fmt.Sprintf("%s event %s at sequence %d", evt.Direction, evt.Type, evt.Sequence),
			websocketPayloadType(payload),
			fmt.Errorf("got outbound before expected capture event"),
		))
	}
	if err := compareReplayPayloads(eventPayload(evt), payload); err != nil {
		return c.setErrLocked(newReplayMismatchError(
			replayEventDescription(evt.Sequence, evt.Type),
			replayEventDescription(evt.Sequence, websocketPayloadType(payload)),
			err,
		))
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
		c.err = newReplayIncompleteError(
			fmt.Sprintf("%s event %s at sequence %d", evt.Direction, evt.Type, evt.Sequence),
			"connection close",
			fmt.Errorf("session replay incomplete"),
		)
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
