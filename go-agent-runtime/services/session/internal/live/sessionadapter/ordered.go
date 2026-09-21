package sessionadapter

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
)

// OrderedSession serializes provider ingress with the public media bridge.
type OrderedSession struct {
	inner             messages.Session
	media             *mediagate.Gate
	flushOutbound     bool
	onDispatch        func(messages.StreamMessage)
	onToolResult      func(string, string, bool) func()
	onContinuation    func() func()
	onOpeningAdmitted func()
}

// NewOrderedSession wraps a provider session with media admission ordering.
func NewOrderedSession(inner messages.Session, media *mediagate.Gate) *OrderedSession {
	return &OrderedSession{inner: inner, media: media}
}

func newOrderedSession(inner messages.Session, media *mediagate.Gate, flushOutbound bool, onDispatch func(messages.StreamMessage), onToolResult func(string, string, bool) func(), onContinuation func() func(), onOpeningAdmitted func()) *OrderedSession {
	return &OrderedSession{
		inner: inner, media: media, flushOutbound: flushOutbound,
		onDispatch: onDispatch, onToolResult: onToolResult,
		onContinuation: onContinuation, onOpeningAdmitted: onOpeningAdmitted,
	}
}

func (s *OrderedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *OrderedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
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

func (s *OrderedSession) sendMarkedControl(ctx context.Context, msg messages.StreamMessage, ackID string, canceled bool) messages.SessionSendOutcome {
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

func (s *OrderedSession) sendAutomatic(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
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

func sessionSendOutcomeForError(ctx context.Context, err error) messages.SessionSendOutcome {
	if err == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
}

func (s *OrderedSession) runAdmission(ctx context.Context, operation func() messages.SessionSendOutcome) messages.SessionSendOutcome {
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

func (s *OrderedSession) runAdmissionBool(ctx context.Context, operation func() bool) bool {
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

func (s *OrderedSession) sendInner(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
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

func (s *OrderedSession) beginAdmission(msg messages.StreamMessage, completeMessage bool) func() {
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

func (s *OrderedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Receive()
}

func (s *OrderedSession) Done() <-chan struct{} {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Done()
}

func (s *OrderedSession) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *OrderedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
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

func (s *OrderedSession) SupportsResponseRequests() bool {
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

type CompleteMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

type CompleteMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

func (s *OrderedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(CompleteMessageSender)
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

func (s *OrderedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil {
		return false
	}
	sender, ok := s.inner.(CompleteMessageWithoutResponseSender)
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

func (s *OrderedSession) SupportsCompleteMessages() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capability.SupportsCompleteMessages()
}

func (s *OrderedSession) SupportsCompleteMessagesWithoutResponse() bool {
	if s == nil || s.inner == nil {
		return false
	}
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	return ok && capability.SupportsCompleteMessagesWithoutResponse()
}

func (s *OrderedSession) InitialSessionConfigSent() bool {
	if s == nil || s.inner == nil {
		return false
	}
	marker, ok := s.inner.(interface{ InitialSessionConfigSent() bool })
	return ok && marker.InitialSessionConfigSent()
}
