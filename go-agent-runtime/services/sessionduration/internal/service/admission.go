package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
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

func (s *Service) NewAdmissionInferencer(inner messages.SessionInferencer, admission sessionduration.EventAdmission, closeDone chan struct{}) sessionduration.AdmissionInferencer {
	boundary, ok := admission.(*EventAdmission)
	if !ok {
		boundary = nil
	}
	return NewAdmissionInferencer(inner, boundary, closeDone)
}

func (s *Service) NewAdmissionSession(ctx context.Context, inner messages.Session, admission sessionduration.EventAdmission, onClose func(error)) sessionduration.AdmissionSession {
	boundary, ok := admission.(*EventAdmission)
	if !ok {
		boundary = nil
	}
	return NewAdmissionSession(ctx, inner, boundary, onClose)
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

func (c *controller) observeLivenessLocked(msg messages.StreamMessage) (arm, reset, disarm bool, failure error) {
	if !c.options.Liveness.Enabled || msg.Role == messages.RoleTool || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return false, false, false, nil
	}
	switch {
	case msg.Type == messages.StreamTypeMessageStart:
		arm = true
	case msg.Type == messages.StreamTypeResponseCreate:
		arm = true
	case msg.Type == messages.StreamTypeMessageEnd:
		if c.isEmptyResponseLocked(msg) {
			failure = c.makeLivenessErrorLocked(msg, false)
		}
		disarm = true
	case msg.Type == messages.StreamTypeSessionOpen:
		// The provider has not started a response yet.
	case isProviderOutput(msg) || msg.Type == messages.StreamTypeToolCallStart || msg.Type == messages.StreamTypeToolCallDelta || msg.Type == messages.StreamTypeToolCallEnd:
		reset = true
	case msg.Type == messages.StreamTypeError:
		disarm = true
	default:
		// Provider metadata and unrelated stream messages do not affect liveness.
	}
	return arm, reset, disarm, failure
}

func (c *controller) observeOutputLocked(msg messages.StreamMessage) {
	//nolint:exhaustive // only response output boundaries affect this state.
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		c.responseOutput = false
		c.responseComplete = false
		c.toolObligation = false
	case messages.StreamTypeTextDelta, messages.StreamTypeReasoningDelta, messages.StreamTypeAudioDelta, messages.StreamTypeImageDelta, messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta, messages.StreamTypeEmbeddingDelta, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd, messages.StreamTypeRefusal:
		if msg.Role != messages.RoleUser && msg.Role != messages.RoleTool {
			c.responseOutput = true
		}
	case messages.StreamTypeTranscriptDelta:
		if msg.Role != messages.RoleUser && msg.Role != messages.RoleTool {
			c.responseOutput = true
		}
	case messages.StreamTypeToolCallStart:
		c.toolObligation = true
	case messages.StreamTypeMessageEnd:
		c.responseComplete = true
	default:
		// Non-response messages do not change terminal output state.
	}
	if !c.responseOutput {
		c.outputState = messages.TerminalOutputNone
	} else if c.responseComplete {
		c.outputState = messages.TerminalOutputComplete
	} else {
		c.outputState = messages.TerminalOutputPartial
	}
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
	deferClose bool
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
	if i.deferClose {
		wrapped.deferCloseUntilFinalization()
	}
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

func (i *AdmissionInferencer) deferSessionCloseUntilFinalization() {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.deferClose = true
	session := i.session
	i.mu.Unlock()
	if session != nil {
		session.deferCloseUntilFinalization()
	}
}

func (i *AdmissionInferencer) finalizeSessionClose() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session == nil {
		return nil
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), defaultDrainWallSafety)
	defer cancel()
	drainErr := session.DrainPlayback(drainCtx)
	session.releaseDeferredClose()
	return errors.Join(drainErr, session.Close())
}
