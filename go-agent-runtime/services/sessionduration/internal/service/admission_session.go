package service

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type AdmissionSession struct {
	inner      messages.Session
	admission  *EventAdmission
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	doneOnce   sync.Once
	closeOnce  sync.Once
	drainOnce  sync.Once
	closeMu    sync.Mutex
	closeErr   error
	drainErr   error
	onClose    func(error)
	deferClose bool

	terminalMu            sync.Mutex
	providerTerminal      messages.StreamMessage
	providerTerminalValue *messages.SessionCloseValue
	providerTerminalSeen  bool
}

type sessionAdmissionController interface {
	SessionAdmissionClosed() bool
}

type sessionAdmissionPolicy interface {
	SessionAdmissionAllows(messages.StreamMessage) bool
}

type completeMessageAdmissionPolicy interface {
	SessionAdmissionAllowsCompleteMessage(messages.Message) bool
}

func (s *AdmissionSession) SessionAdmissionClosed() bool {
	if s == nil || s.inner == nil {
		return false
	}
	controller, ok := s.inner.(sessionAdmissionController)
	return ok && controller.SessionAdmissionClosed()
}

func (s *AdmissionSession) SessionAdmissionAllows(msg messages.StreamMessage) bool {
	if s == nil || s.inner == nil {
		return false
	}
	if policy, ok := s.inner.(sessionAdmissionPolicy); ok {
		return policy.SessionAdmissionAllows(msg)
	}
	if s.SessionAdmissionClosed() {
		return msg.Type == messages.StreamTypeResponseCancel || msg.Type == messages.StreamTypeSessionClose
	}
	return true
}

func (s *AdmissionSession) SessionAdmissionAllowsCompleteMessage(msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	if policy, ok := s.inner.(completeMessageAdmissionPolicy); ok {
		return policy.SessionAdmissionAllowsCompleteMessage(msg)
	}
	return !s.SessionAdmissionClosed()
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
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome preserves the provider's typed admission result through the
// duration boundary. The receive-side EventAdmission controls inbound
// lifecycle events; outbound tool-result sends must retain buffer-full and
// closed distinctions for terminal diagnostics.
func (s *AdmissionSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
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

func (s *AdmissionSession) DrainPlayback(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.drainOnce.Do(func() {
		if s.inner == nil {
			return
		}
		drainer, ok := s.inner.(sessionduration.PlaybackDrainer)
		if ok {
			s.drainErr = drainer.DrainPlayback(ctx)
		}
	})
	return s.drainErr
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
	s.closeMu.Lock()
	deferred := s.deferClose
	s.closeMu.Unlock()
	if deferred {
		s.closeAdmission()
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

func (s *AdmissionSession) deferCloseUntilFinalization() {
	if s == nil {
		return
	}
	s.closeMu.Lock()
	s.deferClose = true
	s.closeMu.Unlock()
}

func (s *AdmissionSession) releaseDeferredClose() {
	if s == nil {
		return
	}
	s.closeMu.Lock()
	s.deferClose = false
	s.closeMu.Unlock()
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
