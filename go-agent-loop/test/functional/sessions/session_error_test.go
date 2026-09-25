package sessions

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// stopOnCleanup closes the provider and cancels the loop when a test that
// does not use Stop finishes, so no scenario outlives its test.
func stopOnCleanup(t *testing.T, scenario *SessionScenario) {
	t.Cleanup(func() {
		scenario.Inf.Close()
		scenario.cancel()
	})
}

// TestSessionInferenceError verifies that an ERROR event from the provider
// terminates the engine with that error, without the harness cancelling it.
func TestSessionInferenceError(t *testing.T) {
	t.Parallel()
	inf := NewMockSessionInferencer()
	scenario := NewSessionScenario(t, inf, NewMockToolExecutor())
	scenario.Start()
	stopOnCleanup(t, scenario)

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeError, Value: messages.NewErrorValue("rate limit exceeded"), Role: messages.RoleAssistant,
	})
	scenario.SendText("trigger")

	exited, err := scenario.awaitRunExit(5 * time.Second)
	if !exited {
		t.Fatal("engine did not terminate after a provider ERROR event")
	}
	if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("engine exit error = %v, want the provider error", err)
	}
}

// TestSessionDisconnect verifies that a provider disconnect during an active
// inference surfaces SESSION.CLOSE to the client.
func TestSessionDisconnect(t *testing.T) {
	t.Parallel()
	inf := NewMockSessionInferencer()
	scenario := NewSessionScenario(t, inf, NewMockToolExecutor())
	scenario.Start()
	stopOnCleanup(t, scenario)

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Disconnect only once the provider has received the user turn.
	scenario.SendText("trigger")
	if _, ok := inf.WaitForSentMessage(messages.StreamTypeTextDelta, 3*time.Second); !ok {
		t.Fatal("timed out waiting for the user turn to reach the provider")
	}
	if scenario.WaitForEvent(messages.StreamTypeSessionClose, 0) {
		t.Fatal("SESSION.CLOSE was emitted before the provider disconnected")
	}

	inf.SimulateDisconnect()
	if !scenario.WaitForEvent(messages.StreamTypeSessionClose, 3*time.Second) {
		t.Fatal("provider disconnect did not surface SESSION.CLOSE")
	}
}
