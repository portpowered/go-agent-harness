package embedding_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
)

// A host can validate untrusted configuration without constructing live devices
// or providers, and only credential references may leave the planning boundary.
func TestExternalRoomPlansConfigurationWithoutRetainingCredentials(t *testing.T) {
	const document = `schema_version: 1
room:
  max_duration: 2m
participants:
  - id: customer
    system_prompt: Ask for a trip
    opening_prompt: Start by asking for a trip
    provider: OPENAI
    model: gpt-realtime
    api_key_env: ROOM_KEY
    tools: []
  - id: assistant
    system_prompt: Answer the customer
    provider: openai
    model: gpt-realtime
    api_key_env: ROOM_KEY
    tools: []
`
	path := filepath.Join(t.TempDir(), "room.yaml")
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	service := roomswire.NewService(roomswire.Dependencies{})
	lookup := func(name string) (string, bool) { return "test-secret-never-in-plan", name == "ROOM_KEY" }
	plan, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: path, CredentialLookup: lookup})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ConfigDir != filepath.Dir(path) || plan.Manifest.Room.MaxDuration != 2*time.Minute || len(plan.Participants) != 2 {
		t.Fatalf("unexpected launch plan: %+v", plan)
	}
	participant, ok := plan.Participant("customer")
	if !ok || participant.Provider != "openai" || participant.CredentialReference != "ROOM_KEY" || participant.CredentialProvenance != rooms.RoomCredentialFromEnvironment {
		t.Fatalf("participant admission: %+v", participant)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "test-secret-never-in-plan") {
		t.Fatal("launch plan retained a credential value")
	}
	_, err = service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ConfigPath: path, ManifestPath: path + ".other"})
	if !errors.Is(err, rooms.ErrLaunchPathConflict) {
		t.Fatalf("conflicting paths: %v", err)
	}
	for _, suffix := range []string{"unknown_field: true\n", "---\nroom: {}\n"} {
		if err := os.WriteFile(path, []byte(document+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ResolveLaunchPlan(rooms.RoomLaunchOptions{ManifestPath: path, CredentialLookup: lookup}); !errors.Is(err, rooms.ErrInvalidDocument) {
			t.Fatalf("untrusted suffix %q accepted or misclassified: %v", suffix, err)
		}
	}
}
