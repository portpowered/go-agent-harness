package sessionwrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// InstructionsInferencer configures caller-owned session seams with a generic
// SESSION.UPDATE once the provider announces that the session is open.
type InstructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
	tools        []messages.ToolDefinition
}

var _ messages.SessionInferencer = (*InstructionsInferencer)(nil)

// NewInstructionsInferencer snapshots the instructions and definitions.
func NewInstructionsInferencer(inner messages.SessionInferencer, instructions string, definitions []messages.ToolDefinition) *InstructionsInferencer {
	return &InstructionsInferencer{inner: inner, instructions: instructions, tools: messages.CanonicalToolDefinitions(definitions)}
}

// ConnectSession connects the provider and starts the relay.
func (i *InstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newInstructionsSession(ctx, inner, i.instructions, i.tools), nil
}

// instructionsSession relays provider messages and sends one configuration
// update before forwarding the first SESSION.OPEN or SESSION.CREATED.
type instructionsSession struct {
	forwarder
	instructions  string
	tools         []messages.ToolDefinition
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
	done          chan struct{}
	doneOnce      sync.Once
}

var (
	_ messages.Session                  = (*instructionsSession)(nil)
	_ messages.SessionSendOutcomeSender = (*instructionsSession)(nil)
)

func newInstructionsSession(parent context.Context, inner messages.Session, instructions string, definitions []messages.ToolDefinition) *instructionsSession {
	ctx, cancel := context.WithCancel(parent)
	session := &instructionsSession{
		forwarder:    forwarder{inner: inner},
		instructions: instructions,
		tools:        messages.CanonicalToolDefinitions(definitions),
		receive:      messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go session.relay()
	return session
}

func (s *instructionsSession) relay() {
	defer s.markDone()
	innerReceive := s.inner.Receive()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.inner.Done():
			s.drainAfterDone(innerReceive)
			return
		case msg := <-innerReceive.Chan():
			if !s.forward(msg) {
				return
			}
		}
	}
}

func (s *instructionsSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := innerReceive.Read()
		if !ok || !s.forward(msg) {
			return
		}
	}
}

func (s *instructionsSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		if err := s.configure(); err != nil {
			if closeErr := s.inner.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close session after instruction failure: %w", closeErr))
			}
			s.receive.Write(s.ctx, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(err)})
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

// configure sends the configuration update at most once.
func (s *instructionsSession) configure() error {
	var configureErr error
	s.configureOnce.Do(func() {
		outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{
			Type: messages.StreamTypeSessionUpdate,
			Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
				Instructions: s.instructions,
				Tools:        s.tools,
			}),
		})
		if outcome.OK() {
			return
		}
		configureErr = fmt.Errorf("send session instructions: %s", outcome.Status)
		if outcome.Err != nil {
			configureErr = fmt.Errorf("%w: %w", configureErr, outcome.Err)
		}
	})
	return configureErr
}

func (s *instructionsSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

func (s *instructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *instructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *instructionsSession) Done() <-chan struct{} { return s.done }

func (s *instructionsSession) Close() error {
	s.cancel()
	err := s.inner.Close()
	s.markDone()
	return err
}

func (s *instructionsSession) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}
