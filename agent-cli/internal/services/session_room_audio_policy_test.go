package services

import (
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRoomCoordinatorAudioInputPolicyClassifiesExactMixedSources(t *testing.T) {
	coordinator := newRoomCoordinator(nil, 0, nil)
	coordinator.addParticipant(&roomParticipantRuntime{
		plan: &roomParticipantPlan{manifest: room.Participant{ID: "agent-a", Kind: room.ParticipantKindAgent}},
	})
	coordinator.addParticipant(&roomParticipantRuntime{
		plan: &roomParticipantPlan{manifest: room.Participant{ID: "agent-b", Kind: room.ParticipantKindAgent}},
	})
	coordinator.addParticipant(&roomParticipantRuntime{
		plan: &roomParticipantPlan{manifest: room.Participant{ID: "customer", Kind: room.ParticipantKindCustomer}},
	})

	tests := []struct {
		name    string
		sources []string
		want    messages.SessionAudioInputPolicy
	}{
		{name: "peer agents only", sources: []string{"agent-a", "agent-b"}, want: messages.SessionAudioInputPolicyDoNotInterrupt},
		{name: "human contributor", sources: []string{"agent-a", "customer"}, want: messages.SessionAudioInputPolicyInterrupt},
		{name: "customer only", sources: []string{"customer"}, want: messages.SessionAudioInputPolicyInterrupt},
		{name: "unknown source", sources: []string{"agent-a", "not-in-room"}, want: messages.SessionAudioInputPolicyDefault},
		{name: "empty source set", sources: nil, want: messages.SessionAudioInputPolicyDefault},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := coordinator.audioInputPolicy(test.sources); got != test.want {
				t.Fatalf("audioInputPolicy(%v) = %q, want %q", test.sources, got, test.want)
			}
		})
	}
}
