package strict

import (
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type replayState struct {
	mu           sync.Mutex
	consumed     int
	dialed       bool
	messageTypes []int
}

func (s *replayState) expectedTypeLocked() int {
	if s.consumed >= len(s.messageTypes) {
		return 0
	}
	return s.messageTypes[s.consumed]
}

type trackingDialer struct {
	inner transport.Dialer
	state *replayState
}

func (d *trackingDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	d.state.mu.Lock()
	if d.state.dialed {
		err := fmt.Errorf("%w: replay bundle permits one session connection", replay.ErrBundleMismatch)
		d.state.mu.Unlock()
		return nil, err
	}
	d.state.dialed = true
	d.state.mu.Unlock()
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &trackingConn{inner: conn, state: d.state}, nil
}

type trackingConn struct {
	inner transport.Conn
	state *replayState
}

func (c *trackingConn) ReadMessage() (int, []byte, error) {
	messageType, payload, err := c.inner.ReadMessage()
	if err != nil {
		return messageType, payload, err
	}
	c.state.mu.Lock()
	if expected := c.state.expectedTypeLocked(); expected != 0 && messageType != expected {
		if expected == 2 && messageType == 1 {
			messageType = expected
		} else {
			err := fmt.Errorf("%w: received message type %d, expected %d", replay.ErrBundleMismatch, messageType, expected)
			c.state.mu.Unlock()
			return messageType, payload, err
		}
	}
	c.state.consumed++
	c.state.mu.Unlock()
	return messageType, payload, nil
}

func (c *trackingConn) WriteMessage(messageType int, payload []byte) error {
	c.state.mu.Lock()
	expected := c.state.expectedTypeLocked()
	c.state.mu.Unlock()
	if expected != 0 && messageType != expected {
		err := fmt.Errorf("%w: sent message type %d, expected %d", replay.ErrBundleMismatch, messageType, expected)
		return err
	}
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		return fmt.Errorf("%w: %w", replay.ErrBundleMismatch, err)
	}
	c.state.mu.Lock()
	c.state.consumed++
	c.state.mu.Unlock()
	return nil
}

func (c *trackingConn) Close() error {
	return c.inner.Close()
}
