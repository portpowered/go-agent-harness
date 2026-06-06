package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestSessionGracefulClose(t *testing.T) {
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

	time.Sleep(100 * time.Millisecond)
	deltas := scenario.Deltas()

	// Verify lifecycle: SESSION.OPEN first, SESSION.CLOSE + LOOP.END last.
	AssertSessionLifecycle(t, deltas)
}

func TestSessionStopTermination(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Send stop instead of session_close.
	scenario.SendControlPlane(messages.ControlPlaneMessageTypeStop)
	time.Sleep(200 * time.Millisecond)
	inf.Close()
	scenario.cancel()

	select {
	case err := <-scenario.errCh:
		if err != nil && err.Error() != "context canceled" {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for loop exit")
	}

	time.Sleep(100 * time.Millisecond)
	deltas := scenario.Deltas()

	// Should have SESSION.CLOSE with reason derived from stop.
	hasClose := false
	for _, d := range deltas {
		if d.Type == messages.StreamTypeSessionClose {
			hasClose = true
		}
	}
	if !hasClose {
		t.Error("expected SESSION.CLOSE after stop")
	}
}

func TestSessionLifecycleOrder(t *testing.T) {
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

	time.Sleep(100 * time.Millisecond)
	deltas := scenario.Deltas()

	// Verify full lifecycle ordering.
	AssertSessionLifecycle(t, deltas)
}
