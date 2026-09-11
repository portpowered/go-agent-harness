package agentruntime

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// These test-only adapters preserve the older unit-test seam while production
// duration admission is owned by go-agent-runtime/services/duration.
type sessionDurationAdmissionSession struct{ inner messages.Session }

func (s *sessionDurationAdmissionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}
func (s *sessionDurationAdmissionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inner.Receive()
}
func (s *sessionDurationAdmissionSession) Done() <-chan struct{} { return s.inner.Done() }
func (s *sessionDurationAdmissionSession) Close() error          { return s.inner.Close() }
func (s *sessionDurationAdmissionSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}
func (s *sessionDurationAdmissionSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}
func (s *sessionDurationAdmissionSession) SupportsCompleteMessages() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	if ok {
		return capability.SupportsCompleteMessages()
	}
	_, ok = s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok
}
func (s *sessionDurationAdmissionSession) SupportsCompleteMessagesWithoutResponse() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	if ok {
		return capability.SupportsCompleteMessagesWithoutResponse()
	}
	_, ok = s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok
}

func isDurationShutdownMessage(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		return true
	}
	if msg.Type != messages.StreamTypeError {
		return false
	}
	value, ok := msg.Value.(*messages.ErrorValue)
	return !ok || value.IsTerminal()
}

func isDurationForwardMessage(msg messages.StreamMessage) bool {
	return isDurationShutdownMessage(msg) || msg.Type == messages.StreamTypeError
}

func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out interface{ Write([]byte) (int, error) }, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock SessionDurationClock, _ any) error {
	return runAgentLoopSessionWithDurationClock(ctx, out, inferencer, opts, maxDuration, clock)
}

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out interface{ Write([]byte) (int, error) }, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock SessionDurationClock, _ any) error {
	return runAgentLoopSessionWithDurationClockStream(ctx, out, inferencer, opts, maxDuration, clock)
}
