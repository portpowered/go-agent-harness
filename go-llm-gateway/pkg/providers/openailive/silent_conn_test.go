package openailive_test

import (
	"errors"
	"sync"

	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var errSilentClosed = errors.New("silent conn closed")

// silentConn answers session.start with session.started (unless
// withholdStart is set) and then never sends another frame, so a close
// handshake can only end by timeout.
type silentConn struct {
	withholdStart bool

	once    sync.Once
	closed  chan struct{}
	mu      sync.Mutex
	replied bool
}

func newSilentConn() *silentConn {
	return &silentConn{closed: make(chan struct{})}
}

func (c *silentConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	reply := !c.replied && !c.withholdStart
	c.replied = true
	c.mu.Unlock()
	if reply {
		frame, err := live.EncodeEvent(live.SessionStarted{Session: live.SessionResource{ID: "live_silent", SessionConfig: live.SessionConfig{Model: live.Model1}}})
		return 1, frame, err
	}
	<-c.closed
	return 0, nil, errSilentClosed
}

func (c *silentConn) WriteMessage(int, []byte) error {
	select {
	case <-c.closed:
		return errSilentClosed
	default:
		return nil
	}
}

func (c *silentConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

type silentDialer struct{ conn *silentConn }

func (d silentDialer) Dial(string, map[string]string) (transport.Conn, error) { return d.conn, nil }
