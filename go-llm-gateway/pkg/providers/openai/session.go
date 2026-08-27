package openai

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

var (
	_ messages.Session                  = (*realtimeSession)(nil)
	_ messages.SessionSendOutcomeSender = (*realtimeSession)(nil)
	_ messages.SessionDropCounters      = (*realtimeSession)(nil)
)

type realtimeSession struct {
	conn   transport.Conn
	logger logging.Logger
	// sendQueue buffers outbound wire events (client-to-provider, the
	// session's input path). Overflow drops are counted by the buffer itself
	// and logged through the default drop observer attached below.
	sendQueue *messages.TypedBuffer[models.SessionEvent]
	// recvBuf buffers translated inbound events (provider-to-client, the
	// session's output path).
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done      chan struct{}
	closeOnce sync.Once

	mediaMu sync.Mutex
	media   *rtc.SessionMedia
}

var _ messages.SessionSendOutcomeSender = (*realtimeSession)(nil)

func newRealtimeSession(conn transport.Conn, logger logging.Logger) *realtimeSession {
	s := &realtimeSession{
		conn:      conn,
		logger:    logger,
		sendQueue: messages.NewTypedBuffer[models.SessionEvent](64),
		recvBuf:   messages.NewTypedBuffer[messages.StreamMessage](64),
		done:      make(chan struct{}),
	}
	providers.AttachSessionDropLoggers(logger, s.sendQueue, s.recvBuf)
	return s
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

func (s *realtimeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

// InputDrops reports cumulative drops on the client-to-provider send queue.
func (s *realtimeSession) InputDrops() int64 { return s.sendQueue.Drops() }

// OutputDrops reports cumulative drops on the provider-to-client receive buffer.
func (s *realtimeSession) OutputDrops() int64 { return s.recvBuf.Drops() }

func (s *realtimeSession) Done() <-chan struct{} {
	return s.done
}

func (s *realtimeSession) Close() error {
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
