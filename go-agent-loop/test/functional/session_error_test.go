package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// TestSessionInferenceError verifies that an error from the model terminates the engine.
// Note: ERROR events in the model delta stream cause the engine to return an error.
func TestSessionInferenceError(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Queue an error event from the server.
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeError, Value: messages.NewErrorValue("rate limit exceeded"), Role: messages.RoleAssistant,
	})

	scenario.SendText("trigger")

	// The engine should terminate due to the error.
	time.Sleep(500 * time.Millisecond)
	inf.Close()
	scenario.cancel()

	select {
	case <-scenario.errCh:
		// Expected: engine exits with error.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for engine to exit after error")
	}
}

// TestSessionDisconnect verifies that closing the mock inferencer triggers shutdown.
func TestSessionDisconnect(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Need an active inference for the disconnect to be detected.
	scenario.SendText("trigger")
	time.Sleep(100 * time.Millisecond)

	// Simulate disconnect by closing the mock.
	inf.SimulateDisconnect()

	// The model runner should detect the closed channel and the engine should stop.
	time.Sleep(500 * time.Millisecond)
	scenario.cancel()

	select {
	case <-scenario.errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for loop exit after disconnect")
	}
}
