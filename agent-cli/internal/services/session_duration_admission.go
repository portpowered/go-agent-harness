package services

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const sessionDurationAdmissionBufferCapacity = 1024

// sessionDurationAdmission is the single admission boundary for provider
// events. Closing it prevents a provider event from entering the loop after
// the logical deadline while preserving events accepted before the close.
type sessionDurationAdmission struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	once   sync.Once
}

func newSessionDurationAdmission() *sessionDurationAdmission {
	return &sessionDurationAdmission{done: make(chan struct{})}
}

func (a *sessionDurationAdmission) close() {
	a.closeWithDrain(nil, nil, nil)
}

func (a *sessionDurationAdmission) closeWithDrain(receive, source *messages.TypedBuffer[messages.StreamMessage], onAdmit func(messages.StreamMessage)) {
	a.once.Do(func() {
		a.mu.Lock()
		if receive != nil && source != nil {
			for {
				msg, ok := source.Read()
				if !ok || !receive.Write(context.Background(), msg) {
					break
				}
				if onAdmit != nil {
					onAdmit(msg)
				}
			}
		}
		a.closed = true
		a.mu.Unlock()
		close(a.done)
	})
}

func (a *sessionDurationAdmission) admit(receive *messages.TypedBuffer[messages.StreamMessage], msg messages.StreamMessage) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	return receive.Write(context.Background(), msg)
}

// sessionDurationAdmissionInferencer inserts the admission boundary between
// the provider session and the agent loop. The public Session interface exposes
// a concrete receive buffer, so the wrapper forwards through its own buffer and
// can stop admitting provider events without changing the shared interface.
type sessionDurationAdmissionInferencer struct {
	inner      messages.SessionInferencer
	admission  *sessionDurationAdmission
	mu         sync.Mutex
	runtimeErr error
	closeErr   error
	connected  bool
	session    *sessionDurationAdmissionSession
	closeDone  chan struct{}
	closeOnce  sync.Once
}

func (i *sessionDurationAdmissionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.mu.Lock()
		i.runtimeErr = err
		i.mu.Unlock()
		return nil, err
	}
	i.mu.Lock()
	i.connected = true
	wrapped := newSessionDurationAdmissionSession(ctx, session, i.admission, i.recordCloseError)
	i.session = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionDurationAdmissionInferencer) recordCloseError(err error) {
	i.mu.Lock()
	i.closeErr = err
	i.mu.Unlock()
	if i.closeDone != nil {
		i.closeOnce.Do(func() { close(i.closeDone) })
	}
}

func (i *sessionDurationAdmissionInferencer) closeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closeErr
}

func (i *sessionDurationAdmissionInferencer) runtimeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runtimeErr
}

func (i *sessionDurationAdmissionInferencer) waitForClose() {
	i.mu.Lock()
	connected := i.connected
	closeDone := i.closeDone
	i.mu.Unlock()
	if connected && closeDone != nil {
		<-closeDone
	}
}

func (i *sessionDurationAdmissionInferencer) providerTerminalMessage() (messages.StreamMessage, bool) {
	if i == nil {
		return messages.StreamMessage{}, false
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session == nil {
		return messages.StreamMessage{}, false
	}
	return session.providerTerminalMessage()
}

func (i *sessionDurationAdmissionInferencer) isProviderTerminalMessage(msg messages.StreamMessage) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	return session != nil && session.isProviderTerminalMessage(msg)
}

func (i *sessionDurationAdmissionInferencer) closeAdmission() {
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session != nil {
		session.closeAdmission()
		return
	}
	i.admission.close()
}

type sessionDurationAdmissionSession struct {
	inner     messages.Session
	admission *sessionDurationAdmission
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeMu   sync.Mutex
	closeErr  error
	onClose   func(error)

	terminalMu            sync.Mutex
	providerTerminal      messages.StreamMessage
	providerTerminalValue *messages.SessionCloseValue
	providerTerminalSeen  bool
}

func newSessionDurationAdmissionSession(ctx context.Context, inner messages.Session, admission *sessionDurationAdmission, onClose func(error)) *sessionDurationAdmissionSession {
	s := &sessionDurationAdmissionSession{
		inner:     inner,
		admission: admission,
		receive:   messages.NewTypedBuffer[messages.StreamMessage](sessionDurationAdmissionBufferCapacity),
		done:      make(chan struct{}),
		onClose:   onClose,
	}
	go s.forward(ctx)
	return s
}

func (s *sessionDurationAdmissionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

// SendMessage forwards the optional complete-message capability of the
// wrapped provider session. Duration admission must not hide the rich message
// path used to deliver a tool result on the next model turn.
func (s *sessionDurationAdmissionSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages for callers
// that batch more than one tool result before requesting the next response.
func (s *sessionDurationAdmissionSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionDurationAdmissionSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionDurationAdmissionSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionDurationAdmissionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionDurationAdmissionSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionDurationAdmissionSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionDurationAdmissionSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionDurationAdmissionSession) providerTerminalMessage() (messages.StreamMessage, bool) {
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

func (s *sessionDurationAdmissionSession) isProviderTerminalMessage(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return false
	}
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	return s.providerTerminalSeen && value == s.providerTerminalValue
}

func (s *sessionDurationAdmissionSession) observeProviderMessage(msg messages.StreamMessage) {
	if msg.Type != messages.StreamTypeSessionClose {
		return
	}
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
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
	s.providerTerminal = messages.StreamMessage{
		Type:  msg.Type,
		Role:  msg.Role,
		Value: &clone,
	}
	s.providerTerminalValue = value
	s.providerTerminalSeen = true
}

func (s *sessionDurationAdmissionSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeAdmission()
		err := s.inner.Close()
		// A provider may publish its terminal close while servicing Close. The
		// forwarding goroutine can already have observed the runner context's
		// cancellation by then, so make one final source drain after the inner
		// close to retain that provider-authored terminal without reopening
		// ordinary event admission.
		s.drainSourceAfterClose()
		s.closeMu.Lock()
		s.closeErr = err
		s.closeMu.Unlock()
		if s.onClose != nil {
			s.onClose(err)
		}
	})
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *sessionDurationAdmissionSession) closeAdmission() {
	s.admission.closeWithDrain(s.receive, s.inner.Receive(), s.observeProviderMessage)
}

func (s *sessionDurationAdmissionSession) drainSourceAfterClose() {
	source := s.inner.Receive()
	for {
		msg, ok := source.Read()
		if !ok {
			return
		}
		s.observeProviderMessage(msg)
		if isDurationShutdownMessage(msg) {
			s.receive.Write(context.Background(), msg)
		}
	}
}

func (s *sessionDurationAdmissionSession) forward(ctx context.Context) {
	source := s.inner.Receive()
	sourceCh := source.Chan()
	admissionDone := s.admission.done
	admissionOpen := true
	for {
		select {
		case <-s.inner.Done():
			s.drainSource(source, admissionOpen)
			s.closeDone()
			return
		case <-ctx.Done():
			s.closeDone()
			return
		case <-admissionDone:
			// The deadline closes ordinary event admission, but the provider
			// session must stay alive long enough to receive a terminal event
			// during graceful shutdown. Keep reading the source and forward only
			// terminal/error messages after this boundary.
			admissionOpen = false
			admissionDone = nil
		case msg, ok := <-sourceCh:
			if !ok {
				s.closeDone()
				return
			}
			s.observeProviderMessage(msg)
			if admissionOpen {
				if !s.admission.admit(s.receive, msg) {
					admissionOpen = false
					if isDurationShutdownMessage(msg) {
						s.receive.Write(context.Background(), msg)
					}
				}
				continue
			}
			if isDurationShutdownMessage(msg) {
				s.receive.Write(context.Background(), msg)
			}
		}
	}
}

func (s *sessionDurationAdmissionSession) drainSource(source *messages.TypedBuffer[messages.StreamMessage], admissionOpen bool) {
	for {
		msg, ok := source.Read()
		if !ok {
			return
		}
		s.observeProviderMessage(msg)
		if admissionOpen {
			if s.admission.admit(s.receive, msg) {
				continue
			}
			admissionOpen = false
		}
		if isDurationShutdownMessage(msg) {
			s.receive.Write(context.Background(), msg)
		}
	}
}

func isDurationShutdownMessage(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeSessionClose || msg.Type == messages.StreamTypeError
}

func (s *sessionDurationAdmissionSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

var _ messages.SessionInferencer = (*sessionDurationAdmissionInferencer)(nil)
var _ messages.Session = (*sessionDurationAdmissionSession)(nil)
