package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Stop returns only after LOOP.END was collected, so the delta stream is
// complete when these tests read it.

func TestSessionGracefulClose(t *testing.T) {
	t.Parallel()
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Verify lifecycle: SESSION.OPEN first, SESSION.CLOSE + LOOP.END last.
	AssertSessionLifecycle(t, scenario.Deltas())
}

func TestSessionStopTermination(t *testing.T) {
	t.Parallel()
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Send stop instead of session_close: it must still close the session.
	scenario.SendControlPlane(messages.ControlPlaneMessageTypeStop)
	if !scenario.WaitForEvent(messages.StreamTypeSessionClose, 3*time.Second) {
		t.Fatal("expected SESSION.CLOSE after stop")
	}

	inf.Close()
	scenario.cancel()
	exited, err := scenario.awaitRunExit(5 * time.Second)
	if !exited {
		t.Fatal("timed out waiting for loop exit")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionLifecycleOrder(t *testing.T) {
	t.Parallel()
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Inject some events in between.
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hi"), Role: messages.RoleAssistant,
	})
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant,
	})
	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Verify full lifecycle ordering.
	AssertSessionLifecycle(t, scenario.Deltas())
}
