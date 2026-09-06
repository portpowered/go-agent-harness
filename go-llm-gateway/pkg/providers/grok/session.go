package grok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	_ messages.Session                   = (*grokSession)(nil)
	_ messages.SessionSendOutcomeSender  = (*grokSession)(nil)
	_ messages.SessionResponseRequester  = (*grokSession)(nil)
	_ messages.SessionResponseCapability = (*grokSession)(nil)
	_ messages.SessionDropCounters       = (*grokSession)(nil)
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

	done        chan struct{}
	closeOnce   sync.Once
	errMu       sync.Mutex
	terminalErr error

	mediaMu         sync.Mutex
	media           *sharedaudio.SessionMedia
	mediaClaimed    bool
	mediaContinuous bool
	mediaSampleRate int
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

// InitialSessionConfigSent reports that ConnectSession already sent the
// provider-owned session.update before the read loop started. The runtime uses
// this optional marker to avoid echoing that configuration on session.created.
func (*grokSession) InitialSessionConfigSent() bool { return true }

// SendWithOutcome writes a StreamMessage to the outbound queue and reports the
// precise public lifecycle outcome.
func (s *grokSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	event, ok := translateOutbound(msg)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	events := []models.SessionEvent{event}
	if msg.Type == messages.StreamTypeMessageEnd {
		// Finite client-side audio turns need an explicit response request after
		// committing their input buffer. This mirrors the OpenAI realtime
		// adapter and keeps Grok device probes from waiting for server-side VAD.
		events = append(events, models.NewResponseCreateEvent())
	}
	return s.sendEvents(ctx, events)
}

// RequestResponse starts a response without adding another user turn. This is
// needed when a tool result follows an audio-only input, whose history has no
// text event that can request the continuation.
func (s *grokSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (*grokSession) SupportsResponseRequests() bool { return true }

func (s *grokSession) sendEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
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
	for _, event := range events {
		// A terminated session reports closed regardless of remaining outbound
		// buffer capacity.
		select {
		case <-s.done:
			return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
		default:
		}
		outcome := s.sendQueue.WriteContext(ctx, event)
		switch outcome.Status {
		case messages.BufferWriteSucceeded:
		case messages.BufferWriteBufferFull:
			return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
		default:
			return sessionSendContextOutcome(ctx)
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
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
	s.releaseUnclaimedRTCMedia()
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

// TerminalError returns the unexpected provider-side transport or protocol
// error that terminated the session, if one was observed. A clean caller-side
// Close and context cancellation do not set this value.
func (s *grokSession) TerminalError() error {
	if s == nil {
		return nil
	}
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.terminalErr
}

func (s *grokSession) setTerminalError(err error) {
	if s == nil || err == nil {
		return
	}
	s.errMu.Lock()
	if s.terminalErr == nil {
		s.terminalErr = err
	}
	s.errMu.Unlock()
}

func (s *grokSession) isStopping(ctx context.Context) bool {
	select {
	case <-s.done:
		return true
	default:
	}
	return ctx.Err() != nil
}

func isExpectedGrokReadClose(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
}

func (s *grokSession) handleReadError(ctx context.Context, err error) {
	if s.isStopping(ctx) {
		s.closeWithLog()
		return
	}
	if isExpectedGrokReadClose(err) &&
		!transport.IsInjectedFault(err) &&
		!errors.Is(err, providers.ErrReplayMismatch) {
		s.closeWithLog()
		return
	}
	s.setTerminalError(err)
	s.logger.Error("grok: websocket read error", logging.Field{Key: "error", Value: err})
	s.recvBuf.WriteTerminal(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: providers.NewStreamTransportErrorValue(err),
	})
	s.closeWithLog()
}

func (s *grokSession) handleParseError(raw []byte, err error) {
	s.setTerminalError(err)
	s.logger.Warn("grok: failed to parse server event",
		logging.Field{Key: "error", Value: err},
		logging.Field{Key: "raw", Value: string(raw)},
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
	s.closeWithLog()
}

// readLoop reads messages from the WebSocket, translates them to StreamMessages,
// and writes them to recvBuf.
func (s *grokSession) readLoop(ctx context.Context) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.handleReadError(ctx, err)
			return
		}

		event, err := parseServerEvent(data)
		if err != nil {
			s.handleParseError(data, err)
			return
		}

		_ = s.publishRTCMedia(event)
		msgs := translateInbound(event)
		for _, m := range msgs {
			if !s.recvBuf.Write(ctx, m) {
				select {
				case <-s.done:
					return
				case <-ctx.Done():
					s.closeWithLog()
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
				if s.isStopping(ctx) {
					s.closeWithLog()
					return
				}
				s.setTerminalError(err)
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
		if media := s.currentRTCMedia(); media != nil {
			_ = media.Close()
		}
		closeErr = s.conn.Close()
	})
	return closeErr
}

func (s *grokSession) closeWithLog() {
	if err := s.Close(); err != nil {
		s.logger.Warn("grok: session close error", logging.Field{Key: "error", Value: err})
	}
}
