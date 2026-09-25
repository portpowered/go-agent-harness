package wire

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

type testSessionService struct {
	mu              sync.Mutex
	configs         []providers.SessionConfig
	sessions        []*testSession
	failure         string
	silent          bool
	connected       chan struct{}
	connectEntered  chan struct{}
	connectRelease  <-chan struct{}
	connectReturned chan struct{}
	// Half-duplex turn-taking tokens. The service commits the turn target, and
	// immediately cancels both loops, on the assistant's MESSAGE.END. Bridged
	// PCM that is still queued inside the customer loop at that instant is
	// intentionally dropped by the stop, so the fake assistant completes a
	// response only after the customer session has heard its PCM, and the fake
	// customer replies only after the assistant response it heard completed.
	customerHeard      chan struct{}
	assistantCompleted chan struct{}
}

type firstFailureSessionService struct{ built int }

func (s *firstFailureSessionService) BuildSession(_ context.Context, _ providers.SessionConfig) (messages.SessionInferencer, error) {
	side := s.built
	s.built++
	return firstFailureInferencer{side: side, session: &testSession{receive: messages.NewTypedBuffer[messages.StreamMessage](128)}}, nil
}

type firstFailureInferencer struct {
	side    int
	session *testSession
}

func (i firstFailureInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i.side == 0 {
		failure := messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("primary customer failure")}
		if !i.session.receive.Write(ctx, failure) {
			return nil, ctx.Err()
		}
		return i.session, nil
	}
	<-ctx.Done()
	return nil, errors.New("secondary assistant failure")
}

func newTestSessionService(t *testing.T, failure string) *testSessionService {
	t.Helper()
	service := &testSessionService{
		failure: failure, connected: make(chan struct{}, 2),
		customerHeard: make(chan struct{}, turnTokenCapacity), assistantCompleted: make(chan struct{}, turnTokenCapacity),
	}
	return service
}

func (s *testSessionService) BuildSession(_ context.Context, config providers.SessionConfig) (messages.SessionInferencer, error) {
	customer := config.Instructions == "You are the customer. Speak naturally, briefly, and only as part of a spoken conversation. Ask one practical follow-up at a time. Do not call tools."
	session := &testSession{receive: messages.NewTypedBuffer[messages.StreamMessage](128)}
	session.onSend = func(ctx context.Context, message messages.StreamMessage) {
		if s.silent {
			return
		}
		switch {
		case customer && message.Type == messages.StreamTypeTextDelta:
			session.emitResponse(ctx, testCustomerPCM())
		case customer && message.Type == messages.StreamTypeAudioDelta:
			s.customerHeard <- struct{}{}
			go func() {
				if awaitTurnToken(ctx, s.assistantCompleted) {
					session.emitResponse(ctx, testCustomerPCM())
				}
			}()
		case message.Type == messages.StreamTypeAudioDelta:
			if !session.emitAudio(ctx, testAssistantPCM()) {
				return
			}
			go func() {
				if awaitTurnToken(ctx, s.customerHeard) && session.emitEnd(ctx) {
					s.assistantCompleted <- struct{}{}
				}
			}()
		}
	}
	s.mu.Lock()
	s.configs = append(s.configs, config)
	s.sessions = append(s.sessions, session)
	s.mu.Unlock()
	return testInferencer{
		session: session, connected: s.connected, failure: s.failure,
		connectEntered: s.connectEntered, connectRelease: s.connectRelease, connectReturned: s.connectReturned,
	}, nil
}

func (s *testSessionService) snapshot() ([]providers.SessionConfig, []*testSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]providers.SessionConfig(nil), s.configs...), append([]*testSession(nil), s.sessions...)
}

type testInferencer struct {
	session         *testSession
	connected       chan struct{}
	failure         string
	connectEntered  chan struct{}
	connectRelease  <-chan struct{}
	connectReturned chan struct{}
}

func (i testInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i.connectEntered != nil {
		i.connectEntered <- struct{}{}
	}
	if i.connectRelease != nil {
		defer func() { i.connectReturned <- struct{}{} }()
		<-i.connectRelease
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if i.failure != "" && !i.session.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(i.failure)}) {
		return nil, ctx.Err()
	}
	if !i.session.receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("selfplay-test", "audio")}) {
		return nil, ctx.Err()
	}
	i.connected <- struct{}{}
	return i.session, nil
}

type testSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	emitMu    sync.Mutex
	mu        sync.Mutex
	sent      []messages.StreamMessage
	onSend    func(context.Context, messages.StreamMessage)
	closed    bool
}

func (s *testSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	s.sent = append(s.sent, cloneTestMessage(message))
	hook := s.onSend
	s.mu.Unlock()
	if hook != nil {
		hook(ctx, message)
	}
	return true
}

func (s *testSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *testSession) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}

func (s *testSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.done == nil {
			s.done = make(chan struct{})
		}
		s.closed = true
		close(s.done)
		s.mu.Unlock()
	})
	return nil
}

func (s *testSession) wasClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *testSession) emitResponse(ctx context.Context, pcm []byte) {
	if s.emitAudio(ctx, pcm) {
		s.emitEnd(ctx)
	}
}

func (s *testSession) emitAudio(ctx context.Context, pcm []byte) bool {
	return s.emit(ctx,
		messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(append([]byte(nil), pcm...))},
		messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
	)
}

func (s *testSession) emitEnd(ctx context.Context) bool {
	return s.emit(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func (s *testSession) emit(ctx context.Context, stream ...messages.StreamMessage) bool {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	for _, message := range stream {
		if !s.receive.Write(ctx, message) {
			return false
		}
	}
	return true
}

// awaitTurnToken blocks until the peer's turn-taking token arrives or the run
// cancels the session context.
func awaitTurnToken(ctx context.Context, tokens <-chan struct{}) bool {
	select {
	case <-tokens:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *testSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func cloneTestMessage(message messages.StreamMessage) messages.StreamMessage {
	if value, ok := message.Value.(*messages.AudioDeltaValue); ok {
		message.Value = messages.NewAudioDeltaValue(append([]byte(nil), value.Content...))
	}
	if value, ok := message.Value.(*messages.TextDeltaValue); ok {
		message.Value = messages.NewTextDeltaValue(value.Content)
	}
	return message
}

func countOutboundText(messagesToCheck []messages.StreamMessage, expected string) int {
	count := 0
	for _, message := range messagesToCheck {
		if message.Type != messages.StreamTypeTextDelta {
			continue
		}
		if expected == "" {
			count++
			continue
		}
		if value, ok := message.Value.(*messages.TextDeltaValue); ok && value.Content == expected {
			count++
		}
	}
	return count
}

func containsOutboundAudio(messagesToCheck []messages.StreamMessage, expected []byte) bool {
	for _, message := range messagesToCheck {
		if value, ok := message.Value.(*messages.AudioDeltaValue); ok && string(value.Content) == string(expected) {
			return true
		}
	}
	return false
}

const turnTokenCapacity = 16

func testCustomerPCM() []byte  { return []byte{1, 2, 3, 4} }
func testAssistantPCM() []byte { return []byte{5, 6, 7, 8} }
