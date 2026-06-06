package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestSessionVADSpeechStarted(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue(),
	})
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeVADSpeechStarted, 3*time.Second) {
		t.Fatal("timed out waiting for VAD.SPEECH_STARTED")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeVADSpeechStarted},
	})
}

func TestSessionVADSpeechStopped(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue(),
	})
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeVADSpeechStopped, 3*time.Second) {
		t.Fatal("timed out waiting for VAD.SPEECH_STOPPED")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestSessionVADInterleavedWithAudio(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Interleave VAD events with audio deltas.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x02}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x03}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()

	// Verify all events appear in order.
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeAudioStart},
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeVADSpeechStarted},
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeVADSpeechStopped},
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeAudioEnd},
	})
}

func TestSessionVADNotInReconstruction(t *testing.T) {
	// VAD events should NOT appear as ContentParts in reconstructed messages.
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0xAA})},
		{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0xBB})},
		{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()},
	}

	msg := messages.ReconstructModelMessageFromDeltas(deltas)

	// Should have AudioPart but no VAD parts.
	hasAudio := false
	for _, part := range msg.ContentParts {
		if _, ok := part.(messages.AudioPart); ok {
			hasAudio = true
		}
		// VAD values are StreamMessageValues, not ContentParts, so they
		// can't appear here by design. This test documents that contract.
	}
	if !hasAudio {
		t.Error("expected AudioPart in reconstructed message")
	}
	// Verify audio bytes are correct (0xAA + 0xBB combined).
	for _, part := range msg.ContentParts {
		if ap, ok := part.(messages.AudioPart); ok {
			if len(ap.Bytes) != 2 || ap.Bytes[0] != 0xAA || ap.Bytes[1] != 0xBB {
				t.Errorf("audio bytes: got %v, want [0xAA 0xBB]", ap.Bytes)
			}
		}
	}
}

func TestSessionVADMultipleTurns(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Simulate multiple speech turns with VAD boundaries.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()},
		{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x02}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()

	// Count VAD events.
	startCount, stopCount := 0, 0
	for _, d := range deltas {
		if d.Type == messages.StreamTypeVADSpeechStarted {
			startCount++
		}
		if d.Type == messages.StreamTypeVADSpeechStopped {
			stopCount++
		}
	}
	if startCount != 2 {
		t.Errorf("VAD.SPEECH_STARTED count: got %d, want 2", startCount)
	}
	if stopCount != 2 {
		t.Errorf("VAD.SPEECH_STOPPED count: got %d, want 2", stopCount)
	}
}
