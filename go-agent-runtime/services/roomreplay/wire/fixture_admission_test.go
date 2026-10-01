package wire

import (
	"path/filepath"
	"testing"
)

// The committed room-audio fixtures are replay bundles first: room replay must
// admit each one, including its provider captures, before room evidence
// projects the audio (see roomevidence/wire golden tests).
func TestCommittedRoomAudioFixturesAdmitAsReplayPlans(t *testing.T) {
	t.Parallel()
	service := NewService(fixtureCaptureInspector{})
	for _, name := range []string{"clean-turn-taking", "deliberate-overlap", "long-conversation-termination"} {
		plan, err := service.Load(t.Context(), filepath.Join("testdata", "room-audio", name))
		if err != nil {
			t.Fatalf("admit %s: %v", name, err)
		}
		if len(plan.Participants) != 2 || len(plan.Timeline) == 0 {
			t.Fatalf("admit %s: participants=%d timeline=%d, want two participants and a timeline", name, len(plan.Participants), len(plan.Timeline))
		}
	}
}
