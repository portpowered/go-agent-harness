package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionToolCallDuringAudio verifies that tool call events interleave
// with audio deltas in the delta stream without corruption.
func TestSessionToolCallDuringAudio(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	tool.AddResult("get_weather", `{"temp": 20}`)
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Server sends audio then a tool call then more audio.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x01}), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("call-1", "get_weather"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-1", "get_weather", `{"city":"London"}`), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	if !scenario.WaitForEvent(messages.StreamTypeToolCallEnd, 3*time.Second) {
		t.Fatal("timed out waiting for TOOLCALL.END")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deltas := scenario.Deltas()
	AssertSessionDeltaContains(t, deltas, []ExpectedDelta{
		{Type: messages.StreamTypeAudioDelta},
		{Type: messages.StreamTypeToolCallStart},
		{Type: messages.StreamTypeToolCallEnd},
	})
}

// TestSessionToolCallDoesNotTerminate verifies that in DuplexSession, completing
// a tool call does NOT trigger loop termination — the session persists.
func TestSessionToolCallDoesNotTerminate(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	tool.AddResult("ping", "pong")
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Server sends a tool call that completes.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("call-2", "ping"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-2", "ping", "{}"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	// Wait for the tool call to be processed.
	if !scenario.WaitForEvent(messages.StreamTypeToolCallEnd, 3*time.Second) {
		t.Fatal("timed out waiting for TOOLCALL.END")
	}

	// Wait a bit more — the loop should NOT terminate.
	time.Sleep(500 * time.Millisecond)

	deltas := scenario.Deltas()

	// Verify LOOP.END has NOT been emitted (session is still active).
	for _, d := range deltas {
		if d.Type == messages.StreamTypeLoopEnd {
			t.Error("LOOP.END should not be emitted — session should persist after tool call")
		}
	}

	// Now close the session.
	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSessionToolCallCompleted verifies the full tool call round-trip:
// server sends tool call → tool executor runs → result goes back.
func TestSessionToolCallCompleted(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	tool.AddResult("get_weather", `{"temp": 20, "unit": "celsius"}`)
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("call-3", "get_weather"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-3", "get_weather", `{"city":"Paris"}`), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	// Wait for tool call processing.
	time.Sleep(1 * time.Second)

	// Verify the tool executor was called.
	calls := tool.Calls()
	if len(calls) == 0 {
		t.Fatal("tool executor was never called")
	}
	if calls[0].Name != "get_weather" {
		t.Errorf("tool name: got %q, want %q", calls[0].Name, "get_weather")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSessionMultipleToolCalls verifies multiple sequential tool calls.
func TestSessionMultipleToolCalls(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	tool.AddResult("tool_a", "result_a")
	tool.AddResult("tool_b", "result_b")
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()

	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Server sends two tool calls in sequence.
	inf.AddServerEventSequence([]messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("c1", "tool_a"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("c1", "tool_a", `{}`), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("c2", "tool_b"), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("c2", "tool_b", `{}`), Role: messages.RoleAssistant},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{}), Role: messages.RoleAssistant},
	})

	scenario.SendText("trigger")

	// Wait for processing.
	time.Sleep(1 * time.Second)

	// Verify both tools were called.
	calls := tool.Calls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 tool calls, got %d", len(calls))
	}

	names := map[string]bool{}
	for _, call := range calls {
		names[call.Name] = true
	}
	if !names["tool_a"] {
		t.Error("tool_a was not called")
	}
	if !names["tool_b"] {
		t.Error("tool_b was not called")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
