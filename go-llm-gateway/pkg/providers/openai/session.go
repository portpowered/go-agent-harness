package openai

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var _ messages.Session = (*realtimeSession)(nil)

type realtimeSession struct {
	conn    transport.Conn
	logger  logging.Logger
	sendCh  chan models.SessionEvent
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done      chan struct{}
	closeOnce sync.Once
}

var _ messages.SessionSendOutcomeSender = (*realtimeSession)(nil)

func newRealtimeSession(conn transport.Conn, logger logging.Logger) *realtimeSession {
	return &realtimeSession{
		conn:    conn,
		logger:  logger,
		sendCh:  make(chan models.SessionEvent, 64),
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

func (s *realtimeSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome writes a StreamMessage to the outbound queue and reports the
// precise public lifecycle outcome.
func (s *realtimeSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	events, ok := realtimeOutboundEvents(msg)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.sendEvents(ctx, events)
}

func (s *realtimeSession) sendEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	default:
	}
	for _, event := range events {
		// A terminated session reports closed regardless of remaining
		// outbound buffer capacity.
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
		case s.sendCh <- event:
		default:
			return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
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

func (s *realtimeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

func (s *realtimeSession) Done() <-chan struct{} {
	return s.done
}

func (s *realtimeSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		closeErr = s.conn.Close()
	})
	return closeErr
}
