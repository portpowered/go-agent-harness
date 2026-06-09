package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionSendText(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Queue a text response from the server.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("what is the weather?")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Verify the server response events appeared in the delta stream.
	deltas := scenario.Deltas()
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeTextDelta},
		{Type: messages.StreamTypeMessageEnd},
	})
}

func TestSessionTextResponse(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("The weather"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(" is sunny"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue(), Role: messages.RoleAssistant},
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
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeTextDelta},
		{Type: messages.StreamTypeTextDelta},
		{Type: messages.StreamTypeTextEnd},
	})
}

func TestSessionTextAndAudioMixed(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Server sends interleaved text and audio.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hi"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue(), Role: messages.RoleAssistant},
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
	hasText := false
	hasAudio := false
	for _, d := range deltas {
		if d.Type == messages.StreamTypeTextDelta {
			hasText = true
		}
		if d.Type == messages.StreamTypeAudioDelta {
			hasAudio = true
		}
	}
	if !hasText {
		t.Error("expected TEXT.DELTA in delta stream")
	}
	if !hasAudio {
		t.Error("expected AUDIO.DELTA in delta stream")
	}
}

func TestSessionTextDoesNotTerminate(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("answer"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue(), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeMessageEnd, 3*time.Second) {
		t.Fatal("timed out waiting for MESSAGE.END")
	}

	// Wait to confirm loop does NOT terminate.
	time.Sleep(500 * time.Millisecond)

	deltas := scenario.Deltas()
	for _, d := range deltas {
		if d.Type == messages.StreamTypeLoopEnd {
			t.Error("LOOP.END should not be emitted in session mode after text response")
		}
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
