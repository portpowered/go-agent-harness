package wire

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Each participant speaks a distinct bit so a delivered mix reveals exactly
// whose audio it contains: peers must be heard, and a participant's own
// audio must never be routed back to it.
func TestServiceRoutesPeerPCMToEachParticipantButNeverItsOwn(t *testing.T) {
	live := newContractLive()
	live.media = true
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	voices := map[string]int16{alphaID: 1, betaID: 2, gammaID: 4}
	done, ready := startRun(ctx, service, rooms.RoomRunOptions{
		Manifest:    agentRoom(rooms.Room{Interactive: true}, alphaID, betaID, gammaID),
		AudioFormat: rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 2 * time.Millisecond},
	})
	awaitReady(t, ready, len(voices))
	for id, voice := range voices {
		handle := live.handle(t, id)
		for range 4 {
			handle.inbound.frames <- audio.PCMFrame{Samples: []int16{voice, voice}}
		}
	}
	for id, own := range voices {
		heard := collectHeardVoices(t, live.handle(t, id), own, 7^own)
		if heard != 7^own {
			t.Fatalf("participant %q heard voices %03b, want every peer %03b", id, heard, 7^own)
		}
	}
	cancel()
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationStopped {
		t.Fatalf("room = %+v / %v, want a clean stop", outcome.result, outcome.err)
	}
}

func collectHeardVoices(t *testing.T, handle *contractHandle, own, want int16) int16 {
	t.Helper()
	var heard int16
	deadline := time.After(contractWait)
	for heard != want {
		select {
		case frame := <-handle.outbound.frames:
			for _, sample := range frame.Samples {
				if sample&own != 0 {
					t.Fatalf("participant voice %03b was routed back to itself in %v", own, frame.Samples)
				}
				heard |= sample
			}
		case <-deadline:
			return heard
		}
	}
	return heard
}

func TestServiceKeepsViableRoomRunningAfterOneParticipantEnds(t *testing.T) {
	live := newContractLive()
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ready := startRun(ctx, service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, alphaID, betaID, gammaID)})
	awaitReady(t, ready, 3)
	alpha := live.handle(t, alphaID)
	alpha.finish(nil)
	waitFor(t, "alpha close", func() bool { _, closes := alpha.counts(); return closes == 1 })
	for _, id := range []string{betaID, gammaID} {
		if cancels, closes := live.handle(t, id).counts(); cancels != 0 || closes != 0 {
			t.Fatalf("peer %q cancels=%d closes=%d after alpha ended, want untouched", id, cancels, closes)
		}
	}
	select {
	case outcome := <-done:
		t.Fatalf("room stopped after one participant ended: %+v", outcome)
	default:
	}
	cancel()
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationStopped {
		t.Fatalf("room = %+v / %v, want a clean stop", outcome.result, outcome.err)
	}
	for _, id := range []string{alphaID, betaID, gammaID} {
		assertParticipant(t, outcome.result, id, rooms.ParticipantTerminationEnded)
	}
}

func TestServiceCompletesWhenEveryParticipantEnds(t *testing.T) {
	live := newContractLive()
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	done, ready := startRun(context.Background(), service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, alphaID, betaID, gammaID)})
	awaitReady(t, ready, 3)
	for _, id := range []string{alphaID, betaID, gammaID} {
		live.handle(t, id).finish(nil)
	}
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationStopped || len(outcome.result.ActiveParticipants) != 0 {
		t.Fatalf("room = %+v / %v, want a clean stop with nobody active", outcome.result, outcome.err)
	}
	for _, id := range []string{alphaID, betaID, gammaID} {
		assertParticipant(t, outcome.result, id, rooms.ParticipantTerminationEnded)
	}
}

func TestServiceStopsEveryParticipantAtMaxDuration(t *testing.T) {
	live := newContractLive()
	scheduler := clock.NewDeterministic(time.Unix(1_700_000_000, 0), time.Millisecond)
	service := NewService(Dependencies{Live: live, Clock: scheduler})
	done, ready := startRun(context.Background(), service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{MaxDuration: 3 * time.Second}, alphaID, betaID)})
	awaitReady(t, ready, 2)
	scheduler.AdvanceBy(3 * time.Second)
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationMaxDurationReached {
		t.Fatalf("room = %+v / %v, want max duration", outcome.result, outcome.err)
	}
	for _, id := range []string{alphaID, betaID} {
		assertParticipant(t, outcome.result, id, rooms.ParticipantTerminationEnded)
		if cancels, closes := live.handle(t, id).counts(); cancels == 0 || closes != 1 {
			t.Fatalf("participant %q cancels=%d closes=%d, want bound cancellation and one close", id, cancels, closes)
		}
	}
}

func TestServiceKeepsParticipantFailureCauseWhenRoomIsCancelled(t *testing.T) {
	providerErr := errors.New("provider transport reset")
	live := newContractLive()
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ready := startRun(ctx, service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, alphaID, betaID)})
	awaitReady(t, ready, 2)
	alpha := live.handle(t, alphaID)
	alpha.finish(providerErr)
	waitFor(t, "alpha close", func() bool { _, closes := alpha.counts(); return closes == 1 })
	cancel()
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.result.TerminationReason != rooms.RoomTerminationStopped {
		t.Fatalf("room = %+v / %v, want the surviving peer's clean stop", outcome.result, outcome.err)
	}
	failed := assertParticipant(t, outcome.result, alphaID, rooms.ParticipantTerminationError)
	if failed.Connected || !strings.Contains(failed.Error, providerErr.Error()) {
		t.Fatalf("failed participant = %+v, want its own provider cause", failed)
	}
	assertParticipant(t, outcome.result, betaID, rooms.ParticipantTerminationEnded)
}

func TestServiceReturnsTypedFailureWhenEveryParticipantFails(t *testing.T) {
	live := newContractLive()
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	done, ready := startRun(context.Background(), service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, bravoID, alphaID)})
	awaitReady(t, ready, 2)
	live.handle(t, bravoID).finish(errors.New("bravo dial failed"))
	live.handle(t, alphaID).finish(errors.New("alpha dial failed"))
	outcome := awaitOutcome(t, done)
	var failure *rooms.AllParticipantsFailedError
	if !errors.Is(outcome.err, rooms.ErrAllParticipantsFailed) || !errors.As(outcome.err, &failure) {
		t.Fatalf("room error = %v, want typed all-participants failure", outcome.err)
	}
	if len(failure.Participants) != 2 || failure.Participants[0].ParticipantID != alphaID || failure.Participants[1].ParticipantID != bravoID {
		t.Fatalf("failure participants = %+v, want sorted identities", failure.Participants)
	}
	if !strings.HasPrefix(outcome.err.Error(), "room run: all 2 participant(s) failed (alpha: ") || !strings.Contains(outcome.err.Error(), "; bravo: ") {
		t.Fatalf("room error = %q, want sorted participant causes", outcome.err)
	}
	if outcome.result.TerminationReason != rooms.RoomTerminationStopped || len(outcome.result.Participants) != 2 {
		t.Fatalf("room result = %+v, want both participant results reported", outcome.result)
	}
}

func TestServiceReportsEveryParticipantFailureOnlyWhenNobodySurvives(t *testing.T) {
	live := newContractLive()
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	done, ready := startRun(context.Background(), service, rooms.RoomRunOptions{Manifest: agentRoom(rooms.Room{Interactive: true}, alphaID, betaID)})
	awaitReady(t, ready, 2)
	live.handle(t, alphaID).finish(errors.New("alpha dial failed"))
	live.handle(t, betaID).finish(nil)
	outcome := awaitOutcome(t, done)
	if outcome.err != nil {
		t.Fatalf("partial failure error = %v, want the surviving peer to keep the run successful", outcome.err)
	}
	assertParticipant(t, outcome.result, alphaID, rooms.ParticipantTerminationError)
	assertParticipant(t, outcome.result, betaID, rooms.ParticipantTerminationEnded)
}

func TestServicePassesEachParticipantsSessionFactsToLive(t *testing.T) {
	live := newContractLive()
	service := NewService(Dependencies{Live: live, Clock: clock.Real{}})
	manifest := agentRoom(rooms.Room{Interactive: true}, alphaID, betaID)
	manifest.Participants[0].Voice, manifest.Participants[0].Tools = "alloy", []string{"read_file"}
	manifest.Participants[1].OpeningPrompt = ""
	var readiness []rooms.RoomParticipantReady
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, ready := startRun(ctx, service, rooms.RoomRunOptions{Manifest: manifest, OnParticipantReady: func(value rooms.RoomParticipantReady) { readiness = append(readiness, value) }})
	awaitReady(t, ready, 2)
	alpha, beta := live.request(t, alphaID), live.request(t, betaID)
	if alpha.SessionID != alphaID || alpha.Voice != "alloy" || alpha.OpeningPrompt != "start" || alpha.Instructions != alphaID || alpha.CredentialReference != "UNRESOLVED_alpha" || len(alpha.ToolNames) != 1 || alpha.ToolNames[0] != "read_file" {
		t.Fatalf("alpha live request = %+v, want its own manifest facts", alpha)
	}
	if beta.Voice != "" || beta.OpeningPrompt != "" || len(beta.ToolNames) != 0 || !beta.ProviderLiveness.Enabled {
		t.Fatalf("beta live request = %+v, want provider-default voice, no opener, and no tools", beta)
	}
	cancel()
	awaitOutcome(t, done)
	for _, value := range readiness {
		if value.Kind != rooms.ParticipantKindAgent || value.Provider != fakeProvider || value.Model != fakeModel || value.InputDevice != "" {
			t.Fatalf("agent readiness = %+v, want provider facts without devices", value)
		}
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(contractWait)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
