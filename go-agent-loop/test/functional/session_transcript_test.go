package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestSessionTranscriptDelta(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("hello"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("hello world"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeTranscriptEnd, 3*time.Second) {
		t.Fatal("timed out waiting for TRANSCRIPT.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeTranscriptDelta},
		{Type: messages.StreamTypeTranscriptEnd},
	})
}

func TestSessionTranscriptWithAudio(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Interleave audio and transcript.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("hello"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x02}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue(" world"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("hello world"), Role: messages.RoleAssistant},
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
	hasAudio := false
	hasTranscript := false
	for _, d := range deltas {
		if d.Type == messages.StreamTypeAudioDelta {
			hasAudio = true
		}
		if d.Type == messages.StreamTypeTranscriptDelta {
			hasTranscript = true
		}
	}
	if !hasAudio {
		t.Error("expected AUDIO.DELTA")
	}
	if !hasTranscript {
		t.Error("expected TRANSCRIPT.DELTA")
	}
}

func TestSessionTranscriptReconstruction(t *testing.T) {
	// Unit test: verify reconstruction produces TranscriptPart.
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeTranscriptStart, Value: messages.NewTranscriptStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("hello ")},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("world")},
		{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("hello world")},
	}

	msg := messages.ReconstructModelMessageFromDeltas(deltas)

	hasTranscript := false
	for _, part := range msg.ContentParts {
		if tp, ok := part.(messages.TranscriptPart); ok {
			hasTranscript = true
			if tp.Text != "hello world" {
				t.Errorf("transcript text: got %q, want %q", tp.Text, "hello world")
			}
		}
	}
	if !hasTranscript {
		t.Error("expected TranscriptPart in reconstructed message")
	}
}

func TestSessionTranscriptSeparateFromText(t *testing.T) {
	// Verify TranscriptPart and TextPart are separate.
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("LLM response")},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("ASR transcript")},
	}

	msg := messages.ReconstructModelMessageFromDeltas(deltas)

	hasText := false
	hasTranscript := false
	for _, part := range msg.ContentParts {
		if _, ok := part.(messages.TextPart); ok {
			hasText = true
		}
		if _, ok := part.(messages.TranscriptPart); ok {
			hasTranscript = true
		}
	}
	if !hasText {
		t.Error("expected TextPart")
	}
	if !hasTranscript {
		t.Error("expected TranscriptPart")
	}
	if len(msg.ContentParts) != 2 {
		t.Errorf("expected 2 content parts, got %d", len(msg.ContentParts))
	}
}

func TestSessionTranscriptAndVAD(t *testing.T) {
	// Verify audio → AudioPart, transcript → TranscriptPart, VAD → not in reconstruction.
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01})},
		{Type: messages.StreamTypeVADSpeechStarted, Value: messages.NewVADSpeechStartedValue()},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("hi")},
		{Type: messages.StreamTypeVADSpeechStopped, Value: messages.NewVADSpeechStoppedValue()},
	}

	msg := messages.ReconstructModelMessageFromDeltas(deltas)

	hasAudio := false
	hasTranscript := false
	for _, part := range msg.ContentParts {
		if _, ok := part.(messages.AudioPart); ok {
			hasAudio = true
		}
		if _, ok := part.(messages.TranscriptPart); ok {
			hasTranscript = true
		}
	}
	if !hasAudio {
		t.Error("expected AudioPart")
	}
	if !hasTranscript {
		t.Error("expected TranscriptPart")
	}
	// Should be exactly 2 parts (audio + transcript, no VAD).
	if len(msg.ContentParts) != 2 {
		t.Errorf("expected 2 content parts (audio + transcript), got %d", len(msg.ContentParts))
		for i, p := range msg.ContentParts {
			t.Logf("  part[%d] = %T", i, p)
		}
	}
}
