package services

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

func TestAnalyzeRoomLatencyBundleDerivesBucketsAndSummary(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	format := room.PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond}
	recorder := newRoomLatencyRecorder(clock, format)
	recorder.observeSpeakerAudio("a", []string{"b"}, make([]byte, 20)) // 10 ms of PCM.
	clock.AdvanceTo(100)
	recorder.observeSpeechStopped("b")
	clock.AdvanceTo(125)
	recorder.observeRuntime("b", SessionRuntimeObservation{Kind: SessionRuntimeObservationInputCommit})
	clock.AdvanceTo(130)
	recorder.observeRuntime("b", SessionRuntimeObservation{
		Kind:       SessionRuntimeObservationResponseCreate,
		ResponseID: "response-1",
	})
	clock.AdvanceTo(730)
	recorder.observeProviderAudio("b", "response-1")
	clock.AdvanceTo(750)
	recorder.observePeerAudio("b", "a", []byte{1, 2})

	report, err := AnalyzeRoomLatencyBundle(recorder.bundle())
	if err != nil {
		t.Fatalf("AnalyzeRoomLatencyBundle: %v", err)
	}
	if report.EligibleCount != 1 || report.ExcludedCount != 0 {
		t.Fatalf("counts = eligible %d excluded %d, want 1/0", report.EligibleCount, report.ExcludedCount)
	}
	transition := report.Transitions[0]
	if !transition.Eligible {
		t.Fatalf("transition excluded: %+v", transition)
	}
	values := map[string]int64{
		"detection":             transition.DetectionMS,
		"commit_after_end":      transition.CommitAfterEndMS,
		"response_after_commit": transition.ResponseAfterCommitMS,
		"dispatch":              transition.DispatchMS,
		"provider":              transition.ProviderMS,
		"local_output":          transition.LocalOutputMS,
		"harness_owned":         transition.HarnessOwnedMS,
		"four_bucket_sum":       transition.FourBucketSumMS,
		"direct_gap":            transition.DirectGapMS,
		"total":                 transition.TotalMS,
	}
	want := map[string]int64{
		"detection":             90,
		"commit_after_end":      25,
		"response_after_commit": 5,
		"dispatch":              30,
		"provider":              600,
		"local_output":          20,
		"harness_owned":         140,
		"four_bucket_sum":       740,
		"direct_gap":            740,
		"total":                 740,
	}
	for name, wantValue := range want {
		if got := values[name]; got != wantValue {
			t.Errorf("%s = %d ms, want %d ms", name, got, wantValue)
		}
	}
	if report.Summary.Provider.SampleCount != 1 || report.Summary.Provider.MedianMS != 600 || report.Summary.Provider.P95MS != 600 || report.Summary.Provider.MaxMS != 600 {
		t.Fatalf("provider summary = %+v, want one 600 ms sample", report.Summary.Provider)
	}
	if report.Summary.HarnessOwned.MedianMS != 140 {
		t.Fatalf("harness summary = %+v, want 140 ms", report.Summary.HarnessOwned)
	}
	if transition.LastSpeakerSample == nil || !transition.LastSpeakerSample.Timestamp.Equal(time.Unix(1700000000, 0).UTC().Add(10*time.Millisecond)) {
		t.Fatalf("last speaker landmark = %+v, want final sample at base+10ms", transition.LastSpeakerSample)
	}
}

func TestAnalyzeRoomLatencyBundleAllowsEqualTimeCausalOrdering(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	recorder := newRoomLatencyRecorder(clock, room.PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond})
	recorder.observeSpeakerAudio("a", []string{"b"}, make([]byte, 20))
	clock.AdvanceTo(10)
	recorder.observeSpeechStopped("b")
	recorder.observeRuntime("b", SessionRuntimeObservation{Kind: SessionRuntimeObservationInputCommit})
	recorder.observeRuntime("b", SessionRuntimeObservation{Kind: SessionRuntimeObservationResponseCreate, ResponseID: "same-time"})
	recorder.observeProviderAudio("b", "same-time")
	recorder.observePeerAudio("b", "a", []byte{1, 2})

	report, err := AnalyzeRoomLatencyBundle(recorder.bundle())
	if err != nil {
		t.Fatalf("AnalyzeRoomLatencyBundle: %v", err)
	}
	if report.EligibleCount != 1 || report.ExcludedCount != 0 {
		t.Fatalf("counts = eligible %d excluded %d, want 1/0", report.EligibleCount, report.ExcludedCount)
	}
	transition := report.Transitions[0]
	if transition.DetectionMS != 0 || transition.DispatchMS != 0 || transition.ProviderMS != 0 || transition.LocalOutputMS != 0 || transition.TotalMS != 0 {
		t.Fatalf("equal-time durations = %+v, want all zero", transition)
	}
}

func TestAnalyzeRoomLatencyBundleClassifiesIncompleteAndReorderedLandmarks(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	recorder := newRoomLatencyRecorder(clock, room.PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond})
	recorder.observeSpeakerAudio("a", []string{"b"}, make([]byte, 20))
	clock.AdvanceTo(10)
	recorder.observeSpeechStopped("b")
	recorder.observeRuntime("b", SessionRuntimeObservation{Kind: SessionRuntimeObservationInputCommit})
	incomplete, err := AnalyzeRoomLatencyBundle(recorder.bundle())
	if err != nil {
		t.Fatalf("Analyze incomplete bundle: %v", err)
	}
	if incomplete.EligibleCount != 0 || incomplete.ExcludedCount != 1 || incomplete.Transitions[0].ExclusionReason != RoomLatencyReasonMissingResponseCreate {
		t.Fatalf("incomplete report = %+v, want missing response_create", incomplete)
	}

	base := time.Unix(1700000000, 0).UTC()
	events := []RoomLatencyEvent{
		{Sequence: 1, Kind: RoomLatencyEventSpeakerPCM, TransitionID: "reordered", ParticipantID: "a", PeerParticipantID: "b", Timestamp: base, PCMBytes: 20, SampleRateHz: 1000, Channels: 1},
		{Sequence: 2, Kind: RoomLatencyEventEndOfSpeech, TransitionID: "reordered", ParticipantID: "b", PeerParticipantID: "a", Timestamp: base.Add(10 * time.Millisecond)},
		{Sequence: 3, Kind: RoomLatencyEventResponseCreate, TransitionID: "reordered", ParticipantID: "b", ResponseID: "r", Timestamp: base.Add(20 * time.Millisecond)},
		{Sequence: 4, Kind: RoomLatencyEventInputCommit, TransitionID: "reordered", ParticipantID: "b", Timestamp: base.Add(20 * time.Millisecond)},
		{Sequence: 5, Kind: RoomLatencyEventProviderAudio, TransitionID: "reordered", ParticipantID: "b", ResponseID: "r", Timestamp: base.Add(30 * time.Millisecond)},
		{Sequence: 6, Kind: RoomLatencyEventPeerAudio, TransitionID: "reordered", ParticipantID: "b", PeerParticipantID: "a", ResponseID: "r", Timestamp: base.Add(40 * time.Millisecond)},
	}
	reordered, err := AnalyzeRoomLatencyBundle(RoomLatencyBundle{
		SchemaVersion: RoomLatencyBundleSchemaVersion,
		Format:        RoomLatencyPCMFormat{SampleRateHz: 1000, Channels: 1, FrameDurationNS: int64(20 * time.Millisecond)},
		Events:        events,
	})
	if err != nil {
		t.Fatalf("Analyze reordered bundle: %v", err)
	}
	if reordered.EligibleCount != 0 || len(reordered.Exclusions) != 1 || reordered.Transitions[0].ExclusionReason != RoomLatencyReasonReorderedLandmarks {
		t.Fatalf("reordered report = %+v, want reordered_landmarks", reordered)
	}
}

func TestRoomLatencyArtifactRoundTripAndManifestReader(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	recorder := newRoomLatencyRecorder(clock, room.DefaultPCM16Format())
	recorder.observeSpeakerAudio("a", []string{"b"}, make([]byte, 48))
	clock.AdvanceTo(25)
	recorder.observeSpeechStopped("b")
	recorder.observeRuntime("b", SessionRuntimeObservation{Kind: SessionRuntimeObservationInputCommit})
	recorder.observeRuntime("b", SessionRuntimeObservation{Kind: SessionRuntimeObservationResponseCreate, ResponseID: "r"})
	clock.AdvanceTo(625)
	recorder.observeProviderAudio("b", "r")
	recorder.observePeerAudio("b", "a", []byte{1, 2})

	destination := t.TempDir()
	artifactPath := filepath.Join(destination, RoomLatencyArtifactPath)
	if err := recorder.write(artifactPath, nil); err != nil {
		t.Fatalf("write room latency artifact: %v", err)
	}
	bundle, err := ReadRoomLatencyBundle(artifactPath)
	if err != nil {
		t.Fatalf("ReadRoomLatencyBundle: %v", err)
	}
	if len(bundle.Events) != len(recorder.bundle().Events) {
		t.Fatalf("round-trip events = %d, want %d", len(bundle.Events), len(recorder.bundle().Events))
	}
	manifest := roomEvidenceManifest{SchemaVersion: roomEvidenceSchemaVersion, Artifacts: map[string]string{"room.latency": RoomLatencyArtifactPath}}
	if err := writeRoomEvidenceManifestFile(filepath.Join(destination, RoomEvidenceManifestPath), manifest, nil); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	report, err := ReadRoomLatencyReport(destination)
	if err != nil {
		t.Fatalf("ReadRoomLatencyReport: %v", err)
	}
	if report.EligibleCount != 1 || report.Summary.Provider.MedianMS != 600 {
		t.Fatalf("round-trip report = %+v, want one 600 ms eligible transition", report)
	}
}

func TestObservedSessionEmitsAutomaticCommitAndResponseBoundaries(t *testing.T) {
	runtimeObserver := &recordingSessionRuntimeObserver{}
	runtime := newSessionRuntimeObservationRecorder(runtimeObserver, nil)
	wrapped := &observedSession{Session: newRoomTestSession(), runtime: runtime}
	if !wrapped.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("MESSAGE.END was not accepted")
	}
	if !wrapped.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		t.Fatal("RESPONSE.CREATE was not accepted")
	}
	if len(runtimeObserver.observations) != 3 {
		t.Fatalf("runtime observations = %#v, want commit plus two response boundaries", runtimeObserver.observations)
	}
	wantKinds := []SessionRuntimeObservationKind{
		SessionRuntimeObservationInputCommit,
		SessionRuntimeObservationResponseCreate,
		SessionRuntimeObservationResponseCreate,
	}
	for index, want := range wantKinds {
		if got := runtimeObserver.observations[index].Kind; got != want {
			t.Fatalf("observation %d kind = %q, want %q", index, got, want)
		}
	}
}
