package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// TestRunnerBoundsFinalTurnDeliveryWaitOnRoomClock pins the fallback of the
// max_turns final-audio hold: when a speaker produced audio for its final
// turn but no response boundary ever reaches the peer, the room keeps running
// on the room clock and stops for max_turns once that speaker has been quiet
// for the bounded window — never on wall-clock time.
func TestRunnerBoundsFinalTurnDeliveryWaitOnRoomClock(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	alice, bob := newFakeLiveHandle(), newFakeLiveHandle()
	alice.startEvents = []session.LiveEvent{liveStreamEvent("alice", messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "resp", Value: messages.NewAudioDeltaValue([]byte{1, 0, 2, 0}),
	})}
	for _, handle := range []*fakeLiveHandle{alice, bob} {
		handle.media = audio.MediaEndpoints{Inbound: silentInbound{}, Outbound: &recordingOutbound{}}
	}
	runner := New(Dependencies{Live: &fakeLiveService{handles: map[string]*fakeLiveHandle{"alice": alice, "bob": bob}}, Clock: clock})

	turnEnds := make(chan string, 2)
	type outcome struct {
		result rooms.RoomResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runner.Run(context.Background(), nil, rooms.RoomRunOptions{
			Manifest: testManifest(),
			OnDiagnostic: func(participantID string, record rooms.RoomDiagnosticRecord) {
				if record.Fields["kind"] == string(messages.StreamTypeMessageEnd) {
					turnEnds <- participantID
				}
			},
		})
		done <- outcome{result: result, err: err}
	}()
	for range 2 {
		select {
		case <-turnEnds:
		case <-time.After(5 * time.Second):
			t.Fatal("participants did not complete their turns")
		}
	}

	select {
	case got := <-done:
		t.Fatalf("room stopped before alice's final audio could reach bob: %+v err=%v", got.result, got.err)
	case <-time.After(100 * time.Millisecond):
	}

	clock.AdvanceBy(finalTurnDeliveryQuiet)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run error = %v", got.err)
		}
		if got.result.TerminationReason != rooms.RoomTerminationMaxTurnsReached {
			t.Fatalf("room termination = %q, want max turns", got.result.TerminationReason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("room did not stop after the bounded final-turn delivery window")
	}
}

// silentInbound is a provider track that never produces audio before the
// room stops.
type silentInbound struct{}

func (silentInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	<-ctx.Done()
	return audio.PCMFrame{}, ctx.Err()
}

func (silentInbound) Close() error { return nil }
