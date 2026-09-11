package stream

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ShouldStop applies the finite input response boundary after the loop has
// observed one provider message.
func (s *Service) ShouldStop(message messages.StreamMessage, policy TurnStopPolicy) bool {
	if !policy.AwaitingResponse {
		return message.Type == messages.StreamTypeSessionClose
	}
	if message.Type == messages.StreamTypeMessageEnd && (call(policy.TerminalToolFailure) || call(policy.TerminalScheduledFailure)) {
		return true
	}
	if policy.WaitForClose {
		return callMessage(policy.Terminal, message) || message.Type == messages.StreamTypeSessionClose
	}
	switch message.Type {
	case messages.StreamTypeMessageEnd:
		if policy.MessageEndAdmitted != nil && !policy.MessageEndAdmitted() {
			return false
		}
		if policy.RequireAssistantResponse && (message.Role == messages.RoleTool || policy.AssistantResponseCompleted == nil || !policy.AssistantResponseCompleted()) {
			return false
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return callMessage(policy.Terminal, message)
	}
}

func call(f func() bool) bool { return f != nil && f() }
func callMessage(f func(messages.StreamMessage) bool, message messages.StreamMessage) bool {
	return f != nil && f(message)
}

// JoinTerminationErrors keeps independent session and input failures while
// suppressing expected context cancellation at the shared boundary.
func (s *Service) JoinTerminationErrors(runErr, audioErr error) error {
	var errs []error
	if runErr != nil && !s.IsExpectedCancellation(runErr) {
		errs = append(errs, fmt.Errorf("session error: %w", runErr))
	}
	if audioErr != nil && !s.IsExpectedCancellation(audioErr) {
		errs = append(errs, audioErr)
	}
	return errors.Join(errs...)
}

func (s *Service) IsExpectedCancellation(err error) bool {
	return err == nil || (!errors.Is(err, ErrEndOfTurnLost) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)))
}
