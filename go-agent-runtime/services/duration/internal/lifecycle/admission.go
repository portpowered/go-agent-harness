package lifecycle

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const admissionBufferCapacity = 1024

// admission is the single provider-event boundary. It closes ordinary
// admission at the deadline while retaining terminal/error messages emitted
// during graceful provider shutdown.
type admission struct {
	inner messages.SessionInferencer
	gate  *admissionGate

	mu         sync.Mutex
	runtimeErr error
	closeErr   error
	connected  bool
	session    *admittedSession
	closeDone  chan struct{}
	closeOnce  sync.Once
}

func newAdmission(inner messages.SessionInferencer) *admission {
	return &admission{inner: inner, gate: &admissionGate{done: make(chan struct{})}, closeDone: make(chan struct{})}
}

func (a *admission) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := a.inner.ConnectSession(ctx)
	if err != nil {
		a.mu.Lock()
		a.runtimeErr = err
		a.mu.Unlock()
		return nil, err
	}
	wrapped := newAdmittedSession(ctx, session, a.gate, a.recordCloseError)
	a.mu.Lock()
	a.connected = true
	a.session = wrapped
	a.mu.Unlock()
	return wrapped, nil
}

func (a *admission) recordCloseError(err error) {
	a.mu.Lock()
	a.closeErr = err
	a.mu.Unlock()
	a.closeOnce.Do(func() { close(a.closeDone) })
}

func (a *admission) Close(ctx context.Context) {
	ctx = nonNilContext(ctx)
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	if session != nil {
		session.closeAdmission(ctx)
		return
	}
	a.gate.close()
}

func (a *admission) Wait() {
	a.mu.Lock()
	connected := a.connected
	closeDone := a.closeDone
	session := a.session
	a.mu.Unlock()
	if connected {
		<-closeDone
		if session != nil {
			<-session.done
		}
	}
}

func (a *admission) RuntimeError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runtimeErr
}

func (a *admission) CloseError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closeErr
}

func (a *admission) IsProviderTerminal(msg messages.StreamMessage) bool {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	return session != nil && session.isProviderTerminalMessage(msg)
}

func (a *admission) ProviderTerminal() (messages.StreamMessage, bool) {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	if session == nil {
		return messages.StreamMessage{}, false
	}
	return session.providerTerminalMessage()
}

type admissionGate struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	once   sync.Once
}

func (g *admissionGate) close() {
	g.once.Do(func() {
		g.mu.Lock()
		g.closed = true
		g.mu.Unlock()
		close(g.done)
	})
}

func (g *admissionGate) admit(ctx context.Context, receive *messages.TypedBuffer[messages.StreamMessage], msg messages.StreamMessage) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	return receive.Write(ctx, msg)
}

type admittedSession struct {
	inner             messages.Session
	gate              *admissionGate
	receive           *messages.TypedBuffer[messages.StreamMessage]
	done              chan struct{}
	doneOnce          sync.Once
	closeOnce         sync.Once
	closeMu           sync.Mutex
	closeErr          error
	closeStarted      bool
	closeFinished     chan struct{}
	closeFinishedOnce sync.Once
	onClose           func(error)

	terminalMu            sync.Mutex
	providerTerminal      messages.StreamMessage
	providerTerminalValue *messages.SessionCloseValue
	providerTerminalSeen  bool
}

func newAdmittedSession(ctx context.Context, inner messages.Session, gate *admissionGate, onClose func(error)) *admittedSession {
	s := &admittedSession{inner: inner, gate: gate, receive: messages.NewTypedBuffer[messages.StreamMessage](admissionBufferCapacity), done: make(chan struct{}), closeFinished: make(chan struct{}), onClose: onClose}
	go s.forward(ctx)
	return s
}

func (s *admittedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

func (s *admittedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *admittedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *admittedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}

func (s *admittedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *admittedSession) SupportsCompleteMessages() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	if ok {
		return capability.SupportsCompleteMessages()
	}
	_, ok = s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok
}

func (s *admittedSession) SupportsCompleteMessagesWithoutResponse() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	if ok {
		return capability.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok = s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok
}

func (s *admittedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *admittedSession) Done() <-chan struct{}                                  { return s.done }

// RTCMedia forwards the optional audio media capability through the generic
// admission decorator so a host device adapter can retain its own ownership.
func (s *admittedSession) RTCMedia() audio.MediaEndpoints {
	if source, ok := s.inner.(interface{ RTCMedia() audio.MediaEndpoints }); ok {
		return source.RTCMedia()
	}
	return audio.MediaEndpoints{}
}

func (s *admittedSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		s.closeStarted = true
		s.closeMu.Unlock()
		s.closeAdmission(context.Background())
		err := s.inner.Close()
		s.drainSourceAfterClose()
		s.closeMu.Lock()
		s.closeErr = err
		s.closeMu.Unlock()
		s.closeFinishedOnce.Do(func() { close(s.closeFinished) })
		if s.onClose != nil {
			s.onClose(err)
		}
	})
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *admittedSession) closeAdmission(ctx context.Context) {
	s.gate.close()
	for {
		msg, ok := s.inner.Receive().Read()
		if !ok {
			return
		}
		s.observeProviderMessage(msg)
		if !s.isShutdownMessage(msg) {
			continue
		}
		_ = s.receive.Write(ctx, msg)
	}
}

func (s *admittedSession) drainSourceAfterClose() {
	for {
		msg, ok := s.inner.Receive().Read()
		if !ok {
			return
		}
		s.observeProviderMessage(msg)
		if s.isShutdownMessage(msg) {
			_ = s.receive.Write(context.Background(), msg)
		}
	}
}

func (s *admittedSession) observeProviderMessage(msg messages.StreamMessage) {
	if msg.Type != messages.StreamTypeSessionClose {
		return
	}
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		return
	}
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	if s.providerTerminalSeen {
		return
	}
	clone := *value
	if clone.TerminalProvenance == "" {
		clone.TerminalProvenance = messages.TerminalProvenanceProvider
	}
	s.providerTerminal = messages.StreamMessage{Type: msg.Type, Role: msg.Role, Value: &clone}
	s.providerTerminalValue = value
	s.providerTerminalSeen = true
}

func (s *admittedSession) providerTerminalMessage() (messages.StreamMessage, bool) {
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	if !s.providerTerminalSeen {
		return messages.StreamMessage{}, false
	}
	msg := s.providerTerminal
	if value, ok := msg.Value.(*messages.SessionCloseValue); ok {
		clone := *value
		msg.Value = &clone
	}
	return msg, true
}

func (s *admittedSession) isProviderTerminalMessage(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return false
	}
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	return s.providerTerminalSeen && value == s.providerTerminalValue
}

func (s *admittedSession) isShutdownMessage(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		return true
	}
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

var _ messages.SessionInferencer = (*admission)(nil)
var _ messages.Session = (*admittedSession)(nil)
