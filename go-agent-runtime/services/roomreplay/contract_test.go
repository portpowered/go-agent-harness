package roomreplay_test

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
)

func TestRoomReplayBundleErrorClassificationAndProjection(t *testing.T) {
	mismatch := &roomreplay.RoomReplayBundleError{Kind: roomreplay.RoomReplayBundleMismatch}
	if !errors.Is(mismatch, roomreplay.ErrInvalidRoomReplayBundle) {
		t.Fatal("mismatch error did not classify as invalid bundle")
	}
	incomplete := &roomreplay.RoomReplayBundleError{Kind: roomreplay.RoomReplayBundleIncomplete}
	if !errors.Is(incomplete, roomreplay.ErrRoomReplayBundleIncomplete) {
		t.Fatal("incomplete error did not classify as incomplete bundle")
	}

	plan := roomreplay.RoomReplayPlan{Participants: []roomreplay.RoomReplayParticipant{
		{ID: "agent", Kind: roomreplay.ParticipantKindAgent, Provider: "openai", Model: "model", Artifacts: []roomreplay.RoomReplayArtifact{{Path: "a"}}},
		{ID: "human", Kind: roomreplay.ParticipantKindHuman, Provider: "ignored", Model: "ignored"},
	}}
	participant, ok := plan.Participant("agent")
	if !ok {
		t.Fatal("Participant did not find stable ID")
	}
	participant.Artifacts[0].Path = "mutated"
	original, _ := plan.Participant("agent")
	if original.Artifacts[0].Path != "a" {
		t.Fatalf("Participant returned mutable plan state: %+v", original.Artifacts)
	}
}
