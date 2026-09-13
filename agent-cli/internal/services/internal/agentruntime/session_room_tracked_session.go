package agentruntime

import (
	"context"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeRoomsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"sync"
)

// Deprecated: retained only as a decision-free adapter for the legacy room host.
type roomTrackedSession struct {
	messages.Session
	lifecycle       *roomParticipantLifecycle
	admissionClosed <-chan struct{}
	delegate        runtimeRooms.TrackedSession
	delegateOnce    sync.Once
}

func (s *roomTrackedSession) delegateSession() runtimeRooms.TrackedSession {
	s.delegateOnce.Do(func() {
		var participant runtimeRooms.ParticipantLifecycle
		if s.lifecycle != nil {
			participant = s.lifecycle.backendLifecycle()
		}
		s.delegate = runtimeRoomsWire.NewTrackedSession(s.Session, participant, s.admissionClosed)
	})
	return s.delegate
}
func trackedValue[T any](s *roomTrackedSession, fallback T, call func(runtimeRooms.TrackedSession) T) T {
	if s == nil {
		return fallback
	}
	if delegate := s.delegateSession(); delegate != nil {
		return call(delegate)
	}
	return fallback
}
func (s *roomTrackedSession) SessionAdmissionClosed() bool {
	return trackedValue(s, false, runtimeRooms.TrackedSession.SessionAdmissionClosed)
}
func (s *roomTrackedSession) SessionAdmissionAllows(msg messages.StreamMessage) bool {
	return trackedValue(s, false, func(d runtimeRooms.TrackedSession) bool { return d.SessionAdmissionAllows(msg) })
}
func (s *roomTrackedSession) SessionAdmissionAllowsCompleteMessage(msg messages.Message) bool {
	return trackedValue(s, false, func(d runtimeRooms.TrackedSession) bool { return d.SessionAdmissionAllowsCompleteMessage(msg) })
}
func (s *roomTrackedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}
func (s *roomTrackedSession) Close() error {
	if s == nil {
		return nil
	}
	if delegate := s.delegateSession(); delegate != nil {
		return delegate.Close()
	}
	return nil
}
func (s *roomTrackedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return trackedValue(s, messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}, func(d runtimeRooms.TrackedSession) messages.SessionSendOutcome { return d.SendWithOutcome(ctx, msg) })
}
func (s *roomTrackedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return trackedValue(s, messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}, func(d runtimeRooms.TrackedSession) messages.SessionSendOutcome { return d.RequestResponse(ctx) })
}
func (s *roomTrackedSession) SupportsResponseRequests() bool {
	return trackedValue(s, false, runtimeRooms.TrackedSession.SupportsResponseRequests)
}
func (s *roomTrackedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	return trackedValue(s, false, func(d runtimeRooms.TrackedSession) bool { return d.SendMessage(ctx, msg) })
}
func (s *roomTrackedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	return trackedValue(s, false, func(d runtimeRooms.TrackedSession) bool { return d.SendMessageWithoutResponse(ctx, msg) })
}
func (s *roomTrackedSession) SupportsCompleteMessages() bool {
	return trackedValue(s, false, runtimeRooms.TrackedSession.SupportsCompleteMessages)
}
func (s *roomTrackedSession) SupportsCompleteMessagesWithoutResponse() bool {
	return trackedValue(s, false, func(d runtimeRooms.TrackedSession) bool { return d.SupportsCompleteMessagesWithoutResponse() })
}
func (s *roomTrackedSession) TerminalError() error {
	delegate := s.delegateSession()
	if delegate == nil {
		return terminalSessionError(s.Session)
	}
	return delegate.TerminalError()
}
func (s *roomTrackedSession) rtcMedia() (RTCMediaEndpoints, bool) {
	if delegate := s.delegateSession(); delegate != nil {
		if media, ok := delegate.RTCMedia(); ok {
			return media, true
		}
	}
	return rtcMediaFromSession(s.Session)
}
