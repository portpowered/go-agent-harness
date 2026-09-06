package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// TestRunnerRejectsInvalidParticipantBrowserToolsBeforeLiveConstruction
// preserves the legacy room admission guarantee at the public room boundary:
// malformed browser policy is rejected before any participant session is
// opened or host capability is constructed.
func TestRunnerRejectsInvalidParticipantBrowserToolsBeforeLiveConstruction(t *testing.T) {
	service := &fakeLiveService{handles: map[string]*fakeLiveHandle{
		"customer":  newFakeLiveHandle(),
		"assistant": newFakeLiveHandle(),
	}}
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1},
		Participants: []rooms.Participant{
			{
				ID: "customer", SystemPrompt: "customer", Provider: "openai", Model: "gpt-realtime", APIKeyEnv: "CUSTOMER_KEY", Tools: []string{},
				BrowserTools: &rooms.BrowserToolsConfig{Backend: "chrome"},
			},
			{ID: "assistant", SystemPrompt: "assistant", Provider: "openai", Model: "gpt-realtime", APIKeyEnv: "ASSISTANT_KEY", Tools: []string{}},
		},
	}
	runner := New(Dependencies{Live: service, Clock: platformclock.Real{}})
	_, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{Manifest: manifest})
	if err == nil || !errors.Is(err, rooms.ErrInvalidBrowserTools) {
		t.Fatalf("room admission error = %v, want invalid browser tools", err)
	}
	service.mu.Lock()
	openCount := len(service.requests)
	service.mu.Unlock()
	if openCount != 0 {
		t.Fatalf("live sessions opened before browser validation: %d", openCount)
	}
	var validationErr *rooms.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "participants[0].browserTools.backend" {
		t.Fatalf("validation error = %v, want participant-qualified browser backend field", err)
	}
}
