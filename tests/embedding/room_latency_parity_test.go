package embedding_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
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
	assertPublicRoomEvidenceBundle(t, run)
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
	run.roomService = roomswire.NewService(roomswire.Dependencies{
		Live: &publicRoomLatencyLiveService{provider: run.provider}, Clock: run.clock,
		Evidence: roomevidencewire.NewService(), Latency: roomevidencewire.NewLatencyService(),
	})
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
	// The room builds its media graph after the ready callbacks. Start the
	// virtual clock only once every peer mixer armed its first cadence timer,
	// so the mixer cadence stays aligned with the room clock's origin.
	run.waitMixersIdle(t, publicRoomLatencyIdleTimers)
}

// publicRoomLatencyIdleTimers is the number of room-clock timers pending
// while the room is idle between clock advances: the room's max-duration
// deadline plus one cadence timer per peer mixer (speaker and listener).
const publicRoomLatencyIdleTimers = 3

// publicRoomLatencyFramePeriod is the negotiated mixer cadence.
const publicRoomLatencyFramePeriod = 20 * time.Millisecond

// waitMixersIdle blocks until the room has armed timers pending timers.
// Deterministic timers are never pending once due, so a re-armed mixer
// timer proves the mixer processed every cadence up to the current tick and
// will not mix again until the test advances the clock.
func (run *publicRoomLatencyRun) waitMixersIdle(t *testing.T, timers int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(run.roomCtx, publicRoomLatencyTestTimeout)
	defer cancel()
	if err := run.clock.WaitForTimers(ctx, timers); err != nil {
		t.Fatalf("room did not arm %d room-clock timers: %v", timers, err)
	}
}

// deliverResponse releases one provider response at the provider latency
// landmark and drives it through the peer mixer: the response frame is
// admitted to the peer's mixer input, then exactly one mixer period elapses.
func (run *publicRoomLatencyRun) deliverResponse(t *testing.T, participantID, peerID string, start publicRoomLatencyResponseStart) {
	t.Helper()
	run.releaseResponse(t, participantID, start)
	run.advanceMixer(t, participantID)
	waitPublicRoomLatencyFanout(t, run.fanouts, participantID, peerID, run.pcmFixture)
}

// releaseResponse advances to the provider landmark, lets the mixers drain
// that advance, and publishes the response audio.
func (run *publicRoomLatencyRun) releaseResponse(t *testing.T, participantID string, start publicRoomLatencyResponseStart) {
	t.Helper()
	advancePublicRoomLatencyResponse(run.clock, start)
	run.waitMixersIdle(t, publicRoomLatencyIdleTimers)
	if err := run.provider.releaseResponse(participantID, start.responseID, run.pcmFixture); err != nil {
		t.Fatalf("release %s response %s: %v", participantID, start.responseID, err)
	}
	waitPublicRoomLatencyAudio(t, run.audioEvents, participantID, start.responseID, run.pcmFixture, start.tick+600)
}

// advanceMixer waits until the source's released frame sits in its peers'
// mixer inputs, then advances one mixer period. Every mixer's next cadence
// deadline is at most one period after the current tick, and a mixer that
// has not re-armed yet fires immediately when it does, so this single
// advance mixes the admitted frame without real-time settling.
func (run *publicRoomLatencyRun) advanceMixer(t *testing.T, sourceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(run.roomCtx, publicRoomLatencyTestTimeout)
	defer cancel()
	if err := run.provider.participant(sourceID).handle.inbound.waitAdmitted(ctx); err != nil {
		t.Fatalf("room did not admit %s audio to its peer mixers: %v", sourceID, err)
	}
	run.clock.AdvanceBy(publicRoomLatencyFramePeriod)
}

func (run *publicRoomLatencyRun) playOpening(t *testing.T) {
	t.Helper()
	openingID, err := run.provider.startOpening(run.speakerID, run.pcmFixture)
	if err != nil {
		t.Fatalf("start opening response: %v", err)
	}
	openingStart := waitPublicRoomLatencyResponseStart(t, run.responseStarts, run.speakerID, openingID)
	run.deliverResponse(t, run.speakerID, run.listenerID, openingStart)
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
		run.playTurn(t, turn.participantID, turn.peerID, turn.responseID)
	}
}

func (run *publicRoomLatencyRun) playTurn(t *testing.T, participantID, peerID, responseID string) {
	t.Helper()
	start := run.startTurn(t, participantID, responseID)
	run.deliverResponse(t, participantID, peerID, start)
	run.provider.completeTurn(participantID)
}

// startTurn waits for the peer audio to reach the participant, ends its
// speech after 60 ms, and waits for the resulting response to start.
func (run *publicRoomLatencyRun) startTurn(t *testing.T, participantID, responseID string) publicRoomLatencyResponseStart {
	t.Helper()
	waitPublicRoomLatencyInput(t, run.provider.inputEvents, participantID, run.pcmFixture)
	run.clock.AdvanceTo(run.clock.Tick() + 60)
	if err := run.provider.stopSpeech(participantID); err != nil {
		t.Fatalf("stop %s speech: %v", participantID, err)
	}
	return waitPublicRoomLatencyResponseStart(t, run.responseStarts, participantID, responseID)
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
	report, err := roomevidencewire.NewLatencyService().Report(run.outputDir)
	if err != nil {
		t.Fatalf("read finalized room latency report: %v", err)
	}
	assertPublicRoomLatencyCounts(t, report)
	assertPublicRoomLatencyTransitions(t, report)
	bundle, err := roomevidencewire.NewLatencyService().ReadBundle(filepath.Join(run.outputDir, roomevidence.LatencyPath))
	if err != nil {
		t.Fatalf("read finalized latency bundle: %v", err)
	}
	if len(bundle.Events) == 0 {
		t.Fatal("finalized latency bundle has no events")
	}
	derived, err := roomevidencewire.NewLatencyService().AnalyzeBundle(bundle)
	if err != nil {
		t.Fatalf("reanalyze finalized latency bundle: %v", err)
	}
	if derived.EligibleCount != report.EligibleCount || derived.Summary != report.Summary {
		t.Fatalf("report is not reproducible from finalized bundle: read=%+v derived=%+v", report.Summary, derived.Summary)
	}
}

func assertPublicRoomEvidenceBundle(t *testing.T, run *publicRoomLatencyRun) {
	t.Helper()
	evidence := roomevidencewire.NewService()
	plan, err := evidence.LoadPlan(run.outputDir)
	if err != nil {
		t.Fatalf("load room-produced evidence without credentials: %v", err)
	}
	bundle, err := evidence.Load(plan)
	if err != nil {
		t.Fatalf("load room-produced evidence bundle: %v", err)
	}
	analysis, err := evidence.Analyze(bundle)
	if err != nil {
		t.Fatalf("analyze room-produced evidence bundle: %v", err)
	}
	if len(bundle.Participants) != len(run.manifest.Participants) {
		t.Fatalf("room evidence participants = %d, want %d", len(bundle.Participants), len(run.manifest.Participants))
	}
	for _, participant := range bundle.Participants {
		wantPCM := bytes.Repeat(run.pcmFixture, 2)
		if participant.WAV.Role != roomevidence.RoomReplayAudioRoleWAV || !bytes.Equal(participant.WAV.PCM, wantPCM) {
			t.Fatalf("participant %q WAV role=%q bytes=%d, want role %q and %d exact bytes", participant.ID, participant.WAV.Role, len(participant.WAV.PCM), roomevidence.RoomReplayAudioRoleWAV, len(wantPCM))
		}
		found := false
		for _, stream := range analysis.Result.Streams {
			if stream.StreamID == participant.WAV.StreamID {
				found = stream.SampleCount == participant.WAV.SampleCount
				break
			}
		}
		if !found {
			t.Fatalf("analysis omitted exact WAV stream %q", participant.WAV.StreamID)
		}
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

// waitPublicRoomLatencyFanout waits for the mixed provider frame to reach its
// peer. The caller has already advanced the one mixer period that emits it;
// the timeout only bounds a broken run.
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
	run.playTurn(t, run.listenerID, run.speakerID, "response-listener-01")
	run.playTurn(t, run.speakerID, run.listenerID, "response-speaker-02")

	// Final turn: the provider publishes the audio and then MESSAGE.END. The
	// mixers already drained the release tick, so the frame stays in the
	// peer's mixer input until the test advances the room clock.
	start := run.startTurn(t, run.listenerID, "response-listener-02")
	run.releaseResponse(t, run.listenerID, start)
	run.provider.completeTurn(run.listenerID)
	run.assertFinalTurnHeld(t)

	run.advanceMixer(t, run.listenerID)
	waitPublicRoomLatencyFanout(t, run.fanouts, run.listenerID, run.speakerID, run.pcmFixture)
	assertPublicRoomLatencyOutcome(t, run, run.waitOutcome(t))
}

// assertFinalTurnHeld proves the room reached max_turns and holds its stop
// for the undelivered final audio. The final-turn delivery wait arms one room
// clock timer beside the idle ones; a room that ignored the queued audio
// stops instead. The mixers drained the release tick before the frame was
// published, so the peer cannot hold the frame until the clock advances.
func (run *publicRoomLatencyRun) assertFinalTurnHeld(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(run.roomCtx, publicRoomLatencyTestTimeout)
	defer cancel()
	held := make(chan error, 1)
	go func() { held <- run.clock.WaitForTimers(ctx, publicRoomLatencyIdleTimers+1) }()
	select {
	case <-run.runDone:
		t.Fatal("room stopped before the final response reached the peer")
	case err := <-held:
		if err != nil {
			t.Fatalf("room did not hold the max_turns stop for final audio: %v", err)
		}
	}
	select {
	case fanout := <-run.fanouts:
		t.Fatalf("final fanout %+v reached the peer before the room clock advanced", fanout)
	default:
	}
}

var _ session.LiveService = (*publicRoomLatencyLiveService)(nil)
var _ session.LiveHandle = (*publicRoomLatencyLiveHandle)(nil)

// waitAdmitted blocks until the room's source worker has fanned every pushed
// frame out to the peer mixer inputs and come back for the next frame.
func (i *publicRoomLatencyInbound) waitAdmitted(ctx context.Context) error {
	for {
		i.admission.Lock()
		if i.reads > i.pushed {
			i.admission.Unlock()
			return nil
		}
		if i.readEntered == nil {
			i.readEntered = make(chan struct{})
		}
		entered := i.readEntered
		i.admission.Unlock()
		select {
		case <-entered:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func publicRoomLatencyPCMBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func publicRoomLatencySamplesSilent(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}
