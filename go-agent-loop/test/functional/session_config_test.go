package functional

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionConfigSentOnSessionCreated verifies that when the agent loop is
// configured with session parameters (model, instructions, modalities) and the
// inference provider emits SESSION.CREATED, the loop sends a SESSION.UPDATE
// message back to the provider within 1 second containing the configured values.
//
// Flow: SESSION.CREATED (from provider) → model runner intercepts → SESSION.UPDATE (to provider).
func TestSessionConfigSentOnSessionCreated(t *testing.T) {
	const (
		wantInstructions = "You are a helpful robot vacuum assistant"
		wantModel        = "grok-3"
	)
	wantModalities := []string{"audio", "text"}

	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor()
	scenario := NewSessionScenario(t, inf, tool,
		agentloop.WithSessionConfig(messages.SessionUpdateConfig{
			Instructions: wantInstructions,
			Model:        wantModel,
			Modalities:   wantModalities,
		}),
	)
	scenario.Start()

	// Wait for SESSION.OPEN so we know the session is established and the
	// model runner is running.
	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, 3*time.Second) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	// Inject SESSION.CREATED from the inference provider. In production this
	// arrives from the WebSocket server; here we simulate it directly.
	inf.AddServerEvent(messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("mock-session-id", wantModel),
	})

	// The model runner must respond with SESSION.UPDATE within 1 second.
	got, ok := inf.WaitForSentMessage(messages.StreamTypeSessionUpdate, 1*time.Second)
	if !ok {
		t.Fatal("timed out waiting for SESSION.UPDATE — model runner may not be sending session config on SESSION.CREATED")
	}

	// Verify the SESSION.UPDATE payload contains the configured values.
	v, ok := got.Value.(*messages.SessionUpdateValue)
	if !ok || v == nil {
		t.Fatal("SESSION.UPDATE value is not *SessionUpdateValue")
	}
	if v.Instructions != wantInstructions {
		t.Errorf("instructions: got %q, want %q", v.Instructions, wantInstructions)
	}
	if v.Model != wantModel {
		t.Errorf("model: got %q, want %q", v.Model, wantModel)
	}
	if len(v.Modalities) != len(wantModalities) {
		t.Errorf("modalities length: got %d, want %d", len(v.Modalities), len(wantModalities))
	} else {
		for i, m := range wantModalities {
			if v.Modalities[i] != m {
				t.Errorf("modalities[%d]: got %q, want %q", i, v.Modalities[i], m)
			}
		}
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
