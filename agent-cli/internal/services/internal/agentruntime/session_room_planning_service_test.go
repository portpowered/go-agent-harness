package agentruntime

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

const roomTestSecret = "secret-alpha"

func TestBuildRoomParticipantPlansRuntimeAdapterPreservesCredentialBoundary(t *testing.T) {
	opts, factoryCalls := newRoomTestRunOptions([]string{"alpha"}, map[string]*roomTestInferencer{"alpha": {}})
	plans, secrets, err := buildRoomParticipantPlans(opts, validationOptionsForRoomTest(opts))
	if err != nil {
		t.Fatalf("buildRoomParticipantPlans: %v", err)
	}
	if len(plans) != 1 || plans[0].startupErr != nil {
		t.Fatalf("plans = %#v, want one clean plan", plans)
	}
	if factoryCalls["alpha"].APIKey != roomTestSecret || plans[0].options.APIKey != roomTestSecret {
		t.Fatalf("credential projection = factory %q/plan %q, want %s", factoryCalls["alpha"].APIKey, plans[0].options.APIKey, roomTestSecret)
	}
	if len(secrets) != 1 || secrets[0] != roomTestSecret {
		t.Fatalf("redaction secrets = %q, want one participant secret", secrets)
	}
	if strings.Contains(plans[0].manifest.SystemPrompt, plans[0].options.APIKey) {
		t.Fatal("credential was copied into participant manifest text")
	}
}

func validationOptionsForRoomTest(opts RoomRunOptions) room.ValidationOptions {
	return room.ValidationOptions{LookupCredential: opts.CredentialLookup}
}
