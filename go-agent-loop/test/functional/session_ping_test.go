package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionPingPong_FullLoop verifies that a ping control plane message sent
// through the full agent loop (not just the subsystem) produces a PONG delta.
// This is the integration-level counterpart to the direct subsystem tests in
// pkg/subsystems/ping_pong_test.go.
func TestSessionPingPong_FullLoop(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Send a ping through the full loop.
	scenario.SendControlPlane(messages.ControlPlaneMessageTypePing)

	// Wait for the PONG event to appear in the delta stream.
	if !scenario.WaitForEvent(messages.StreamTypePong, 3*time.Second) {
		t.Fatal("timed out waiting for PONG — PingPong subsystem may not be registered in the agent loop constructor")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Verify PONG is present in collected deltas.
	deltas := scenario.Deltas()
	hasPong := false
	for _, d := range deltas {
		if d.Type == messages.StreamTypePong {
			hasPong = true
			if pv, ok := d.Value.(*messages.PongValue); ok {
				now := time.Now().UnixMilli()
				diff := now - pv.Timestamp
				if diff < 0 || diff > 5000 {
					t.Errorf("PONG timestamp unreasonable: diff=%dms", diff)
				}
			} else {
				t.Error("PONG value is not *PongValue")
			}
			break
		}
	}
	if !hasPong {
		t.Error("PONG delta not found in collected deltas")
	}
}

// TestSessionPingDuringAudio tests that ping works alongside audio in session mode.
func TestSessionPingDuringAudio(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Start audio and include a text delta so model runner is active.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})
	scenario.SendText("trigger audio")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()
	hasAudio := false
	for _, d := range deltas {
		if d.Type == messages.StreamTypeAudioDelta {
			hasAudio = true
		}
	}
	if !hasAudio {
		t.Error("expected AUDIO.DELTA")
	}
}
