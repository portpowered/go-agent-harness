package services

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// roomTrackedSession makes the room's session owner explicit. The model
// runner remains responsible for calling Close, while this decorator records
// the completed ownership boundary and preserves the optional capabilities of
// the underlying provider session.
type roomTrackedSession struct {
	messages.Session
	lifecycle       *roomParticipantLifecycle
	admissionClosed <-chan struct{}
	once            sync.Once
	closeErr        error
}

func (s *roomTrackedSession) SessionAdmissionClosed() bool {
	return s != nil && roomChannelClosed(s.admissionClosed)
}

// SessionAdmissionAllows keeps the room admission boundary selective: ordinary
// input is closed at a bound, while a tool result that was already requested
// and its one continuation request may still drain during grace.
func (s *roomTrackedSession) SessionAdmissionAllows(msg messages.StreamMessage) bool {
	if s == nil {
		return false
	}
	if !s.SessionAdmissionClosed() {
		return true
	}
	switch msg.Type {
	case messages.StreamTypeResponseCancel, messages.StreamTypeSessionClose:
		return true
	default:
		return s.lifecycle != nil && s.lifecycle.admitSessionMessageAfterBound(msg)
	}
}

func (s *roomTrackedSession) SessionAdmissionAllowsCompleteMessage(msg messages.Message) bool {
	if s == nil {
		return false
	}
	if !s.SessionAdmissionClosed() {
		return true
	}
	return s.lifecycle != nil && s.lifecycle.admitCompleteToolResultAfterBound(msg)
}

func (s *roomTrackedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *roomTrackedSession) Close() error {
	if s == nil || s.Session == nil {
		return nil
	}
	s.once.Do(func() {
		s.closeErr = s.Session.Close()
		if s.lifecycle != nil {
			s.lifecycle.markOwnedSessionClosed(s.closeErr)
		}
	})
	return s.closeErr
}

func (s *roomTrackedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllows(msg) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	var outcome messages.SessionSendOutcome
	if sender, ok := s.Session.(messages.SessionSendOutcomeSender); ok {
		outcome = sender.SendWithOutcome(ctx, msg)
	} else {
		outcome = messages.SendSessionWithOutcome(ctx, s.Session, msg)
	}
	if outcome.OK() && s.lifecycle != nil {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			s.lifecycle.recordToolResultSend(s.lifecycle.toolCallID(msg), true, false)
		case messages.StreamTypeResponseCreate:
			s.lifecycle.recordToolContinuationRequest(true)
		case messages.StreamTypeResponseCancel:
			s.lifecycle.recordResponseCancellation()
		}
	}
	return outcome
}

func (s *roomTrackedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllows(messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}
	}
	outcome := messages.RequestSessionResponse(ctx, s.Session)
	if outcome.OK() && s.lifecycle != nil {
		s.lifecycle.recordToolContinuationRequest(true)
	}
	return outcome
}

func (s *roomTrackedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *roomTrackedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(SessionImageMessageSender)
	accepted := ok && sender.SendMessage(ctx, msg)
	if accepted && s.lifecycle != nil {
		s.lifecycle.recordToolResultSend(msg.ToolCallID, true, true)
	}
	return accepted
}

func (s *roomTrackedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	accepted := ok && sender.SendMessageWithoutResponse(ctx, msg)
	if accepted && s.lifecycle != nil {
		s.lifecycle.recordToolResultSend(msg.ToolCallID, true, false)
	}
	return accepted
}

func (s *roomTrackedSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *roomTrackedSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *roomTrackedSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *roomTrackedSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
}
