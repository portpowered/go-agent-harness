package embedding_test

import (
	"testing"
	"time"
)

// publicRoomFinalTurnHoldWindow is the real-time window in which a room that
// ignores undelivered final audio would already have stopped. The room clock
// is deterministic and not advanced during it, so the final frame stays in
// the peer's mixer until the test releases it.
const publicRoomFinalTurnHoldWindow = 200 * time.Millisecond

// TestServiceDeliversFinalTurnAudioWhenMessageEndPrecedesPlayback pins the
// max_turns stop against real provider ordering: response.done (MESSAGE.END)
// arrives while the final response's audio is still queued for the peer. The
// room must keep running until that audio has been handed to the peer, then
// stop for max_turns.
func TestServiceDeliversFinalTurnAudioWhenMessageEndPrecedesPlayback(t *testing.T) {
	run := newPublicRoomLatencyRun(t)
	defer run.cancel()
	run.start(t)
	run.waitReady(t)
	run.provider.audioEvents = run.audioEvents
	run.provider.fanouts = run.fanouts
	run.playOpening(t)
	for _, turn := range []struct{ participantID, peerID, responseID string }{
		{run.listenerID, run.speakerID, "response-listener-01"},
		{run.speakerID, run.listenerID, "response-speaker-02"},
	} {
		if responseID := publicRoomLatencyTurn(t, run.provider, run.clock, run.responseStarts, run.audioEvents, run.fanouts, turn.participantID, turn.peerID, turn.responseID, run.pcmFixture); responseID == "" {
			t.Fatalf("%s did not produce a response", turn.responseID)
		}
	}

	// Final turn: the provider publishes the audio and then MESSAGE.END before
	// the room clock lets the mixer hand the audio to the peer.
	waitPublicRoomLatencyInput(t, run.provider.inputEvents, run.listenerID, run.pcmFixture)
	run.clock.AdvanceTo(run.clock.Tick() + 60)
	if err := run.provider.stopSpeech(run.listenerID); err != nil {
		t.Fatalf("stop listener speech: %v", err)
	}
	start := waitPublicRoomLatencyResponseStart(t, run.responseStarts, run.listenerID, "response-listener-02")
	advancePublicRoomLatencyResponse(run.clock, start)
	// Let mixer wakes due at the advanced tick drain so the final frame waits
	// for the next room-clock period.
	time.Sleep(publicRoomLatencyMixerSettle)
	if err := run.provider.releaseResponse(run.listenerID, start.responseID, run.pcmFixture); err != nil {
		t.Fatalf("release final listener response: %v", err)
	}
	waitPublicRoomLatencyAudio(t, run.audioEvents, run.listenerID, start.responseID, run.pcmFixture, start.tick+600)
	run.provider.completeTurn(run.listenerID)

	select {
	case outcome := <-run.runDone:
		// Stopping is correct only once the peer already holds the audio.
		assertPublicRoomFinalFanoutDelivered(t, run)
		assertPublicRoomLatencyOutcome(t, run, outcome)
		return
	case <-time.After(publicRoomFinalTurnHoldWindow):
	}

	waitPublicRoomLatencyFanout(t, run.clock, run.fanouts, run.listenerID, run.speakerID, run.pcmFixture)
	assertPublicRoomLatencyOutcome(t, run, run.waitOutcome(t))
}

func assertPublicRoomFinalFanoutDelivered(t *testing.T, run *publicRoomLatencyRun) {
	t.Helper()
	select {
	case fanout := <-run.fanouts:
		if fanout.sourceID != run.listenerID || fanout.targetID != run.speakerID {
			t.Fatalf("final fanout = %+v, want %s -> %s", fanout, run.listenerID, run.speakerID)
		}
	default:
		t.Fatal("room stopped before the final response reached the peer")
	}
}
