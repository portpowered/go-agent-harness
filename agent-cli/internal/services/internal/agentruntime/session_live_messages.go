package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SendMessage forwards the optional complete-message provider capability. The
// observation wrapper embeds the stream-only public Session interface, so it
// must preserve the rich tool-result path used by multimodal sessions.
func (s *observedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	unlockProviderBoundary := s.progress.lockProviderBoundary()
	defer unlockProviderBoundary()
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(SessionImageMessageSender)
	if !ok {
		return false
	}
	outcome := sessionCompleteMessageSendOutcome(ctx, sender.SendMessage(ctx, msg))
	s.observeCompleteMessageToolResult(ctx, msg, outcome, true)
	return outcome.OK()
}

// SendMessageWithoutResponse preserves deferred rich-message delivery for
// callers that batch tool results before requesting one provider response.
func (s *observedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	unlockProviderBoundary := s.progress.lockProviderBoundary()
	defer unlockProviderBoundary()
	if s.SessionAdmissionClosed() && !s.SessionAdmissionAllowsCompleteMessage(msg) {
		return false
	}
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
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
	if !outcome.OK() {
		if msg.ToolCallID != "" {
			s.progress.noteToolResultRejected(msg.ToolCallID, outcome)
		}
		return
	}
	if msg.ToolCallID != "" {
		s.progress.noteToolResultAcceptedContext(ctx, msg.ToolCallID)
	}
	if requestsContinuation {
		if msg.ToolCallID != "" {
			s.progress.noteToolContinuationRequestedForContext(ctx, msg.ToolCallID)
		}
		s.progress.armProviderProgress()
	}
}
