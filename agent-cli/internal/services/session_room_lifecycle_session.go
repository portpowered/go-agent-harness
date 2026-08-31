package services

import (
	"context"
	"errors"
	"fmt"
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

// roomConnectTrackingInferencer preserves the existing SessionInferencer
// contract while exposing the first ConnectSession outcome to the room's
// initial-start barrier.
type roomConnectTrackingInferencer struct {
	inner      messages.SessionInferencer
	result     chan error
	outcomes   chan<- roomConnectionOutcome
	once       sync.Once
	mu         sync.Mutex
	ready      bool
	connectErr error
	lifecycle  *roomParticipantLifecycle
}

type roomConnectionOutcome struct {
	tracker *roomConnectTrackingInferencer
	err     error
}

func newRoomConnectTrackingInferencer(inner messages.SessionInferencer) *roomConnectTrackingInferencer {
	return &roomConnectTrackingInferencer{inner: inner, result: make(chan error, 1)}
}

func (i *roomConnectTrackingInferencer) setOutcomeSink(outcomes chan<- roomConnectionOutcome) {
	if i == nil {
		return
	}
	i.outcomes = outcomes
}

func (i *roomConnectTrackingInferencer) publish(err error) {
	if i == nil {
		return
	}
	i.once.Do(func() {
		i.mu.Lock()
		i.ready = true
		i.connectErr = err
		i.mu.Unlock()
		i.result <- err
		if i.outcomes != nil {
			i.outcomes <- roomConnectionOutcome{tracker: i, err: err}
		}
	})
}

func (i *roomConnectTrackingInferencer) outcome() (error, bool) {
	if i == nil {
		return nil, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectErr, i.ready
}

func (i *roomConnectTrackingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		err := errors.New("room participant session inferencer is nil")
		if i != nil {
			i.publish(err)
		}
		return nil, err
	}
	session, err := i.inner.ConnectSession(ctx)
	if err == nil && session == nil {
		err = errors.New("room participant session is nil")
	}
	if err != nil {
		if session != nil {
			if i.lifecycle != nil {
				i.lifecycle.markSessionCreated()
			}
			var admissionClosed <-chan struct{}
			if i.lifecycle != nil {
				admissionClosed = i.lifecycle.admissionClosed
			}
			tracked := &roomTrackedSession{Session: session, lifecycle: i.lifecycle, admissionClosed: admissionClosed}
			if i.lifecycle != nil {
				i.lifecycle.setOwnedSession(tracked)
				terminalError := func() error { return terminalSessionError(tracked) }
				i.lifecycle.setTransportDone(tracked.Done(), terminalError)
			}
			if closeErr := tracked.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close failed session: %w", closeErr))
			}
		}
		i.publish(err)
		return nil, err
	}
	if err == nil && session != nil && i.lifecycle != nil {
		i.lifecycle.markSessionCreated()
		tracked := &roomTrackedSession{Session: session, lifecycle: i.lifecycle, admissionClosed: i.lifecycle.admissionClosed}
		i.lifecycle.setOwnedSession(tracked)
		terminalError := func() error { return terminalSessionError(tracked) }
		i.lifecycle.setTransportDone(tracked.Done(), terminalError)
		recordSessionEnd := func() {
			i.lifecycle.markTransportEndedWithError(terminalError())
		}
		go func() {
			select {
			case <-tracked.Done():
				recordSessionEnd()
			case <-ctx.Done():
				if roomChannelClosed(tracked.Done()) {
					recordSessionEnd()
				} else {
					// Cancellation can win before the model runner reaches its
					// deferred session Close (for example while the room is still
					// admitting a sibling). The tracker owns this idempotent
					// fallback so a connected provider cannot outlive the room.
					_ = tracked.Close()
				}
			}
		}()
		session = tracked
	}
	i.publish(err)
	return session, err
}

var _ messages.SessionInferencer = (*roomConnectTrackingInferencer)(nil)
