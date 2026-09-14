package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const streamAdmissionBufferCapacity = 1024

// EventAdmission is the single admission boundary for provider
// events. Closing it prevents a provider event from entering the loop after
// the logical deadline while preserving events accepted before the close.
type EventAdmission struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	once   sync.Once
}

func NewEventAdmission() *EventAdmission {
	return &EventAdmission{done: make(chan struct{})}
}

func (a *EventAdmission) close() {
	a.closeWithDrain(nil, nil, nil)
}

func (a *EventAdmission) closeWithDrain(receive, source *messages.TypedBuffer[messages.StreamMessage], onAdmit func(messages.StreamMessage)) {
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

func (a *EventAdmission) admit(ctx context.Context, receive *messages.TypedBuffer[messages.StreamMessage], msg messages.StreamMessage) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	return receive.Write(ctx, msg)
}

// AdmissionInferencer inserts the admission boundary between
// the provider session and the agent loop. The public Session interface exposes
// a concrete receive buffer, so the wrapper forwards through its own buffer and
// can stop admitting provider events without changing the shared interface.
type AdmissionInferencer struct {
	inner      messages.SessionInferencer
	admission  *EventAdmission
	mu         sync.Mutex
	runtimeErr error
	closeErr   error
	connected  bool
	session    *AdmissionSession
	closeDone  chan struct{}
	closeOnce  sync.Once
}

func NewAdmissionInferencer(inner messages.SessionInferencer, admission *EventAdmission, closeDone chan struct{}) *AdmissionInferencer {
	if admission == nil {
		admission = NewEventAdmission()
	}
	if closeDone == nil {
		closeDone = make(chan struct{})
	}
	return &AdmissionInferencer{inner: inner, admission: admission, closeDone: closeDone}
}

func (i *AdmissionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) { //nolint:contextcheck // nil contexts use the session API's documented background behavior.
	if i == nil || i.inner == nil {
		return nil, errors.New("session duration inferencer is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.mu.Lock()
		i.runtimeErr = err
		i.mu.Unlock()
		return nil, err
	}
	i.mu.Lock()
	i.connected = true
	wrapped := NewAdmissionSession(ctx, session, i.admission, i.recordCloseError)
	i.session = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *AdmissionInferencer) recordCloseError(err error) {
	i.mu.Lock()
	i.closeErr = err
	i.mu.Unlock()
	if i.closeDone != nil {
		i.closeOnce.Do(func() { close(i.closeDone) })
	}
}

func (i *AdmissionInferencer) CloseError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closeErr
}

func (i *AdmissionInferencer) RuntimeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runtimeErr
}

func (i *AdmissionInferencer) WaitForClose() {
	i.mu.Lock()
	connected := i.connected
	closeDone := i.closeDone
	i.mu.Unlock()
	if connected && closeDone != nil {
		<-closeDone
	}
}

func (i *AdmissionInferencer) ProviderTerminalMessage() (messages.StreamMessage, bool) {
	if i == nil {
		return messages.StreamMessage{}, false
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session == nil {
		return messages.StreamMessage{}, false
	}
	return session.ProviderTerminalMessage()
}

func (i *AdmissionInferencer) IsProviderTerminalMessage(msg messages.StreamMessage) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	return session != nil && session.IsProviderTerminalMessage(msg)
}

func (i *AdmissionInferencer) CloseAdmission() {
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session != nil {
		session.closeAdmission()
		return
	}
	i.admission.close()
}

type AdmissionSession struct {
	inner     messages.Session
	admission *EventAdmission
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

func NewAdmissionSession(ctx context.Context, inner messages.Session, admission *EventAdmission, onClose func(error)) *AdmissionSession { //nolint:contextcheck // nil contexts use the session API's documented background behavior.
	if ctx == nil {
		ctx = context.Background()
	}
	s := &AdmissionSession{
		inner:     inner,
		admission: admission,
		receive:   messages.NewTypedBuffer[messages.StreamMessage](streamAdmissionBufferCapacity),
		done:      make(chan struct{}),
		onClose:   onClose,
	}
	if inner != nil && inner.Receive() != nil && inner.Done() != nil {
		go s.forward(ctx)
	} else {
		s.closeDone()
	}
	return s
}

func (s *AdmissionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s != nil && s.inner != nil && s.inner.Send(ctx, msg)
}

// RequestResponse forwards the optional explicit response capability while
// retaining the admission wrapper's compatibility with replay sessions.
func (s *AdmissionSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *AdmissionSession) SupportsResponseRequests() bool {
	return s != nil && s.inner != nil && messages.SupportsSessionResponseRequests(s.inner)
}

// SendMessage forwards the optional complete-message capability of the
// wrapped provider session. Duration admission must not hide the rich message
// path used to deliver a tool result on the next model turn.
func (s *AdmissionSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(completeMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages for callers
// that batch more than one tool result before requesting the next response.
func (s *AdmissionSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(completeMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *AdmissionSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *AdmissionSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *AdmissionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *AdmissionSession) Done() <-chan struct{} {
	return s.done
}

func (s *AdmissionSession) RTCMedia() (audio.MediaEndpoints, bool) {
	if owner, ok := s.inner.(interface{ RTCMedia() audio.MediaEndpoints }); ok {
		return owner.RTCMedia(), true
	}
	if owner, ok := s.inner.(interface {
		RTCMedia() (audio.MediaEndpoints, bool)
	}); ok {
		return owner.RTCMedia()
	}
	return audio.MediaEndpoints{}, false
}

func (s *AdmissionSession) TerminalError() error {
	if source, ok := s.inner.(interface{ TerminalError() error }); ok {
		return source.TerminalError()
	}
	return nil
}

func (s *AdmissionSession) ProviderTerminalMessage() (messages.StreamMessage, bool) {
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

func (s *AdmissionSession) IsProviderTerminalMessage(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return false
	}
	s.terminalMu.Lock()
	defer s.terminalMu.Unlock()
	return s.providerTerminalSeen && value == s.providerTerminalValue
}

func (s *AdmissionSession) observeProviderMessage(msg messages.StreamMessage) {
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

func (s *AdmissionSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeAdmission()
		var err error
		if s.inner != nil {
			err = s.inner.Close()
		}
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

func (s *AdmissionSession) closeAdmission() {
	if s == nil || s.admission == nil {
		return
	}
	var source *messages.TypedBuffer[messages.StreamMessage]
	if s.inner != nil {
		source = s.inner.Receive()
	}
	s.admission.closeWithDrain(s.receive, source, s.observeProviderMessage)
}

func IsDurationShutdownMessage(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeSessionClose || isTerminalErrorMessage(msg)
}

func IsDurationForwardMessage(msg messages.StreamMessage) bool {
	return IsDurationShutdownMessage(msg) || msg.Type == messages.StreamTypeError
}

func (s *AdmissionSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}
