package agentruntime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunRoom_RejectsInvalidParticipantBrowserToolsBeforeSessionConstruction(t *testing.T) {
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{
				ID: "customer", SystemPrompt: "customer", Provider: "openai", Model: "gpt-realtime", APIKeyEnv: "CUSTOMER_KEY", Tools: []string{},
				BrowserTools: &room.BrowserToolsConfig{Backend: "chrome"},
			},
			{ID: "assistant", SystemPrompt: "assistant", Provider: "openai", Model: "gpt-realtime", APIKeyEnv: "ASSISTANT_KEY", Tools: []string{}},
		},
	}
	credentialLookups := 0
	sessionFactoryCalls := 0
	_, err := RunRoomWithResult(context.Background(), io.Discard, RoomRunOptions{
		Manifest: manifest,
		CredentialLookup: func(string) (string, bool) {
			credentialLookups++
			return "room-secret", true
		},
		SessionFactory: func(room.Participant, SessionRunOptions) (messages.SessionInferencer, error) {
			sessionFactoryCalls++
			return nil, errors.New("session construction must not be reached")
		},
	})
	if err == nil || !errors.Is(err, room.ErrUnsupportedBrowserToolsBackend) {
		t.Fatalf("RunRoomWithResult error = %v, want unsupported browser backend", err)
	}
	var validationErr *room.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Field != "participants[0].browserTools.backend" {
		t.Fatalf("validation error = %v, want participant-qualified browser backend field", err)
	}
	if credentialLookups != 0 || sessionFactoryCalls != 0 {
		t.Fatalf("pre-validation side effects = credential lookups %d, session factory calls %d", credentialLookups, sessionFactoryCalls)
	}
}
