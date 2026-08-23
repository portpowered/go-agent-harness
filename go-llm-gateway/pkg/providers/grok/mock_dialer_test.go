package grok

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// mockWebSocketConn simulates a Grok realtime WebSocket connection for testing.
// It supports injecting server messages and capturing client messages.
type mockWebSocketConn struct {
	mu sync.Mutex

	// serverMessages are queued messages the mock server will deliver via ReadMessage.
	serverMessages [][]byte
	readIdx        int

	// clientMessages captures messages written by the client via WriteMessage.
	clientMessages [][]byte

	closed    bool
	readBlock chan struct{} // closed to unblock a pending ReadMessage

	// If set, ReadMessage returns this error after all queued messages are delivered.
	readErr error
	// If set, WriteMessage returns this error on the next call.
	writeErr error
}

func newMockConn() *mockWebSocketConn {
	return &mockWebSocketConn{
		readBlock: make(chan struct{}),
	}
}

// addServerMessage queues a raw JSON message that the mock server will deliver.
func (c *mockWebSocketConn) addServerMessage(msg []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serverMessages = append(c.serverMessages, msg)
	// Signal any pending ReadMessage that data is available.
	select {
	case c.readBlock <- struct{}{}:
	default:
	}
}

// addServerEvent queues a typed event for delivery.
func (c *mockWebSocketConn) addServerEvent(eventType string, fields map[string]any) {
	m := map[string]any{"type": eventType}
	for k, v := range fields {
		m[k] = v
	}
	data, _ := json.Marshal(m)
	c.addServerMessage(data)
}

func (c *mockWebSocketConn) ReadMessage() (int, []byte, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return 0, nil, errors.New("connection closed")
		}
		if c.readIdx < len(c.serverMessages) {
			msg := c.serverMessages[c.readIdx]
			c.readIdx++
			c.mu.Unlock()
			return 1, msg, nil // 1 = text message
		}
		if c.readErr != nil {
			err := c.readErr
			c.mu.Unlock()
			return 0, nil, err
		}
		c.mu.Unlock()

		// Block until new data or close.
		<-c.readBlock
	}
}

func (c *mockWebSocketConn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}
	if c.writeErr != nil {
		return c.writeErr
	}
	c.clientMessages = append(c.clientMessages, data)
	return nil
}

func (c *mockWebSocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.readBlock)
	return nil
}

// getClientMessages returns a copy of all captured client messages.
func (c *mockWebSocketConn) getClientMessages() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.clientMessages))
	copy(out, c.clientMessages)
	return out
}

// mockDialer implements transport.Dialer for testing.
type mockDialer struct {
	conn    *mockWebSocketConn
	dialErr error
	// capturedURL and capturedHeaders record the last Dial call.
	capturedURL     string
	capturedHeaders map[string]string
}

func (d *mockDialer) Dial(url string, headers map[string]string) (transport.Conn, error) {
	d.capturedURL = url
	d.capturedHeaders = headers
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	return d.conn, nil
}
