package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
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

func TestSessionDurationTerminalCompatibilityPreservesProjectionAndFailures(t *testing.T) {
	state := newSessionDurationTerminalState(&durationAdmission{})
	state.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	state.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")})
	if got := state.outputState(); got != messages.TerminalOutputPartial {
		t.Fatalf("terminal output state before completion = %q, want partial", got)
	}
	state.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	if got := state.outputState(); got != messages.TerminalOutputComplete {
		t.Fatalf("terminal output state after completion = %q, want complete", got)
	}

	closeMessage := messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("compat-session", "planned"),
	}
	if _, admitted := state.admitTerminal(true, closeMessage); admitted {
		t.Fatal("planned close was admitted without an observed provider terminal")
	}
	if err := state.writeObservedProviderTerminal(io.Discard, nil); err != nil {
		t.Fatalf("write observed provider terminal: %v", err)
	}

	var out bytes.Buffer
	if err := writeMaxDurationTerminal(&out, nil, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("write max-duration terminal: %v", err)
	}
	if !strings.Contains(out.String(), string(SessionMaxDurationReason)) {
		t.Fatalf("max-duration terminal output = %q, want %q", out.String(), SessionMaxDurationReason)
	}

	sentinel := errors.New("duration compatibility failure")
	if err := sessionDurationLifecycleError(sentinel, nil, nil); !errors.Is(err, sentinel) {
		t.Fatalf("lifecycle error %v does not preserve sentinel", err)
	}
	if err := sessionTransportError(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("transport error %v does not preserve sentinel", err)
	}
}

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out interface{ Write([]byte) (int, error) }, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock SessionDurationClock, _ any) error {
	return runAgentLoopSessionWithDurationClockStream(ctx, out, inferencer, opts, maxDuration, clock)
}
