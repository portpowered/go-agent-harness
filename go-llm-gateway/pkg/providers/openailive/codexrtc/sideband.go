package codexrtc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// Sideband limits.
const (
	// DefaultSidebandWriteTimeout bounds one sideband write, so a peer that
	// stops reading cannot block a sender forever.
	DefaultSidebandWriteTimeout = 10 * time.Second
	// SidebandCloseGrace bounds how long Close waits to send session.close
	// and the close frame before it drops the connection.
	SidebandCloseGrace = time.Second
	// SidebandMaxFrameBytes bounds one received frame, as OpenClaw does
	// (realtime-quicksilver-sideband.ts:20).
	SidebandMaxFrameBytes = 16 << 20
)

// ErrSidebandClosed reports that the sideband ended with a normal close, or
// was closed locally. Any other end is an error that wraps the cause.
var ErrSidebandClosed = errors.New("codexrtc: sideband closed")

// Sideband is the control WebSocket of one call. Send is safe for concurrent
// use; Receive must have one caller at a time. Close may be called at any
// time, from any goroutine, and always releases the connection.
type Sideband struct {
	conn         *websocket.Conn
	writeTimeout time.Duration
	// writeLock is a one-slot semaphore rather than a mutex so Close can give
	// up on it after SidebandCloseGrace.
	writeLock chan struct{}
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

func newSideband(conn *websocket.Conn, writeTimeout time.Duration) *Sideband {
	conn.SetReadLimit(SidebandMaxFrameBytes)
	return &Sideband{conn: conn, writeTimeout: writeTimeout, writeLock: make(chan struct{}, 1)}
}

// DialSideband opens the sideband of call with a fresh credential and the
// call's identity headers. Nothing is sent on connect: the session was set at
// call creation, and the server reports session.started on its own.
func (c *CallClient) DialSideband(ctx context.Context, call Call) (*Sideband, error) {
	if !IsCallID(call.ID) {
		return nil, fmt.Errorf("%w: call id %q", ErrBadCallResponse, call.ID)
	}
	header, err := c.identityHeaders(ctx, call.IDs)
	if err != nil {
		return nil, err
	}
	conn, response, err := c.dialer.DialContext(ctx, c.sidebandBase+"/"+call.ID, header)
	if err != nil {
		if response != nil {
			return nil, errors.Join(&StatusError{Op: opSideband, StatusCode: response.StatusCode}, closeBody(response))
		}
		return nil, fmt.Errorf("codexrtc: dial sideband: %w", err)
	}
	if err := closeBody(response); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return newSideband(conn, c.writeTimeout), nil
}

// Send writes one event within the write timeout. After Close it fails with
// ErrSidebandClosed.
func (s *Sideband) Send(event quicksilver.Event) error {
	frame, err := quicksilver.EncodeEvent(event)
	if err != nil {
		return err
	}
	s.writeLock <- struct{}{}
	defer func() { <-s.writeLock }()
	if s.closed.Load() {
		return ErrSidebandClosed
	}
	return s.write(event.EventType(), frame, s.writeTimeout)
}

// write sends one text frame with a deadline; the caller holds writeLock.
func (s *Sideband) write(eventType string, frame []byte, timeout time.Duration) error {
	if err := s.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("codexrtc: send %s: %w", eventType, err)
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		return fmt.Errorf("codexrtc: send %s: %w", eventType, err)
	}
	return nil
}

// Receive reads the next server event. A normal close ends the stream with
// ErrSidebandClosed; an abnormal close or a read failure is returned wrapped,
// as Codex treats it as a lost transport. A binary frame is malformed.
func (s *Sideband) Receive() (quicksilver.Event, error) {
	messageType, frame, err := s.conn.ReadMessage()
	if err != nil {
		if s.closed.Load() || websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			return nil, fmt.Errorf("%w: %w", ErrSidebandClosed, err)
		}
		return nil, fmt.Errorf("codexrtc: sideband read: %w", err)
	}
	if messageType != websocket.TextMessage {
		return nil, fmt.Errorf("%w: binary sideband frame", quicksilver.ErrMalformedEvent)
	}
	return quicksilver.DecodeServerEvent(frame)
}

// Close ends the sideband. It first expires the write deadline so a Send
// blocked on a peer that stopped reading returns, then, if the write lock
// comes free within SidebandCloseGrace, sends session.close and a normal
// close frame with that deadline, and always closes the connection. It is
// idempotent. The frames are best effort: a peer that is gone or stalled is
// not an error.
func (s *Sideband) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		unstickErr := s.conn.NetConn().SetWriteDeadline(time.Now())
		frameErr := s.sendCloseFrames()
		if peerGone(frameErr) {
			frameErr = nil
		}
		closeErr := s.conn.Close()
		if errors.Is(closeErr, net.ErrClosed) {
			closeErr = nil
		}
		s.closeErr = errors.Join(unstickErr, frameErr, closeErr)
	})
	return s.closeErr
}

func (s *Sideband) sendCloseFrames() error {
	timer := time.NewTimer(SidebandCloseGrace)
	defer timer.Stop()
	select {
	case s.writeLock <- struct{}{}:
	case <-timer.C:
		return nil // a writer is still stuck; dropping the connection unblocks it
	}
	defer func() { <-s.writeLock }()
	frame, err := quicksilver.EncodeEvent(quicksilver.SessionClose{})
	if err != nil {
		return err
	}
	if err := s.write(quicksilver.TypeSessionClose, frame, SidebandCloseGrace); err != nil {
		return err
	}
	closeFrame := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	return s.conn.WriteControl(websocket.CloseMessage, closeFrame, time.Now().Add(SidebandCloseGrace))
}

// peerGone reports a write that failed because the connection already ended
// or its deadline expired.
func peerGone(err error) bool {
	var netErr net.Error
	return errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) ||
		(errors.As(err, &netErr) && netErr.Timeout())
}

// closeBody closes a handshake response body; gorilla leaves it open.
func closeBody(response *http.Response) error {
	if response == nil || response.Body == nil {
		return nil
	}
	if err := response.Body.Close(); err != nil {
		return fmt.Errorf("codexrtc: close sideband handshake: %w", err)
	}
	return nil
}
