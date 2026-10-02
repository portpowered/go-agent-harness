package realtime

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// Handler is the provider-specific part of the read and write loops.
type Handler interface {
	// HandleEvent observes one parsed provider event (response lifecycle,
	// RTC media) and returns its normalized stream messages.
	HandleEvent(ctx context.Context, event models.SessionEvent) []messages.StreamMessage
	// ExpectedReadClose reports whether a read error is an orderly provider
	// close; the session then closes without a terminal error.
	ExpectedReadClose(err error) bool
	// ExpectedWriteClose reports whether a write error is part of an orderly
	// shutdown; the write loop then exits without a terminal error. Unless
	// ctx ended, it leaves closing to the read loop, which still drains
	// frames the provider sent before closing.
	ExpectedWriteClose(ctx context.Context, err error) bool
	// EventWritten runs after event reached the connection.
	EventWritten(event models.SessionEvent)
}

// ReadEndHandler is an optional Handler extension for a protocol with its
// own terminal event. When the read loop ends with an error that is not an
// orderly close (ExpectedReadClose is false and the session is not
// stopping), ReadEnded runs before the session closes. Returning true means
// the provider reported the end itself, so the skeleton closes the session
// without recording its own transport ERROR.
type ReadEndHandler interface {
	ReadEnded(ctx context.Context, err error) bool
}

// Start launches the read and write loops.
func (s *Session) Start(ctx context.Context, h Handler) {
	go s.ReadLoop(ctx, h)
	go s.WriteLoop(ctx, h)
}

// ReadLoop reads provider frames, hands each parsed event to h and delivers
// the normalized messages until the connection or session ends.
func (s *Session) ReadLoop(ctx context.Context, h Handler) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.handleReadError(ctx, h, err)
			return
		}
		event, err := ParseEvent(data)
		if err != nil {
			s.handleParseError(err)
			return
		}
		for _, msg := range h.HandleEvent(ctx, event) {
			if !s.deliver(ctx, msg) {
				return
			}
		}
	}
}

// deliver writes one normalized message and reports whether the read loop
// should continue.
func (s *Session) deliver(ctx context.Context, msg messages.StreamMessage) bool {
	if s.cfg.LosslessInbound {
		// Provider frames are lossless. Apply backpressure when the bounded
		// normalized buffer fills, retaining both shutdown paths.
		if outcome := s.recvBuf.WriteWaitContextOrDone(ctx, s.done, msg); !outcome.OK() {
			if ctx.Err() != nil {
				s.CloseWithLog()
			}
			return false
		}
		return true
	}
	if s.recvBuf.Write(ctx, msg) {
		return true
	}
	if s.Closed() {
		return false
	}
	if ctx.Err() != nil {
		s.CloseWithLog()
		return false
	}
	// Buffer full: the message is dropped and counted by the buffer.
	return true
}

func (s *Session) handleReadError(ctx context.Context, h Handler, err error) {
	if s.Stopping(ctx) || h.ExpectedReadClose(err) {
		s.CloseWithLog()
		return
	}
	if ender, ok := h.(ReadEndHandler); ok && ender.ReadEnded(ctx, err) {
		s.CloseWithLog()
		return
	}
	s.SetTerminalError(err)
	s.logger.Error(s.cfg.LogPrefix+": websocket read error", logging.Field{Key: "error", Value: err})
	// Preserve unexpected read failures so callers can distinguish abrupt
	// provider closure from intentional shutdown.
	s.recvBuf.WriteTerminal(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: providers.NewStreamTransportErrorValue(err),
	})
	s.CloseWithLog()
}

func (s *Session) handleParseError(err error) {
	s.SetTerminalError(err)
	s.logger.Warn(s.cfg.LogPrefix+": failed to parse server event", logging.Field{Key: "error", Value: err})
	// An unparseable provider frame is a protocol violation, not a skippable
	// event: surface a classified terminal ERROR so consumers can diagnose
	// the failure instead of silently losing the stream.
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
	s.CloseWithLog()
}

// WriteLoop writes queued events to the connection until the session ends.
func (s *Session) WriteLoop(ctx context.Context, h Handler) {
	for {
		select {
		case <-ctx.Done():
			s.CloseWithLog()
			return
		case <-s.done:
			return
		case event := <-s.sendQueue.Chan():
			if !s.writeQueued(ctx, h, event) {
				return
			}
		}
	}
}

// writeQueued writes one dequeued event and reports whether the loop should
// continue.
func (s *Session) writeQueued(ctx context.Context, h Handler, event models.SessionEvent) bool {
	err := s.WriteEvent(event)
	if err == nil {
		h.EventWritten(event)
		s.outbound.Complete()
		return true
	}
	// The write settles before the session closes, and after a terminal error
	// is recorded, so a FlushOutbound waiter reports the write's real outcome.
	if h.ExpectedWriteClose(ctx, err) {
		s.outbound.Complete()
		if ctx.Err() != nil {
			s.CloseWithLog()
		}
		return false
	}
	s.SetTerminalError(err)
	s.outbound.Complete()
	s.logger.Error(s.cfg.LogPrefix+": websocket write error", logging.Field{Key: "error", Value: err})
	s.CloseWithLog()
	return false
}
