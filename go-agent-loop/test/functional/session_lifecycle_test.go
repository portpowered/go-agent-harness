package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionLifecycle_OpenAndClose(t *testing.T) {
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool)

	scenario.Start()

	// Wait for SESSION.OPEN to be emitted.
	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Trigger session close.
	err := scenario.Stop(5 * time.Second)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Allow time for delta collection to complete.
	time.Sleep(100 * time.Millisecond)

	deltas := scenario.Deltas()
	if len(deltas) == 0 {
		t.Fatal("expected at least some delta events")
	}

	// Verify SESSION.OPEN is the first event.
	if deltas[0].Type != messages.StreamTypeSessionOpen {
		t.Errorf("first delta: got %q, want SESSION.OPEN", deltas[0].Type)
	}

	// Log all deltas for debugging.
	for i, d := range deltas {
		t.Logf("delta[%d] type=%s", i, d.Type)
	}
}
