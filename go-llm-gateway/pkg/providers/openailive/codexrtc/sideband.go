package codexrtc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// ErrSidebandClosed reports that the sideband ended with a normal close, or
// was closed locally. Any other end is an error that wraps the cause.
var ErrSidebandClosed = errors.New("codexrtc: sideband closed")

// Sideband is the control WebSocket of one call. Send is safe for concurrent
// use; Receive must have one caller at a time.
type Sideband struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
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
	return &Sideband{conn: conn}, nil
}

// Send writes one event.
func (s *Sideband) Send(event quicksilver.Event) error {
	frame, err := quicksilver.EncodeEvent(event)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		return fmt.Errorf("codexrtc: send %s: %w", event.EventType(), err)
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

// Close sends session.close and a normal close frame, then releases the
// connection. It is idempotent. A peer that is already gone is not an
// error: the frames are best effort.
func (s *Sideband) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		frameErr := s.Send(quicksilver.SessionClose{})
		if frameErr == nil {
			s.writeMu.Lock()
			frameErr = s.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			s.writeMu.Unlock()
		}
		if peerGone(frameErr) {
			frameErr = nil
		}
		s.closeErr = errors.Join(frameErr, s.conn.Close())
	})
	return s.closeErr
}

// peerGone reports a write that failed because the connection already ended.
func peerGone(err error) bool {
	return errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
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
