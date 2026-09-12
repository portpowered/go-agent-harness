package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
)

type decoratedSession struct {
	inner         messages.Session
	config        sessionupdate.Config
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	doneOnce      sync.Once
	closeOnce     sync.Once
	closeErr      error
	configureOnce sync.Once
	configureErr  error
	relayWG       sync.WaitGroup
}

var _ sessionupdate.DecoratedSession = (*decoratedSession)(nil)

func newSession(inner messages.Session, ctx context.Context, config sessionupdate.Config) *decoratedSession {
	ctx, cancel := context.WithCancel(nonNilContext(ctx))
	innerReceive := inner.Receive()
	capacity := 0
	if innerReceive != nil {
		capacity = innerReceive.Cap()
	}
	s := &decoratedSession{
		inner:   inner,
		config:  config,
		receive: messages.NewTypedBuffer[messages.StreamMessage](capacity),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	s.relayWG.Add(1)
	go s.relay(innerReceive)
	return s
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *decoratedSession) relay(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
	defer s.relayWG.Done()
	defer s.markDone()
	if innerReceive == nil {
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			if closeErr := s.closeInner(); closeErr != nil {
				s.publishConfigurationFailure(closeErr)
			}
			return
		case <-s.inner.Done():
			s.drainAfterDone(innerReceive)
			return
		case msg, ok := <-innerReceive.Chan():
			if !ok || !s.forward(msg) {
				return
			}
		}
	}
}

func (s *decoratedSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := innerReceive.Read()
		if !ok || !s.forward(msg) {
			return
		}
	}
}

func (s *decoratedSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		s.configureOnce.Do(func() { s.configureErr = s.configureSession() })
		if s.configureErr != nil {
			s.publishConfigurationFailure(s.configureErr)
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

func (s *decoratedSession) configureSession() error {
	outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{
		Type: messages.StreamTypeSessionUpdate,
		Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
			Instructions: s.config.Instructions,
			Tools:        s.config.ToolDefinitions,
		}),
	})
	if outcome.OK() {
		return nil
	}
	updateErr := error(&sessionupdate.UpdateSendError{Status: outcome.Status, Err: outcome.Err})
	if closeErr := s.closeInner(); closeErr != nil {
		updateErr = errors.Join(updateErr, fmt.Errorf("close session after instruction failure: %w", closeErr))
	}
	return fmt.Errorf("send session instructions: %w", updateErr)
}

func (s *decoratedSession) publishConfigurationFailure(err error) {
	s.receive.WriteTerminal(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValueWithError(err),
	})
}

func (s *decoratedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

func (s *decoratedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *decoratedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *decoratedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *decoratedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(sessionupdate.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *decoratedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(sessionupdate.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *decoratedSession) SupportsCompleteMessages() bool {
	if capabilities, ok := s.inner.(sessionupdate.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessages()
	}
	_, ok := s.inner.(sessionupdate.CompleteMessageSender)
	return ok
}

func (s *decoratedSession) SupportsCompleteMessagesWithoutResponse() bool {
	if capabilities, ok := s.inner.(sessionupdate.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok := s.inner.(sessionupdate.CompleteMessageWithoutResponseSender)
	return ok
}

func (s *decoratedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *decoratedSession) Done() <-chan struct{} {
	return s.done
}

func (s *decoratedSession) TerminalError() error {
	source, ok := s.inner.(sessionupdate.TerminalErrorSource)
	if !ok {
		return nil
	}
	return source.TerminalError()
}

func (s *decoratedSession) Close() error {
	s.cancel()
	err := s.closeInner()
	s.relayWG.Wait()
	return err
}

func (s *decoratedSession) closeInner() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.inner.Close()
	})
	return s.closeErr
}

func (s *decoratedSession) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}
