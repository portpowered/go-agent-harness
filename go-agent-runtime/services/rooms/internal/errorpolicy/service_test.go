package errorpolicy

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestParticipantFailurePreservesIdentityAndRedactsCause(t *testing.T) {
	sentinel := errors.New("provider authorization: bearer secret-token")
	service := New()
	err := service.ParticipantFailure(rooms.ParticipantFailureRequest{
		ParticipantID: "alice", Cause: sentinel, Secrets: []string{"secret-token"},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("participant failure does not preserve cause identity: %v", err)
	}
	id, ok := service.ParticipantFailureID(err)
	if !ok || id != "alice" {
		t.Fatalf("participant failure identity = %q/%t, want alice/true", id, ok)
	}
	if strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("participant failure leaked or failed to redact cause: %q", err)
	}
}
