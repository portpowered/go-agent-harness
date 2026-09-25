package planning

import (
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestEvidenceSecretsResolvesParticipantCredentials(t *testing.T) {
	manifest := rooms.Manifest{Participants: []rooms.Participant{
		{ID: "a", APIKeyEnv: "SHORT_KEY"},
		{ID: "b", APIKeyEnv: envCredentialPrefix + "LONG_KEY"},
		{ID: "c", APIKeyEnv: "SHORT_KEY"},
		{ID: "human"},
	}}
	values := map[string]string{"SHORT_KEY": " sk-short ", "LONG_KEY": "sk-short-and-longer"}
	got := EvidenceSecrets(manifest, rooms.RoomRunOptions{
		CredentialLookup: func(name string) (string, bool) { value, ok := values[name]; return value, ok },
		ConfigCredential: func(name string) (string, error) {
			if name == "SHORT_KEY" {
				return "sk-config", nil
			}
			return "", errors.New("unconfigured")
		},
	})
	want := []string{"sk-short-and-longer", "sk-config", "sk-short"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence secrets = %q, want %q", got, want)
	}
}

func TestEvidenceSecretsDefaultsToProcessEnvironment(t *testing.T) {
	t.Setenv("ROOM_EVIDENCE_SECRET_TEST_KEY", "sk-from-environment")
	manifest := rooms.Manifest{Participants: []rooms.Participant{{ID: "a", APIKeyEnv: "ROOM_EVIDENCE_SECRET_TEST_KEY"}}}
	if got := EvidenceSecrets(manifest, rooms.RoomRunOptions{}); !reflect.DeepEqual(got, []string{"sk-from-environment"}) {
		t.Fatalf("evidence secrets = %q, want the environment credential", got)
	}
}
