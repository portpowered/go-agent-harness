package seed

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type session struct {
	inner      messages.Session
	wirePrompt string
	seed       sessionturn.Seed
	receive    *messages.TypedBuffer[messages.StreamMessage]
	seedMu     sync.Mutex
	seedSent   bool
}

func newSession(ctx context.Context, inner messages.Session, wirePrompt string, seed sessionturn.Seed) *session {
	s := &session{
		inner:      inner,
		wirePrompt: wirePrompt,
		seed:       seed,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](sessionturn.ReceiveCapacity),
	}
	go s.forwardIncoming(ctx)
	return s
}

var _ sessionturn.Session = (*session)(nil)

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
	sender, ok := s.inner.(sessionturn.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *session) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *session) SupportsCompleteMessages() bool {
	if capabilities, ok := s.inner.(sessionturn.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessages()
	}
	_, ok := s.inner.(sessionturn.CompleteMessageSender)
	return ok
}

func (s *session) SupportsCompleteMessagesWithoutResponse() bool {
	if capabilities, ok := s.inner.(sessionturn.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok := s.inner.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok
}

func (s *session) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *session) Done() <-chan struct{} { return s.inner.Done() }

func (s *session) TerminalError() error {
	source, ok := s.inner.(sessionturn.TerminalErrorSource)
	if !ok {
		return nil
	}
	return source.TerminalError()
}

// RTCMedia forwards the optional shared media capability without importing a
// host package. The boolean form keeps a seed-decorated non-RTC session from
// being mistaken for a live media owner.
func (s *session) RTCMedia() (sharedaudio.MediaEndpoints, bool) {
	if owner, ok := s.inner.(sharedaudio.MediaSession); ok {
		return owner.RTCMedia(), true
	}
	if owner, ok := s.inner.(interface {
		RTCMedia() (sharedaudio.MediaEndpoints, bool)
	}); ok {
		return owner.RTCMedia()
	}
	return sharedaudio.MediaEndpoints{}, false
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
