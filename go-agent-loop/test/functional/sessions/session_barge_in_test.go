package sessions

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionBargeIn_SendsResponseCancel verifies the barge-in behavior:
// when new user audio arrives while the model is streaming an audio response,
// the agent loop sends RESPONSE.CANCEL to the inference provider within 500ms,
// then forwards the interrupting audio.
//
// Sequence:
//  1. Model streams AUDIO.START + AUDIO.DELTA events
//  2. User sends audio (barge-in)
//  3. Agent loop sends RESPONSE.CANCEL to provider (within 500ms)
//  4. Agent loop forwards the new user audio to provider
func TestSessionBargeIn_SendsResponseCancel(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Start the model streaming an audio response (never ends — simulates mid-stream barge-in).
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01, 0x02}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x03, 0x04}), Role: messages.RoleAssistant},
	})

	// Wait for audio streaming to begin in the delta output.
	if !scenario.WaitForEvent(messages.StreamTypeAudioStart, 3*time.Second) {
		t.Fatal("timed out waiting for AUDIO.START to appear in delta stream")
	}

	// Give the model runner time to process AUDIO.START (sets audioStreaming=true).
	time.Sleep(50 * time.Millisecond)

	// Barge-in: user sends audio while the model is streaming.
	bargInPayload := []byte{0xAA, 0xBB, 0xCC}
	scenario.SendAudioInput(bargInPayload)

	// Agent loop must emit RESPONSE.CANCEL to the inference provider within 500ms.
	cancelMsg, ok := inf.WaitForSentMessage(messages.StreamTypeResponseCancel, 500*time.Millisecond)
	if !ok {
		t.Fatal("timed out waiting for RESPONSE.CANCEL — barge-in not implemented or not triggered")
	}
	if _, ok := cancelMsg.Value.(*messages.ResponseCancelValue); !ok {
		t.Error("RESPONSE.CANCEL value is not *ResponseCancelValue")
	}

	// After cancellation, the interrupting audio must also be forwarded to the provider.
	audioMsg, ok := inf.WaitForSentMessage(messages.StreamTypeAudioDelta, 500*time.Millisecond)
	if !ok {
		t.Fatal("timed out waiting for user AUDIO.DELTA after barge-in")
	}
	v, ok := audioMsg.Value.(*messages.AudioDeltaValue)
	if !ok || v == nil {
		t.Fatal("user audio delta value is not *AudioDeltaValue")
	}
	if string(v.Content) != string(bargInPayload) {
		t.Errorf("user audio content: got %v, want %v", v.Content, bargInPayload)
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSessionBargeIn_AudioStopsAfterCancel verifies that after a barge-in, the
// agent loop's output delta buffer stops receiving AUDIO.DELTA events from the
// interrupted response once the mock inferencer stops sending them.
func TestSessionBargeIn_AudioStopsAfterCancel(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Model streams exactly 2 audio deltas before the barge-in, then stops.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x02}), Role: messages.RoleAssistant},
		// No AUDIO.END — simulates abrupt cancellation from server after barge-in.
	})

	// Wait for audio streaming to start.
	if !scenario.WaitForEvent(messages.StreamTypeAudioDelta, 3*time.Second) {
		t.Fatal("timed out waiting for first AUDIO.DELTA")
	}
	time.Sleep(50 * time.Millisecond)

	// Barge-in.
	scenario.SendAudioInput([]byte{0xFF})

	// Wait for RESPONSE.CANCEL to confirm barge-in was detected.
	if _, ok := inf.WaitForSentMessage(messages.StreamTypeResponseCancel, 500*time.Millisecond); !ok {
		t.Fatal("timed out waiting for RESPONSE.CANCEL")
	}

	// Allow time for any further events to propagate.
	time.Sleep(100 * time.Millisecond)

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After cancellation the server stopped sending audio, so we should see the 2
	// deltas that arrived before the barge-in but no more.
	deltas := scenario.Deltas()
	audioCount := 0
	for _, d := range deltas {
		if d.Type == messages.StreamTypeAudioDelta {
			audioCount++
		}
	}
	// Exactly 2 audio deltas should have propagated before the barge-in.
	if audioCount > 2 {
		t.Errorf("too many AUDIO.DELTA events after barge-in: got %d, want ≤2", audioCount)
	}
	if audioCount == 0 {
		t.Error("expected at least one AUDIO.DELTA before barge-in")
	}
}

// TestSessionNoBargeIn_AudioWithoutInterruption verifies that when the model
// streams audio and no user audio arrives, the session plays through without
// RESPONSE.CANCEL being emitted.
func TestSessionNoBargeIn_AudioWithoutInterruption(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Complete audio response with no barge-in.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x10}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// RESPONSE.CANCEL should not have been sent.
	deltas := scenario.Deltas()
	for _, d := range deltas {
		if d.Type == messages.StreamTypeResponseCancel {
			t.Error("unexpected RESPONSE.CANCEL in delta stream — barge-in triggered without user audio")
		}
	}
}
