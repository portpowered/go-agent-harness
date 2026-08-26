package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	_ messages.Session                  = (*grokSession)(nil)
	_ messages.SessionSendOutcomeSender = (*grokSession)(nil)
	_ messages.SessionDropCounters      = (*grokSession)(nil)
)

// grokSession wraps a WebSocket connection as a bidirectional StreamMessage session.
// It translates between agent loop StreamMessages and the Grok wire protocol
// (OpenAI Realtime API conventions) internally, so consumers only see generic
// StreamMessage types.
type grokSession struct {
	conn   transport.Conn
	logger logging.Logger

	// sendQueue buffers outbound wire events (client-to-provider, the
	// session's input path). Overflow drops are counted by the buffer itself
	// and logged through the default drop observer attached in newGrokSession.
	sendQueue *messages.TypedBuffer[models.SessionEvent]

	// recvBuf is the inbound typed buffer of translated StreamMessages.
	// Populated by readLoop() after translating from wire events; it is the
	// provider-to-client output path of the session.
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done      chan struct{}
	closeOnce sync.Once
}

func newGrokSession(conn transport.Conn, logger logging.Logger) *grokSession {
	s := &grokSession{
		conn:      conn,
		logger:    logger,
		sendQueue: messages.NewTypedBuffer[models.SessionEvent](64),
		recvBuf:   messages.NewTypedBuffer[messages.StreamMessage](64),
		done:      make(chan struct{}),
	}
	providers.AttachSessionDropLoggers(logger, s.sendQueue, s.recvBuf)
	return s
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
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome writes a StreamMessage to the outbound queue and reports the
// precise public lifecycle outcome.
func (s *grokSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	event, ok := translateOutbound(msg)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	default:
	}
	// A terminated session reports closed regardless of remaining outbound
	// buffer capacity.
	select {
	case <-s.done:
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	default:
	}
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	case <-s.done:
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	default:
	}
	outcome := s.sendQueue.WriteContext(ctx, event)
	switch outcome.Status {
	case messages.BufferWriteSucceeded:
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	case messages.BufferWriteBufferFull:
		return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
	default:
		return sessionSendContextOutcome(ctx)
	}
}

func sessionSendContextOutcome(ctx context.Context) messages.SessionSendOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

// Receive returns the inbound typed buffer of StreamMessages translated from
// the Grok server. Callers read from this buffer to consume session events.
func (s *grokSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

// InputDrops reports cumulative drops on the client-to-provider send queue.
func (s *grokSession) InputDrops() int64 { return s.sendQueue.Drops() }

// OutputDrops reports cumulative drops on the provider-to-client receive buffer.
func (s *grokSession) OutputDrops() int64 { return s.recvBuf.Drops() }

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
			if ctx.Err() != nil {
				_ = s.Close()
				return
			}
			if !transport.IsInjectedFault(err) {
				_ = s.Close()
				return
			}
			s.logger.Error("grok: websocket read error", logging.Field{Key: "error", Value: err})
			// A transport close is a provider-visible terminal failure. Preserve
			// the typed read error in the stream so callers can distinguish an
			// abrupt close from an intentional session shutdown.
			s.recvBuf.WriteTerminal(messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: providers.NewStreamTransportErrorValue(err),
			})
			_ = s.Close()
			return
		}

		event, err := parseServerEvent(data)
		if err != nil {
			s.logger.Warn("grok: failed to parse server event",
				logging.Field{Key: "error", Value: err},
				logging.Field{Key: "raw", Value: string(data)},
			)
			// An unparseable provider frame is a protocol violation, not a
			// skippable event: surface a classified terminal ERROR so consumers
			// can diagnose the failure instead of silently losing the stream.
			s.recvBuf.WriteTerminal(messages.StreamMessage{
				Type: messages.StreamTypeError,
				Value: messages.NewErrorValueWithTerminal(
					fmt.Sprintf("malformed provider event: %v", err),
					providers.ErrorClassInvalidRequest,
					messages.TerminalReasonTerminalFailure,
					messages.TerminalProvenanceGateway,
					messages.TerminalOutputNone,
				),
			})
			_ = s.Close()
			return
		}

		msgs := translateInbound(event)
		for _, m := range msgs {
			if !s.recvBuf.Write(ctx, m) {
				select {
				case <-s.done:
					return
				case <-ctx.Done():
					_ = s.Close()
					return
				default:
					// Buffer full — drop the message (onDrop callback can log if set).
				}
			}
		}
	}
}

// writeLoop reads events from the send queue and writes them to the WebSocket.
func (s *grokSession) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = s.Close()
			return
		case <-s.done:
			return
		case event := <-s.sendQueue.Chan():
			if err := s.writeEvent(event); err != nil {
				s.logger.Error("grok: websocket write error", logging.Field{Key: "error", Value: err})
				_ = s.Close()
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
