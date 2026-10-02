package openailive

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// idleConn never delivers a frame; Close releases a blocked read.
type idleConn struct {
	once   sync.Once
	closed chan struct{}
}

var errIdleClosed = errors.New("idle conn closed")

func (c *idleConn) ReadMessage() (int, []byte, error) { <-c.closed; return 0, nil, errIdleClosed }
func (c *idleConn) WriteMessage(int, []byte) error    { return nil }
func (c *idleConn) Close() error                      { c.once.Do(func() { close(c.closed) }); return nil }

// A terminal record emitted after the session ended and the pump drained and
// exited (the interleaving where session.closed lands after the close
// handshake released the session) is still delivered, never stranded.
func TestTerminalEmittedAfterThePumpExitedIsDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newLiveSession(&idleConn{closed: make(chan struct{})}, nil, sessionSettings{
			format: AudioFormat{Type: AudioTypePCM, Rate: RatePCM24k}, clock: clock.Real{},
			segmentGap: DefaultSegmentGap, closeTimeout: time.Second,
		})
		go s.pump(t.Context())
		if err := s.base.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		synctest.Wait() // the pump has seen the end, drained and exited
		s.mu.Lock()
		s.emitTerminalLocked(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("live_x", CloseReasonCloseRequested)})
		s.mu.Unlock()
		msg, ok := s.Receive().Read()
		if !ok || msg.Type != messages.StreamTypeSessionClose {
			t.Fatalf("receive = %v %v, want the late SESSION.CLOSE", msg.Type, ok)
		}
	})
}
