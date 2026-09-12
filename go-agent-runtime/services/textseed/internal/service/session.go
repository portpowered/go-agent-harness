package service

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
)

type session struct {
	inner      messages.Session
	wirePrompt string
	seed       textseed.Seed
	receive    *messages.TypedBuffer[messages.StreamMessage]
	seedMu     sync.Mutex
	seedSent   bool
}

func newSession(ctx context.Context, inner messages.Session, wirePrompt string, seed textseed.Seed) *session {
	s := &session{
		inner:      inner,
		wirePrompt: wirePrompt,
		seed:       seed,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](textseed.ReceiveCapacity),
	}
	go s.forwardIncoming(ctx)
	return s
}

var _ textseed.Session = (*session)(nil)

func (s *session) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *session) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.seed.Value)
	}
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *session) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *session) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *session) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(textseed.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *session) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(textseed.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *session) SupportsCompleteMessages() bool {
	_, ok := s.inner.(textseed.CompleteMessageSender)
	return ok
}

func (s *session) SupportsCompleteMessagesWithoutResponse() bool {
	_, ok := s.inner.(textseed.CompleteMessageWithoutResponseSender)
	return ok
}

func (s *session) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *session) Done() <-chan struct{} { return s.inner.Done() }

func (s *session) TerminalError() error {
	source, ok := s.inner.(textseed.TerminalErrorSource)
	if !ok {
		return nil
	}
	return source.TerminalError()
}

func (s *session) Close() error { return s.inner.Close() }

func (s *session) forwardIncoming(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() {
		if err := s.inner.Close(); err != nil {
			return
		}
	})
	defer stop()
	for {
		msg, ok := s.inner.Receive().ReadBlocking(s.inner.Done())
		if !ok {
			return
		}
		if !s.receive.Write(ctx, msg) {
			if err := s.inner.Close(); err != nil {
				return
			}
			return
		}
	}
}

func (s *session) replaceSeed(msg messages.StreamMessage) bool {
	if !s.seed.Present || msg.Type != messages.StreamTypeTextDelta {
		return false
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != s.wirePrompt {
		return false
	}

	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	if s.seedSent {
		return false
	}
	s.seedSent = true
	return true
}
