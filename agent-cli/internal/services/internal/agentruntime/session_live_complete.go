package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// SendMessage forwards the optional complete-message provider capability while
// retaining the observed provider boundary used by tool continuations.
func (s *observedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	unlockProviderBoundary := s.progress.lockProviderBoundary()
	defer unlockProviderBoundary()
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(sessionturn.CompleteMessageSender)
	if !ok {
		return false
	}
	outcome := sessionCompleteMessageSendOutcome(ctx, sender.SendMessage(ctx, msg))
	s.observeCompleteMessageToolResult(ctx, msg, outcome, true)
	return outcome.OK()
}

func (s *observedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	unlockProviderBoundary := s.progress.lockProviderBoundary()
	defer unlockProviderBoundary()
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(sessionturn.CompleteMessageWithoutResponseSender)
	if !ok {
		return false
	}
	outcome := sessionCompleteMessageSendOutcome(ctx, sender.SendMessageWithoutResponse(ctx, msg))
	s.observeCompleteMessageToolResult(ctx, msg, outcome, false)
	return outcome.OK()
}

func sessionCompleteMessageSendOutcome(ctx context.Context, sent bool) messages.SessionSendOutcome {
	if sent {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if ctx != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: ctx.Err()}
		case context.Canceled:
			return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: ctx.Err()}
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}

func (s *observedSession) observeCompleteMessageToolResult(ctx context.Context, msg messages.Message, outcome messages.SessionSendOutcome, requestsContinuation bool) {
	if s == nil || s.progress == nil {
		return
	}
	if outcome.OK() {
		if msg.ToolCallID != "" {
			s.progress.noteToolResultAcceptedWithContext(ctx, msg.ToolCallID)
		}
		if requestsContinuation {
			if msg.ToolCallID != "" {
				s.progress.noteToolContinuationRequestedForWithContext(ctx, msg.ToolCallID)
			}
			s.progress.armProviderProgress()
		}
		return
	}
	if msg.ToolCallID != "" {
		s.progress.noteToolResultRejected(msg.ToolCallID, outcome)
	}
}

func (s *observedSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *observedSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *observedSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.Session.Close()
		s.markDone()
	})
	return s.closeErr
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
