package wire

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestRoomEventStreamRedactsEveryResolvedCredential(t *testing.T) {
	const envSecret, configSecret = "sk-env-alice-1234", "sk-config-bob-5678"
	manifest := rooms.Manifest{Participants: []rooms.Participant{
		{ID: "alice", APIKeyEnv: "env:ALICE_KEY"},
		{ID: "bob", APIKeyEnv: "BOB_KEY"},
	}}
	redactor := NewRoomSecretRedactor(rooms.RoomCredentialSources{
		Manifest: manifest,
		CredentialLookup: func(name string) (string, bool) {
			return envSecret, name == "ALICE_KEY"
		},
		ConfigCredential: func(name string) (string, error) {
			if name == "BOB_KEY" {
				return configSecret, nil
			}
			return "", errors.New("no config credential")
		},
	})
	stream, err := NewRoomEventStream(rooms.RoomEventStreamOptions{ParticipantIDs: []string{"alice", "bob"}, Redactor: redactor})
	if err != nil {
		t.Fatalf("NewRoomEventStream: %v", err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Errorf("stream.Close(): %v", err)
		}
	}()
	subscription, err := stream.Subscribe("")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer subscription.Close()

	if err := stream.Publish(context.Background(), "alice", session.LiveEvent{Kind: string(session.LiveEventOverflow), Dropped: 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := stream.Publish(context.Background(), "bob", session.LiveEvent{Kind: "browser.failed", Reason: "rejected " + envSecret + " and " + configSecret}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	stream.PublishRoomEvent(rooms.RoomStreamEventParticipantFailed, "alice", "dial "+envSecret)

	for index := 0; index < 3; index++ {
		select {
		case frame := <-subscription.Frames():
			if strings.Contains(string(frame), envSecret) || strings.Contains(string(frame), configSecret) {
				t.Fatalf("frame %d leaked a credential: %s", index, frame)
			}
			if index > 0 && !strings.Contains(string(frame), "[REDACTED]") {
				t.Fatalf("frame %d = %s, want the credential redacted", index, frame)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frame %d not delivered", index)
		}
	}
	if got := redactor.Redact("key=" + configSecret); got != "key=[REDACTED]" {
		t.Fatalf("host text redaction = %q", got)
	}
}

func TestRoomEventStreamRejectsInvalidParticipants(t *testing.T) {
	if _, err := NewRoomEventStream(rooms.RoomEventStreamOptions{ParticipantIDs: []string{rooms.RoomStreamParticipantID}}); !errors.Is(err, rooms.ErrInvalidRoomStreamParticipant) {
		t.Fatalf("reserved participant error = %v", err)
	}
}
