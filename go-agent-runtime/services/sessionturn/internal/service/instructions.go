package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

type instructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
	tools        []messages.ToolDefinition
}

func newInstructionsInferencer(inner messages.SessionInferencer, instructions string, definitions []messages.ToolDefinition) messages.SessionInferencer {
	return &instructionsInferencer{inner: inner, instructions: instructions, tools: cloneDefinitions(definitions)}
}

func (i *instructionsInferencer) Request() inference.SessionRequest {
	if i == nil || i.inner == nil {
		return inference.SessionRequest{}
	}
	if source, ok := i.inner.(interface {
		Request() inference.SessionRequest
	}); ok {
		request := source.Request()
		request.Config.Instructions = i.instructions
		request.Config.Tools = cloneDefinitions(i.tools)
		return request
	}
	return inference.SessionRequest{}
}

func configureProviderRequest(inferencer messages.SessionInferencer, instructions string, definitions []messages.ToolDefinition) (messages.SessionInferencer, bool) {
	configurer, ok := inferencer.(sessionturn.SessionInferencerConfigurator)
	if !ok {
		return inferencer, false
	}
	configured := configurer.WithSessionInstructionsAndTools(instructions, definitions)
	if configured == nil {
		return inferencer, false
	}
	return configured, true
}

func (i *instructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		return nil, sessionturn.ErrMissingTurnInferencer
	}
	s, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newInstructionsSession(s, ctx, i.instructions, i.tools), nil
}

type instructionsSession struct {
	inner         messages.Session
	instructions  string
	tools         []messages.ToolDefinition
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
	done          chan struct{}
	doneOnce      sync.Once
}

func newInstructionsSession(inner messages.Session, parent context.Context, instructions string, definitions []messages.ToolDefinition) *instructionsSession {
	ctx, cancel := context.WithCancel(parent)
	s := &instructionsSession{inner: inner, instructions: instructions, tools: cloneDefinitions(definitions), receive: messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go s.relay()
	return s
}

func (s *instructionsSession) relay() {
	defer s.doneOnce.Do(func() { close(s.done) })
	in := s.inner.Receive()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.inner.Done():
			for {
				msg, ok := in.Read()
				if !ok || !s.forward(msg) {
					return
				}
			}
		case msg := <-in.Chan():
			if !s.forward(msg) {
				return
			}
		}
	}
}

func (s *instructionsSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		var configureErr error
		s.configureOnce.Do(func() {
			outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Instructions: s.instructions, Tools: s.tools})})
			if !outcome.OK() {
				configureErr = fmt.Errorf("send session instructions: %s", outcome.Status)
				if outcome.Err != nil {
					configureErr = fmt.Errorf("%w: %w", configureErr, outcome.Err)
				}
			}
		})
		if configureErr != nil {
			if closeErr := s.inner.Close(); closeErr != nil {
				configureErr = errors.Join(configureErr, closeErr)
			}
			s.receive.Write(s.ctx, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(configureErr)})
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

func (s *instructionsSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}
func (s *instructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}
func (s *instructionsSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}
func (s *instructionsSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}
func (s *instructionsSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(sessionturn.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}
func (s *instructionsSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}
func (s *instructionsSession) SupportsCompleteMessages() bool {
	if c, ok := s.inner.(sessionturn.CompleteMessageCapabilities); ok {
		return c.SupportsCompleteMessages()
	}
	_, ok := s.inner.(sessionturn.CompleteMessageSender)
	return ok
}
func (s *instructionsSession) SupportsCompleteMessagesWithoutResponse() bool {
	if c, ok := s.inner.(sessionturn.CompleteMessageCapabilities); ok {
		return c.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok := s.inner.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok
}
func (s *instructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *instructionsSession) Done() <-chan struct{} { return s.done }
func (s *instructionsSession) TerminalError() error {
	if source, ok := s.inner.(sessionturn.TerminalErrorSource); ok {
		return source.TerminalError()
	}
	return nil
}

func (s *instructionsSession) RTCMedia() (sharedaudio.MediaEndpoints, bool) {
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
func (s *instructionsSession) Close() error {
	s.cancel()
	err := s.inner.Close()
	s.doneOnce.Do(func() { close(s.done) })
	return err
}
