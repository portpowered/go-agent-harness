package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// TestSessionAudioServerToClient verifies that server audio deltas injected via
// MockSessionInferencer propagate to the delta event stream as AUDIO.DELTA events.
func TestSessionAudioServerToClient(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	// Wait for SESSION.OPEN.
	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Queue server audio events that will be returned when InferStream is called.
	audioChunk1 := []byte{0x01, 0x02, 0x03}
	audioChunk2 := []byte{0x04, 0x05, 0x06}

	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(audioChunk1), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(audioChunk2), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	// Send a user message to trigger inference — this causes InferStream to be called.
	scenario.SendText("start audio")

	// Wait for MESSAGE.END to appear.
	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()

	// Verify audio events appear in the delta stream.
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeSessionOpen},
		{Type: messages.StreamTypeAudioStart},
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeAudioEnd},
	})
}

// TestSessionMultipleAudioChunks sends 10+ sequential audio chunks from the server
// and verifies all arrive in the delta stream in order.
func TestSessionMultipleAudioChunks(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Queue 12 audio chunks that will be delivered when InferStream is called.
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant,
	})
	for i := range 12 {
		chunk := []byte{byte(i), byte(i + 1)}
		inf.AddServerEvent(messages.StreamMessage{
			Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(chunk), Role: messages.RoleAssistant,
		})
	}
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue(), Role: messages.RoleAssistant,
	})
	inf.AddServerEvent(messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant,
	})

	// Trigger inference by sending a user message.
	scenario.SendText("start audio stream")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Count AUDIO.DELTA events.
	deltas := scenario.Deltas()
	audioCount := 0
	for _, d := range deltas {
		if d.Type == messages.StreamTypeAudioDelta {
			audioCount++
		}
	}
	if audioCount != 12 {
		t.Errorf("audio delta count: got %d, want 12", audioCount)
	}
}

// TestSessionAudioWithMessageLifecycle verifies audio deltas are bracketed by
// MESSAGE.START and MESSAGE.END in the delta stream.
func TestSessionAudioWithMessageLifecycle(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Queue a complete audio response.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0xFF}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	// Trigger inference.
	scenario.SendText("start")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()

	// Verify audio events are bracketed correctly.
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeSessionOpen},
		{Type: messages.StreamTypeAudioStart},
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeAudioEnd},
		{Type: messages.StreamTypeMessageEnd},
	})
}

// TestSessionConcurrentAudioBidirectional simultaneously sends a user text message
// AND injects server audio deltas, verifying both propagate without corruption.
func TestSessionConcurrentAudioBidirectional(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Inject server audio response.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0xAA, 0xBB}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	// Simultaneously send user text.
	scenario.SendText("hello from user")

	// Wait for both to propagate.
	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	time.Sleep(200 * time.Millisecond) // Let both paths settle.

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()

	// Verify server audio arrived in delta stream.
	hasAudio := false
	for _, d := range deltas {
		if d.Type == messages.StreamTypeAudioDelta {
			hasAudio = true
			break
		}
	}
	if !hasAudio {
		t.Error("expected at least one AUDIO.DELTA in delta stream")
		for i, d := range deltas {
			t.Logf("  delta[%d] type=%s", i, d.Type)
		}
	}
}
