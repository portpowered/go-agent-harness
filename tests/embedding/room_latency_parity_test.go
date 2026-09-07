package embedding_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"path/filepath"
	"testing"
	"time"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const publicRoomLatencyTestTimeout = 3 * time.Second

// TestServicePreservesHermeticTurnToTurnLatency is the public-contract
// replacement for the old CLI RunRoomWithResult hermetic test. It keeps the
// causal assertions that matter at the service boundary: source PCM is
// delivered to peers without self-hearing, two turns are admitted per agent,
// and the finalized room latency artifact remains reproducible.
func TestServicePreservesHermeticTurnToTurnLatency(t *testing.T) {
	run := newPublicRoomLatencyRun(t)
	defer run.cancel()
	run.start(t)
	run.waitReady(t)
	run.provider.audioEvents = run.audioEvents
	run.provider.fanouts = run.fanouts
	run.playOpening(t)
	run.playTurns(t)
	outcome := run.waitOutcome(t)
	assertPublicRoomLatencyOutcome(t, run, outcome)
	assertPublicRoomLatencyReport(t, run)
}

type publicRoomLatencyRun struct {
	speakerID, listenerID string
	pcmFixture            []byte
	clock                 *platformclock.Deterministic
	provider              *publicRoomLatencyProvider
	roomService           runtimeRooms.Service
	manifest              runtimeRooms.Manifest
	outputDir             string
	responseStarts        chan publicRoomLatencyResponseStart
	audioEvents           chan publicRoomLatencyAudio
	fanouts               chan publicRoomLatencyFanout
	ready                 chan string
	roomCtx               context.Context
	cancel                context.CancelFunc
	runDone               chan publicRoomLatencyRunOutcome
}

func newPublicRoomLatencyRun(t *testing.T) *publicRoomLatencyRun {
	t.Helper()
	run := &publicRoomLatencyRun{
		speakerID:      "speaker",
		listenerID:     "listener",
		pcmFixture:     publicRoomLatencyPCMFixture(),
		clock:          platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond),
		responseStarts: make(chan publicRoomLatencyResponseStart, 8),
		audioEvents:    make(chan publicRoomLatencyAudio, 8),
		fanouts:        make(chan publicRoomLatencyFanout, 8),
	}
	run.provider = newPublicRoomLatencyProvider([]string{run.speakerID, run.listenerID}, run.clock)
	run.roomService = roomswire.NewService(roomswire.Dependencies{Live: &publicRoomLatencyLiveService{provider: run.provider}, Clock: run.clock})
	run.manifest = runtimeRooms.Manifest{
		SchemaVersion: runtimeRooms.SchemaVersion,
		Room:          runtimeRooms.Room{MaxTurns: 2, MaxDuration: publicRoomLatencyTestTimeout},
		Participants: []runtimeRooms.Participant{
			{ID: run.speakerID, SystemPrompt: "speaker system", OpeningPrompt: "start", Provider: "scripted", Model: "scripted-model", APIKeyEnv: "ROOM_SPEAKER_KEY", Tools: []string{}},
			{ID: run.listenerID, SystemPrompt: "listener system", Provider: "scripted", Model: "scripted-model", APIKeyEnv: "ROOM_LISTENER_KEY", Tools: []string{}},
		},
	}
	run.outputDir = filepath.Join(t.TempDir(), "room-latency")
	run.ready = make(chan string, len(run.manifest.Participants))
	run.roomCtx, run.cancel = context.WithTimeout(context.Background(), publicRoomLatencyTestTimeout)
	run.runDone = make(chan publicRoomLatencyRunOutcome, 1)
	return run
}

func (run *publicRoomLatencyRun) start(t *testing.T) {
	t.Helper()
	go func() {
		result, err := run.roomService.Run(run.roomCtx, io.Discard, runtimeRooms.RoomRunOptions{
			Manifest:    run.manifest,
			OutputDir:   run.outputDir,
			AudioFormat: runtimeRooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond},
			OnParticipantReady: func(value runtimeRooms.RoomParticipantReady) {
				run.ready <- value.ParticipantID
			},
			OnDiagnostic: func(participantID string, record runtimeRooms.RoomDiagnosticRecord) {
				if record.Event != "live_message_start" {
					return
				}
				run.responseStarts <- publicRoomLatencyResponseStart{
					participantID: participantID,
					responseID:    run.provider.currentResponseID(participantID),
					tick:          run.clock.Tick(),
					at:            record.At,
				}
			},
		})
		run.runDone <- publicRoomLatencyRunOutcome{result: result, err: err}
	}()
}

func (run *publicRoomLatencyRun) waitReady(t *testing.T) {
	t.Helper()
	for range run.manifest.Participants {
		select {
		case <-run.ready:
		case <-time.After(publicRoomLatencyTestTimeout):
			t.Fatal("room participants did not reach the public ready boundary")
		}
	}
}

func (run *publicRoomLatencyRun) playOpening(t *testing.T) {
	t.Helper()
	openingID, err := run.provider.startOpening(run.speakerID, run.pcmFixture)
	if err != nil {
		t.Fatalf("start opening response: %v", err)
	}
	openingStart := waitPublicRoomLatencyResponseStart(t, run.responseStarts, run.speakerID, openingID)
	advancePublicRoomLatencyResponse(run.clock, openingStart)
	if err := run.provider.releaseResponse(run.speakerID, openingID, run.pcmFixture); err != nil {
		t.Fatalf("release opening response: %v", err)
	}
	waitPublicRoomLatencyAudio(t, run.audioEvents, run.speakerID, openingID, run.pcmFixture, openingStart.tick+600)
	advancePublicRoomLatencyMixer(run.clock)
	waitPublicRoomLatencyFanout(t, run.fanouts, run.speakerID, run.listenerID, run.pcmFixture)
	run.provider.completeTurn(run.speakerID)
}

func (run *publicRoomLatencyRun) playTurns(t *testing.T) {
	t.Helper()
	turns := []struct {
		participantID, peerID, responseID string
	}{
		{run.listenerID, run.speakerID, "response-listener-01"},
		{run.speakerID, run.listenerID, "response-speaker-02"},
		{run.listenerID, run.speakerID, "response-listener-02"},
	}
	for _, turn := range turns {
		responseID := publicRoomLatencyTurn(t, run.provider, run.clock, run.responseStarts, run.audioEvents, run.fanouts, turn.participantID, turn.peerID, turn.responseID, run.pcmFixture)
		if responseID == "" {
			t.Fatalf("%s did not produce a response", turn.responseID)
		}
	}
}

func (run *publicRoomLatencyRun) waitOutcome(t *testing.T) publicRoomLatencyRunOutcome {
	t.Helper()
	select {
	case outcome := <-run.runDone:
		return outcome
	case <-time.After(publicRoomLatencyTestTimeout):
		t.Fatal("room did not terminate after the final admitted response")
		return publicRoomLatencyRunOutcome{}
	}
}

func assertPublicRoomLatencyOutcome(t *testing.T, run *publicRoomLatencyRun, outcome publicRoomLatencyRunOutcome) {
	t.Helper()
	if outcome.err != nil {
		t.Fatalf("public room latency run: %v", outcome.err)
	}
	if outcome.result.Reason != runtimeRooms.RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", outcome.result.Reason, runtimeRooms.RoomTerminationMaxTurnsReached)
	}
	for _, participantID := range []string{run.speakerID, run.listenerID} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("room result missing participant %q", participantID)
		}
		if participantResult.TurnsCompleted != 2 {
			t.Fatalf("participant %q turns = %d, want 2", participantID, participantResult.TurnsCompleted)
		}
	}
	run.provider.assertScriptedTurns(t, run.speakerID, 2, [][]byte{run.pcmFixture, run.pcmFixture})
	run.provider.assertScriptedTurns(t, run.listenerID, 2, [][]byte{run.pcmFixture, run.pcmFixture})
	run.provider.assertNoOutboundResponseControls(t)
	run.provider.assertInputFrames(t, map[string]int{run.speakerID: 1, run.listenerID: 2}, run.pcmFixture)
}

func assertPublicRoomLatencyReport(t *testing.T, run *publicRoomLatencyRun) {
	t.Helper()
	report, err := roomswire.NewLatencyService().Report(run.outputDir)
	if err != nil {
		t.Fatalf("read finalized room latency report: %v", err)
	}
	assertPublicRoomLatencyCounts(t, report)
	assertPublicRoomLatencyTransitions(t, report)
	bundle, err := roomswire.NewLatencyService().ReadBundle(filepath.Join(run.outputDir, runtimeRooms.RoomLatencyArtifactPath))
	if err != nil {
		t.Fatalf("read finalized latency bundle: %v", err)
	}
	if len(bundle.Events) == 0 {
		t.Fatal("finalized latency bundle has no events")
	}
	derived, err := roomswire.NewLatencyService().AnalyzeBundle(bundle)
	if err != nil {
		t.Fatalf("reanalyze finalized latency bundle: %v", err)
	}
	if derived.EligibleCount != report.EligibleCount || derived.Summary != report.Summary {
		t.Fatalf("report is not reproducible from finalized bundle: read=%+v derived=%+v", report.Summary, derived.Summary)
	}
}

func assertPublicRoomLatencyCounts(t *testing.T, report runtimeRooms.RoomLatencyReport) {
	t.Helper()
	if report.EligibleCount != 3 || report.ExcludedCount != 1 {
		t.Fatalf("latency report counts = eligible %d excluded %d, want 3/1; transitions=%+v exclusions=%+v", report.EligibleCount, report.ExcludedCount, report.Transitions, report.Exclusions)
	}
	if len(report.Exclusions) != 1 || report.Exclusions[0].Reason != runtimeRooms.RoomLatencyReasonUncorrelatedLandmarks || report.Exclusions[0].EventCount != 1 {
		t.Fatalf("latency exclusion = %+v, want one terminal uncorrelated event", report.Exclusions)
	}
}

func assertPublicRoomLatencyTransitions(t *testing.T, report runtimeRooms.RoomLatencyReport) {
	t.Helper()
	want := map[string]string{
		"listener-turn-000001": "response-listener-01",
		"speaker-turn-000001":  "response-speaker-02",
		"listener-turn-000002": "response-listener-02",
	}
	if len(report.Transitions) != len(want) {
		t.Fatalf("latency transitions = %d, want %d: %+v", len(report.Transitions), len(want), report.Transitions)
	}
	for _, transition := range report.Transitions {
		wantResponseID, ok := want[transition.TransitionID]
		if !ok {
			t.Fatalf("unexpected latency transition = %+v", transition)
		}
		assertPublicRoomLatencyTransition(t, transition, wantResponseID)
	}
}

func assertPublicRoomLatencyTransition(t *testing.T, transition runtimeRooms.RoomLatencyTransition, wantResponseID string) {
	t.Helper()
	if !transition.Eligible || transition.ResponseID != wantResponseID {
		t.Fatalf("transition %q = %+v, want eligible response %q", transition.TransitionID, transition, wantResponseID)
	}
	if transition.ProviderMS < 599 || transition.ProviderMS > 601 {
		t.Fatalf("transition %q provider bucket = %d ms, want 600 +/- one clock tick", transition.TransitionID, transition.ProviderMS)
	}
	if transition.HarnessOwnedMS > 400 {
		t.Fatalf("transition %q harness-owned latency = %d ms, want <=400", transition.TransitionID, transition.HarnessOwnedMS)
	}
	if difference := transition.DirectGapMS - transition.FourBucketSumMS; difference > 21 || difference < -21 {
		t.Fatalf("transition %q direct/four-bucket gap = %d/%d ms, want <=20 ms plus rounding", transition.TransitionID, transition.DirectGapMS, transition.FourBucketSumMS)
	}
	if transition.TotalMS != transition.DirectGapMS {
		t.Fatalf("transition %q total = %d ms, want direct gap %d", transition.TransitionID, transition.TotalMS, transition.DirectGapMS)
	}
}

func publicRoomLatencyTurn(t *testing.T, provider *publicRoomLatencyProvider, clock *platformclock.Deterministic, starts <-chan publicRoomLatencyResponseStart, audioEvents <-chan publicRoomLatencyAudio, fanouts <-chan publicRoomLatencyFanout, participantID, peerID, responseID string, pcm []byte) string {
	t.Helper()
	waitPublicRoomLatencyInput(t, provider.inputEvents, participantID, pcm)
	clock.AdvanceTo(clock.Tick() + 60)
	if err := provider.stopSpeech(participantID); err != nil {
		t.Fatalf("stop %s speech: %v", participantID, err)
	}
	start := waitPublicRoomLatencyResponseStart(t, starts, participantID, responseID)
	advancePublicRoomLatencyResponse(clock, start)
	if err := provider.releaseResponse(participantID, start.responseID, pcm); err != nil {
		t.Fatalf("release %s response: %v", participantID, err)
	}
	waitPublicRoomLatencyAudio(t, audioEvents, participantID, start.responseID, pcm, start.tick+600)
	advancePublicRoomLatencyMixer(clock)
	waitPublicRoomLatencyFanout(t, fanouts, participantID, peerID, pcm)
	provider.completeTurn(participantID)
	return start.responseID
}

func publicRoomLatencyPCMFixture() []byte {
	samples := []int16{0, 1, -1, 0, 2, -2, 0, 3, -3, 0, 4, -4, 0, 5, -5, 0, 1600, -1600, 0, 0}
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func advancePublicRoomLatencyResponse(clock *platformclock.Deterministic, start publicRoomLatencyResponseStart) {
	clock.AdvanceTo(start.tick + 600)
	if target := start.at.Add(600 * time.Millisecond); target.After(clock.Now()) {
		clock.AdvanceBy(target.Sub(clock.Now()))
	}
}

func waitPublicRoomLatencyResponseStart(t *testing.T, starts <-chan publicRoomLatencyResponseStart, participantID, responseID string) publicRoomLatencyResponseStart {
	t.Helper()
	select {
	case start := <-starts:
		if start.participantID != participantID || start.responseID != responseID {
			t.Fatalf("response start = %+v, want %s/%s", start, participantID, responseID)
		}
		return start
	case <-time.After(publicRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for response start %s/%s", participantID, responseID)
		return publicRoomLatencyResponseStart{}
	}
}

func waitPublicRoomLatencyInput(t *testing.T, inputs <-chan publicRoomLatencyInput, participantID string, wantPCM []byte) {
	t.Helper()
	select {
	case input := <-inputs:
		if input.participantID != participantID {
			t.Fatalf("input participant = %q, want %q", input.participantID, participantID)
		}
		if !bytes.Equal(input.pcm, wantPCM) {
			t.Fatalf("input PCM for %s = %v, want exact fixture %v", participantID, input.pcm, wantPCM)
		}
	case <-time.After(publicRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for non-empty input to %s", participantID)
	}
}

func waitPublicRoomLatencyAudio(t *testing.T, audioEvents <-chan publicRoomLatencyAudio, participantID, responseID string, wantPCM []byte, wantTick uint64) {
	t.Helper()
	select {
	case event := <-audioEvents:
		if event.participantID != participantID || event.responseID != responseID {
			t.Fatalf("audio event = %+v, want %s/%s", event, participantID, responseID)
		}
		if event.tick != wantTick {
			t.Fatalf("provider audio for %s/%s arrived at tick %d, want response release tick %d", participantID, responseID, event.tick, wantTick)
		}
		if !bytes.Equal(event.pcm, wantPCM) {
			t.Fatalf("audio PCM for %s = %v, want exact fixture %v", responseID, event.pcm, wantPCM)
		}
	case <-time.After(publicRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for provider audio for %s/%s", participantID, responseID)
	}
}

func advancePublicRoomLatencyMixer(clock *platformclock.Deterministic) {
	// Mixer timers are created asynchronously. A few bounded cadence advances
	// make the test independent of whether the worker installed its first timer
	// before or after the provider frame was admitted.
	for index := 0; index < 4; index++ {
		clock.AdvanceBy(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
}

func waitPublicRoomLatencyFanout(t *testing.T, fanouts <-chan publicRoomLatencyFanout, sourceID, targetID string, wantPCM []byte) {
	t.Helper()
	select {
	case fanout := <-fanouts:
		if fanout.sourceID != sourceID || fanout.targetID != targetID {
			t.Fatalf("fanout = %+v, want %s -> %s", fanout, sourceID, targetID)
		}
		if !bytes.Equal(fanout.pcm, wantPCM) {
			t.Fatalf("fanout PCM = %v, want exact fixture %v", fanout.pcm, wantPCM)
		}
	case <-time.After(publicRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for fanout %s -> %s", sourceID, targetID)
	}
}

var _ session.LiveService = (*publicRoomLatencyLiveService)(nil)
var _ session.LiveHandle = (*publicRoomLatencyLiveHandle)(nil)
