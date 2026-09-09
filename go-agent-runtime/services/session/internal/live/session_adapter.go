package live

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// capturingInferencer attaches optional provider media after session setup.
type capturingInferencer struct {
	inner              messages.SessionInferencer
	media              *mediagate.Gate
	continuous         bool
	flushOutbound      bool
	onDispatch         func(messages.StreamMessage)
	onToolResult       func(string, string, bool) func()
	onContinuation     func() func()
	onOpeningAdmitted  func()
	onProviderDone     func(error)
	onMediaAttached    func(bool)
	onMediaUnavailable func(error)
	captureMu          sync.Mutex
	captureFlush       func() error
	connectedSession   messages.Session
}

func (i *capturingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.media.Fail(err)
		return nil, err
	}
	if flusher, ok := i.inner.(interface{ FlushCapture() error }); ok {
		i.captureMu.Lock()
		i.captureFlush = flusher.FlushCapture
		i.captureMu.Unlock()
	}
	i.captureMu.Lock()
	i.connectedSession = s
	i.captureMu.Unlock()
	mediaAttached := false
	if providerMedia, ok := s.(sharedaudio.MediaSession); ok {
		endpoints := captureMediaEndpoints(s, providerMedia, i.continuous)
		mediaAttached = endpoints.Inbound != nil
		i.media.Attach(ctx, endpoints)
	}
	if i.onMediaAttached != nil {
		i.onMediaAttached(mediaAttached)
	}
	if !mediaAttached {
		if i.onMediaUnavailable != nil {
			i.onMediaUnavailable(mediagate.ErrMediaUnavailable)
		}
		i.media.Fail(mediagate.ErrMediaUnavailable)
	}
	// Notify the live owner after the provider cleanup boundary, even if the
	// runner context is already canceled.
	if done := s.Done(); done != nil && i.onProviderDone != nil {
		go func() {
			<-done
			i.onProviderDone(i.TerminalError())
		}()
	}
	return &orderedSession{
		inner:             s,
		media:             i.media,
		flushOutbound:     i.flushOutbound,
		onDispatch:        i.onDispatch,
		onToolResult:      i.onToolResult,
		onContinuation:    i.onContinuation,
		onOpeningAdmitted: i.onOpeningAdmitted,
	}, nil
}

// FlushCapture forwards provider capture finalization after session join.
func (i *capturingInferencer) FlushCapture() error {
	if i == nil {
		return nil
	}
	i.captureMu.Lock()
	flush := i.captureFlush
	i.captureMu.Unlock()
	if flush == nil {
		return nil
	}
	return flush()
}

// orderedSession serializes provider ingress with the public media bridge.
type orderedSession struct {
	inner             messages.Session
	media             *mediagate.Gate
	flushOutbound     bool
	onDispatch        func(messages.StreamMessage)
	onToolResult      func(string, string, bool) func()
	onContinuation    func() func()
	onOpeningAdmitted func()
}

func (s *orderedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *orderedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	if ctx == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: errors.New("session send context is required")}
	}
	if s.media == nil {
		return s.sendAutomatic(ctx, msg)
	}
	ackID := msg.ActorProvidedID
	if present, canceled := s.media.ControlState(ackID); present {
		return s.sendMarkedControl(ctx, msg, ackID, canceled)
	}
	if mediagate.IsControlID(ackID) {
		// Teardown may remove a pending marker before the runner observes it.
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	return s.sendAutomatic(ctx, msg)
}

func (s *orderedSession) sendMarkedControl(ctx context.Context, msg messages.StreamMessage, ackID string, canceled bool) messages.SessionSendOutcome {
	if canceled {
		s.media.CancelAck(ackID)
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	release, err := s.media.BeginControl(ctx, ackID)
	if err != nil {
		s.media.CancelAck(ackID)
		return sessionSendOutcomeForContext(err)
	}
	if _, canceled := s.media.ControlState(ackID); canceled {
		release()
		s.media.Acknowledge(ackID, false)
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	// The marker is an admission detail owned by this adapter. Do not expose
	// it to an external provider implementation.
	msg.ActorProvidedID = ""
	outcome := s.sendInner(ctx, msg)
	if outcome.OK() && s.flushOutbound && msg.Type == messages.StreamTypeMessageEnd {
		if flusher, ok := s.inner.(messages.SessionOutboundFlusher); ok {
			if err := flusher.FlushOutbound(ctx); err != nil {
				outcome = sessionSendOutcomeForError(ctx, err)
			}
		}
	}
	release()
	s.media.Acknowledge(ackID, outcome.OK())
	return outcome
}

func (s *orderedSession) sendAutomatic(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return s.runAdmission(ctx, func() messages.SessionSendOutcome {
		return s.sendInner(ctx, msg)
	})
}

func sessionSendOutcomeForContext(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *orderedSession) runAdmission(ctx context.Context, operation func() messages.SessionSendOutcome) messages.SessionSendOutcome {
	if operation == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	if s == nil || s.media == nil {
		return operation()
	}
	release, err := s.media.BeginAutomatic(ctx)
	if err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed, Err: err}
	}
	outcome := operation()
	release()
	return outcome
}

func (s *orderedSession) runAdmissionBool(ctx context.Context, operation func() bool) bool {
	if operation == nil {
		return false
	}
	if ctx == nil {
		return false
	}
	outcome := s.runAdmission(ctx, func() messages.SessionSendOutcome {
		if operation() {
			return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
		}
		if err := ctx.Err(); err != nil {
			return sessionSendOutcomeForContext(err)
		}
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	})
	return outcome.OK()
}

func (s *orderedSession) sendInner(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	rollback := s.beginAdmission(msg, false)
	var outcome messages.SessionSendOutcome
	if sender, ok := s.inner.(messages.SessionSendOutcomeSender); ok {
		outcome = sender.SendWithOutcome(ctx, msg)
	} else if s.inner.Send(ctx, msg) {
		outcome = messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	} else if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
		} else {
			outcome = messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
		}
	} else {
		outcome = messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	if !outcome.OK() {
		rollback()
	}
	if outcome.OK() && s.onDispatch != nil {
		// Notify the owner only after removing the private admission marker.
		s.onDispatch(msg)
	}
	return outcome
}

func (s *orderedSession) beginAdmission(msg messages.StreamMessage, completeMessage bool) func() {
	if s == nil {
		return func() {}
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		if s.onToolResult == nil {
			return func() {}
		}
		value, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok || value == nil {
			return func() {}
		}
		callID := value.ToolCallID
		if callID == "" {
			callID = msg.ToolCallId
		}
		return s.onToolResult(callID, value.Name, completeMessage)
	}
	if msg.Type == messages.StreamTypeResponseCreate && s.onContinuation != nil {
		return s.onContinuation()
	}
	return func() {}
}

func (s *orderedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Receive()
}

func (s *orderedSession) Done() <-chan struct{} {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Done()
}

func (s *orderedSession) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *orderedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	requester, ok := s.inner.(messages.SessionResponseRequester)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.runAdmission(ctx, func() messages.SessionSendOutcome {
		rollback := s.beginAdmission(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}, false)
		outcome := requester.RequestResponse(ctx)
		if !outcome.OK() {
			rollback()
		}
		if outcome.OK() && s.onDispatch != nil {
			s.onDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		}
		return outcome
	})
}

func (s *orderedSession) SupportsResponseRequests() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(messages.SessionResponseCapability)
	if ok {
		return capability.SupportsResponseRequests()
	}
	_, ok = s.inner.(messages.SessionResponseRequester)
	return ok
}

type completeMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

type completeMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

func (s *orderedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(completeMessageSender)
	return ok && s.runAdmissionBool(ctx, func() bool {
		admission := messages.StreamMessage{
			Type:       messages.StreamTypeToolCallEnd,
			ToolCallId: msg.ToolCallID,
			Value:      messages.NewToolCallEndValue(msg.ToolCallID, msg.Name, ""),
		}
		rollback := s.beginAdmission(admission, true)
		accepted := sender.SendMessage(ctx, msg)
		if !accepted {
			rollback()
		}
		if accepted && s.onOpeningAdmitted != nil {
			s.onOpeningAdmitted()
		}
		if accepted && s.onDispatch != nil {
			s.onDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		}
		return accepted
	})
}

func (s *orderedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(completeMessageWithoutResponseSender)
	return ok && s.runAdmissionBool(ctx, func() bool {
		admission := messages.StreamMessage{
			Type:       messages.StreamTypeToolCallEnd,
			ToolCallId: msg.ToolCallID,
			Value:      messages.NewToolCallEndValue(msg.ToolCallID, msg.Name, ""),
		}
		rollback := s.beginAdmission(admission, false)
		accepted := sender.SendMessageWithoutResponse(ctx, msg)
		if !accepted {
			rollback()
		}
		if accepted && s.onOpeningAdmitted != nil {
			s.onOpeningAdmitted()
		}
		return accepted
	})
}

func (s *orderedSession) SupportsCompleteMessages() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capability.SupportsCompleteMessages()
}

func (s *orderedSession) SupportsCompleteMessagesWithoutResponse() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	return ok && capability.SupportsCompleteMessagesWithoutResponse()
}

func (s *orderedSession) InitialSessionConfigSent() bool {
	if s == nil || s.inner == nil {
		return false
	}
	marker, ok := s.inner.(interface{ InitialSessionConfigSent() bool })
	return ok && marker.InitialSessionConfigSent()
}

// TerminalError reads the joined provider state directly. Final classification
// must not depend on when the asynchronous Done notification gets scheduled.
func (i *capturingInferencer) TerminalError() error {
	i.captureMu.Lock()
	connected := i.connectedSession
	i.captureMu.Unlock()
	if provider, ok := connected.(interface{ TerminalError() error }); ok {
		return provider.TerminalError()
	}
	return nil
}
