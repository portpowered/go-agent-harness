package grok

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

// Ensure grokSession satisfies messages.Session at compile time.
var _ messages.Session = (*grokSession)(nil)

// grokSession wraps a WebSocket connection as a bidirectional StreamMessage session.
// It translates between agent loop StreamMessages and the Grok wire protocol
// (OpenAI Realtime API conventions) internally, so consumers only see generic
// StreamMessage types.
type grokSession struct {
	conn   WebSocketConn
	logger logging.Logger

	// sendCh is the internal queue for outbound wire events (SessionEvent).
	// Populated by Send() after translating from StreamMessage.
	sendCh chan models.SessionEvent

	// recvBuf is the inbound typed buffer of translated StreamMessages.
	// Populated by readLoop() after translating from wire events.
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done      chan struct{}
	closeOnce sync.Once
}

func newGrokSession(conn WebSocketConn, logger logging.Logger) *grokSession {
	return &grokSession{
		conn:    conn,
		logger:  logger,
		sendCh:  make(chan models.SessionEvent, 64),
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

// start launches the read and write goroutines.
func (s *grokSession) start(ctx context.Context) {
	go s.readLoop(ctx)
	go s.writeLoop(ctx)
}

// Send writes a StreamMessage to the session's outbound queue.
// It translates the StreamMessage to a Grok wire event. Returns false
// if the context is cancelled, the session is done, or the message type
// has no outbound representation.
func (s *grokSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	event, ok := translateOutbound(msg)
	if !ok {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.done:
		return false
	case s.sendCh <- event:
		return true
	}
}

// Receive returns the inbound typed buffer of StreamMessages translated from
// the Grok server. Callers read from this buffer to consume session events.
func (s *grokSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

// Done returns a channel closed when the session terminates.
func (s *grokSession) Done() <-chan struct{} {
	return s.done
}

// readLoop reads messages from the WebSocket, translates them to StreamMessages,
// and writes them to recvBuf.
func (s *grokSession) readLoop(ctx context.Context) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.done:
				// Clean shutdown, don't log.
				return
			default:
			}
			s.logger.Error("grok: websocket read error", logging.Field{Key: "error", Value: err})
			s.Close()
			return
		}

		event, err := parseServerEvent(data)
		if err != nil {
			s.logger.Warn("grok: failed to parse server event",
				logging.Field{Key: "error", Value: err},
				logging.Field{Key: "raw", Value: string(data)},
			)
			continue
		}

		msgs := translateInbound(event)
		for _, m := range msgs {
			if !s.recvBuf.Write(ctx, m) {
				select {
				case <-s.done:
					return
				case <-ctx.Done():
					s.Close()
					return
				default:
					// Buffer full — drop the message (onDrop callback can log if set).
				}
			}
		}
	}
}

// writeLoop reads events from sendCh and writes them to the WebSocket.
func (s *grokSession) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-s.done:
			return
		case event, ok := <-s.sendCh:
			if !ok {
				return
			}
			if err := s.writeEvent(event); err != nil {
				s.logger.Error("grok: websocket write error", logging.Field{Key: "error", Value: err})
				s.Close()
				return
			}
		}
	}
}

// writeEvent serializes a SessionEvent to JSON and writes it to the WebSocket.
func (s *grokSession) writeEvent(event models.SessionEvent) error {
	msg := wireEvent{
		Type: string(event.Type),
	}

	// Merge Data fields into the top-level wire message.
	if len(event.Data) > 0 {
		var dataMap map[string]json.RawMessage
		if err := json.Unmarshal(event.Data, &dataMap); err == nil {
			msg.Extra = dataMap
		}
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// WebSocket text message type = 1.
	return s.conn.WriteMessage(1, data)
}

// Close terminates the session, closing the WebSocket and signalling done.
func (s *grokSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		closeErr = s.conn.Close()
	})
	return closeErr
}
