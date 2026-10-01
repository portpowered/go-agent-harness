package sessionwrap

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
)

type OrderedSessionOptions struct {
	Media             *mediagate.Gate
	FlushOutbound     bool
	OnDispatch        func(messages.StreamMessage)
	OnToolResult      func(string, string, bool) func()
	OnContinuation    func() func()
	OnOpeningAdmitted func()
}

type OrderedSession interface {
	messages.Session
	SendWithOutcome(context.Context, messages.StreamMessage) messages.SessionSendOutcome
}

func WrapOrderedSession(inner messages.Session, options OrderedSessionOptions) OrderedSession {
	return &orderedSession{
		SessionCapabilities: messages.SessionCapabilities{Wrapped: inner},
		inner:               inner, media: options.Media, flushOutbound: options.FlushOutbound,
		onDispatch: options.OnDispatch, onToolResult: options.OnToolResult,
		onContinuation: options.OnContinuation, onOpeningAdmitted: options.OnOpeningAdmitted,
	}
}

type orderedSession struct {
	messages.SessionCapabilities
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
		if err := s.FlushOutbound(ctx); err != nil {
			outcome = sessionSendOutcomeForError(ctx, err)
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
	if operation == nil || ctx == nil {
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
	outcome := messages.SendSessionWithOutcome(ctx, s.inner, msg)
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
	if !messages.SupportsSessionResponseRequests(s.inner) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.runAdmission(ctx, func() messages.SessionSendOutcome {
		rollback := s.beginAdmission(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}, false)
		outcome := messages.RequestSessionResponse(ctx, s.inner)
		if !outcome.OK() {
			rollback()
		}
		if outcome.OK() && s.onDispatch != nil {
			s.onDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		}
		return outcome
	})
}

func (s *orderedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil || !messages.SupportsSessionMessages(s.inner) {
		return false
	}
	return s.sendMessage(ctx, msg, true)
}

func (s *orderedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.inner == nil || !messages.SupportsSessionMessagesWithoutResponse(s.inner) {
		return false
	}
	return s.sendMessage(ctx, msg, false)
}

// sendMessage admits one complete tool result message, requesting the next
// response when requestResponse is set.
func (s *orderedSession) sendMessage(ctx context.Context, msg messages.Message, requestResponse bool) bool {
	return s.runAdmissionBool(ctx, func() bool {
		admission := messages.StreamMessage{
			Type:       messages.StreamTypeToolCallEnd,
			ToolCallId: msg.ToolCallID,
			Value:      messages.NewToolCallEndValue(msg.ToolCallID, msg.Name, ""),
		}
		rollback := s.beginAdmission(admission, requestResponse)
		var accepted bool
		if requestResponse {
			accepted = messages.SendSessionMessage(ctx, s.inner, msg)
		} else {
			accepted = messages.SendSessionMessageWithoutResponse(ctx, s.inner, msg)
		}
		if !accepted {
			rollback()
		}
		if accepted && s.onOpeningAdmitted != nil {
			s.onOpeningAdmitted()
		}
		if accepted && requestResponse && s.onDispatch != nil {
			s.onDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
		}
		return accepted
	})
}
