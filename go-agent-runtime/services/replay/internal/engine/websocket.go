package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	gatewaytransport "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const websocketTextMessage = 1

// OutboundPacer gates self-driving replay output at the next recorded client
// event. The WebSocket connection still validates direction and payload.
type OutboundPacer interface {
	WaitForNextOutbound() error
}

// WebSocketDialer is the replay service's in-memory provider transport. It
// owns the event cursor, payload checks, timing, completion and close result.
type WebSocketDialer struct {
	capture        gatewaytesting.SessionCapture
	preserveTiming bool
	clock          clock.TimerSource
	mu             sync.Mutex
	conn           *websocketConn
	done           chan struct{}
}

var _ gatewaytransport.Dialer = (*WebSocketDialer)(nil)
var _ OutboundPacer = (*WebSocketDialer)(nil)

// ValidateWebSocketCapture checks that the admitted capture contains raw
// provider events suitable for the WebSocket replay contract. Integrity and
// envelope validation occur before this point in the replay service loader.
func ValidateWebSocketCapture(capture gatewaytesting.SessionCapture) error {
	for _, event := range capture.Records {
		if event.Direction != gatewaytesting.DirectionClientToServer && event.Direction != gatewaytesting.DirectionServerToClient {
			return fmt.Errorf("session capture event at sequence %d has invalid WebSocket direction %q", event.Sequence, event.Direction)
		}
		if event.PayloadType != gatewaytesting.SessionPayloadTypeWebSocketMessage {
			return fmt.Errorf("session capture contains %q payload; expected %q", event.PayloadType, gatewaytesting.SessionPayloadTypeWebSocketMessage)
		}
		payload := eventPayload(event)
		if len(payload) == 0 || !json.Valid(payload) {
			return fmt.Errorf("session capture event at sequence %d has invalid WebSocket payload", event.Sequence)
		}
	}
	return nil
}

// NewWebSocketDialer takes an owned capture copy and does not open a network
// connection. preserveTiming applies recorded offsets through the injected
// timer source.
func NewWebSocketDialer(capture gatewaytesting.SessionCapture, preserveTiming bool) (*WebSocketDialer, error) {
	if err := ValidateWebSocketCapture(capture); err != nil {
		return nil, err
	}
	owned := capture
	owned.Records = cloneCaptureEvents(capture.Records)
	return &WebSocketDialer{
		capture:        owned,
		preserveTiming: preserveTiming,
		clock:          clock.Real{},
		done:           make(chan struct{}),
	}, nil
}

func (d *WebSocketDialer) Model() string { return d.capture.Provider.Model }

func (d *WebSocketDialer) WaitForNextOutbound() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return errors.New("replay WebSocket dialer has no active connection")
	}
	return conn.waitForNextOutbound()
}

func (d *WebSocketDialer) Dial(_ string, _ map[string]string) (gatewaytransport.Conn, error) {
	events := cloneCaptureEvents(d.capture.Records)
	conn := newWebSocketConn(events, d.done, d.capture.EndsWithDisconnect, d.preserveTiming, d.clock)
	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()
	return conn, nil
}

func (d *WebSocketDialer) Done() <-chan struct{} { return d.done }

func (d *WebSocketDialer) Err() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Err()
}

func (d *WebSocketDialer) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

type websocketConn struct {
	events             []gatewaytesting.CapturedSessionEvent
	index              int
	closed             bool
	endsWithDisconnect bool
	mu                 sync.Mutex
	cond               *sync.Cond
	err                error
	done               chan struct{}
	once               sync.Once
	preserveTiming     bool
	timingStartedAt    time.Time
	firstTimestampMs   int64
	clock              clock.TimerSource
}

var _ gatewaytransport.Conn = (*websocketConn)(nil)

func newWebSocketConn(events []gatewaytesting.CapturedSessionEvent, done chan struct{}, endsWithDisconnect, preserveTiming bool, source clock.TimerSource) *websocketConn {
	firstTimestamp := int64(0)
	if len(events) > 0 {
		firstTimestamp = events[0].TimestampMs
	}
	conn := &websocketConn{
		events:             events,
		done:               done,
		endsWithDisconnect: endsWithDisconnect,
		preserveTiming:     preserveTiming,
		timingStartedAt:    source.Now(),
		clock:              source,
		firstTimestampMs:   firstTimestamp,
	}
	conn.cond = sync.NewCond(&conn.mu)
	return conn
}

func (c *websocketConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if err, done := c.readTerminalLocked(); done {
			return 0, nil, err
		}
		if c.waitingForServerEventLocked() {
			c.cond.Wait()
			continue
		}
		event, index := c.events[c.index], c.index
		c.mu.Unlock()
		err := c.waitForRecordedTimestamp(event.TimestampMs)
		c.mu.Lock()
		if err != nil {
			return 0, nil, err
		}
		if c.index != index {
			continue
		}
		c.index++
		c.cond.Broadcast()
		return websocketTextMessage, append([]byte(nil), eventPayload(event)...), nil
	}
}

func (c *websocketConn) readTerminalLocked() (error, bool) {
	if c.err != nil {
		return c.err, true
	}
	if c.closed {
		return io.EOF, true
	}
	if c.index >= len(c.events) && c.endsWithDisconnect {
		c.closeDoneLocked()
		return io.EOF, true
	}
	return nil, false
}

func (c *websocketConn) waitingForServerEventLocked() bool {
	if c.index >= len(c.events) {
		return !c.endsWithDisconnect
	}
	return c.events[c.index].Direction != gatewaytesting.DirectionServerToClient
}

func (c *websocketConn) waitForNextOutbound() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.err != nil {
			return c.err
		}
		if c.closed {
			return io.ErrClosedPipe
		}
		if c.index >= len(c.events) {
			return replayMismatch("replay completed", "self-driving outbound", errors.New("unexpected outbound event after replay completed"))
		}
		event := c.events[c.index]
		if event.Direction == gatewaytesting.DirectionClientToServer {
			index := c.index
			c.mu.Unlock()
			err := c.waitForRecordedTimestamp(event.TimestampMs)
			c.mu.Lock()
			if err != nil {
				return err
			}
			if c.index != index {
				continue
			}
			return nil
		}
		c.cond.Wait()
	}
}

func (c *websocketConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	if c.index >= len(c.events) {
		return c.setErrLocked(replayMismatch("replay completed", websocketPayloadType(payload), errors.New("unexpected outbound event after replay completed")))
	}
	event := c.events[c.index]
	if event.Direction != gatewaytesting.DirectionClientToServer {
		return c.setErrLocked(replayMismatch(
			fmt.Sprintf("%s event %s at sequence %d", event.Direction, event.Type, event.Sequence),
			websocketPayloadType(payload),
			errors.New("got outbound before expected capture event"),
		))
	}
	if !jsonPayloadEqual(eventPayload(event), payload) {
		return c.setErrLocked(replayMismatch(
			eventDescription(event.Sequence, event.Type),
			eventDescription(event.Sequence, websocketPayloadType(payload)),
			gateway.NewReplayPayloadDivergenceError("websocket message", "<recorded>", "<sent>"),
		))
	}
	index := c.index
	c.mu.Unlock()
	err := c.waitForRecordedTimestamp(event.TimestampMs)
	c.mu.Lock()
	if err != nil {
		return err
	}
	if c.index != index {
		return c.setErrLocked(replayMismatch(
			"unchanged replay cursor while awaiting recorded timing",
			websocketPayloadType(payload),
			errors.New("replay cursor advanced concurrently"),
		))
	}
	c.index++
	c.cond.Broadcast()
	return nil
}

func (c *websocketConn) waitForRecordedTimestamp(timestamp int64) error {
	if c == nil || !c.preserveTiming {
		return nil
	}
	offset := timestamp - c.firstTimestampMs
	if offset <= 0 {
		return nil
	}
	due := c.timingStartedAt.Add(time.Duration(offset) * time.Millisecond)
	delay := due.Sub(c.clock.Now())
	if delay <= 0 {
		return nil
	}
	timer := c.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-c.done:
		return io.EOF
	}
}

func (c *websocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil && c.index < len(c.events) {
		event := c.events[c.index]
		c.err = replayIncomplete(
			fmt.Sprintf("%s event %s at sequence %d", event.Direction, event.Type, event.Sequence),
			"connection close",
			errors.New("session replay incomplete"),
		)
	}
	c.closed = true
	c.closeDoneLocked()
	c.cond.Broadcast()
	return nil
}

func (c *websocketConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *websocketConn) setErrLocked(err error) error {
	c.err = err
	c.closeDoneLocked()
	c.cond.Broadcast()
	return err
}

func (c *websocketConn) closeDoneLocked() { c.once.Do(func() { close(c.done) }) }

func websocketPayloadType(payload []byte) string {
	var message struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &message); err != nil || message.Type == "" {
		return "unknown"
	}
	return message.Type
}
