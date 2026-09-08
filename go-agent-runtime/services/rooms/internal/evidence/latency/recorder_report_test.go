package latency

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestStereoLatencyEvidenceCountsInterleavedSamplesOnce(t *testing.T) {
	format := rooms.AudioFormat{SampleRate: 1000, Channels: 2, FrameDuration: 20 * time.Millisecond}
	recorder := New(platformclock.NewDeterministic(time.Unix(42, 0), time.Millisecond), format)
	recorder.ObserveSpeakerAudio("speaker", []string{"listener"}, audio.PCMFrame{Samples: make([]int16, 20)})
	events := recorder.Bundle().Events
	if len(events) != 1 || events[0].PCMBytes != 40 {
		t.Fatalf("stereo evidence = %+v, want one 40-byte frame", events)
	}
	duration, err := audio.PCM16Duration(events[0].PCMBytes, events[0].SampleRateHz, events[0].Channels)
	if err != nil || duration != 10*time.Millisecond {
		t.Fatalf("stereo duration = %v, %v; want 10ms", duration, err)
	}
}

func TestLatencyWriteFailurePreservesDestinationAndRemovesTemporary(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "occupied")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	recorder := New(platformclock.NewDeterministic(time.Unix(42, 0), time.Millisecond), rooms.AudioFormat{SampleRate: 24000, Channels: 1})
	if err := recorder.Write(destination); err == nil {
		t.Fatal("rename over a directory succeeded")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "occupied" || !entries[0].IsDir() {
		t.Fatalf("failed publication changed destination or leaked temporary: %+v", entries)
	}
}

func TestRecorderReportDerivesLatencyBucketsAndSummary(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	format := rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond}
	recorder := New(clock, format)
	recorder.ObserveSpeakerAudio("a", []string{"b"}, audio.PCMFrame{Samples: make([]int16, 10)})
	clock.AdvanceTo(100)
	recorder.ObserveSpeechStopped("b")
	clock.AdvanceTo(125)
	recorder.ObserveRuntime("b", Observation{Kind: ObservationInputCommit})
	clock.AdvanceTo(130)
	recorder.ObserveRuntime("b", Observation{Kind: ObservationResponseCreate, ResponseID: "response-1"})
	clock.AdvanceTo(730)
	recorder.ObserveProviderAudio("b", "response-1")
	clock.AdvanceTo(750)
	recorder.ObservePeerAudio("b", "a", audio.PCMFrame{Samples: []int16{1}})

	report, err := Analyze(recorder.Bundle())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.EligibleCount != 1 || report.ExcludedCount != 0 {
		t.Fatalf("counts = eligible %d excluded %d, want 1/0", report.EligibleCount, report.ExcludedCount)
	}
	transition := report.Transitions[0]
	if !transition.Eligible {
		t.Fatalf("transition excluded: %+v", transition)
	}
	want := map[string]int64{
		"detection": 90, "commit_after_end": 25, "response_after_commit": 5,
		"dispatch": 30, "provider": 600, "local_output": 20,
		"harness_owned": 140, "four_bucket_sum": 740, "direct_gap": 740, "total": 740,
	}
	got := map[string]int64{
		"detection": transition.DetectionMS, "commit_after_end": transition.CommitAfterEndMS,
		"response_after_commit": transition.ResponseAfterCommitMS, "dispatch": transition.DispatchMS,
		"provider": transition.ProviderMS, "local_output": transition.LocalOutputMS,
		"harness_owned": transition.HarnessOwnedMS, "four_bucket_sum": transition.FourBucketSumMS,
		"direct_gap": transition.DirectGapMS, "total": transition.TotalMS,
	}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("%s = %d ms, want %d ms", name, got[name], wantValue)
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

func TestRecorderReportAllowsEqualTimeCausalOrdering(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	recorder := New(clock, rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond})
	recorder.ObserveSpeakerAudio("a", []string{"b"}, audio.PCMFrame{Samples: make([]int16, 10)})
	clock.AdvanceTo(10)
	recorder.ObserveSpeechStopped("b")
	recorder.ObserveRuntime("b", Observation{Kind: ObservationInputCommit})
	recorder.ObserveRuntime("b", Observation{Kind: ObservationResponseCreate, ResponseID: "same-time"})
	recorder.ObserveProviderAudio("b", "same-time")
	recorder.ObservePeerAudio("b", "a", audio.PCMFrame{Samples: []int16{1}})

	report, err := Analyze(recorder.Bundle())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.EligibleCount != 1 || report.ExcludedCount != 0 {
		t.Fatalf("counts = eligible %d excluded %d, want 1/0", report.EligibleCount, report.ExcludedCount)
	}
	transition := report.Transitions[0]
	if transition.DetectionMS != 0 || transition.DispatchMS != 0 || transition.ProviderMS != 0 || transition.LocalOutputMS != 0 || transition.TotalMS != 0 {
		t.Fatalf("equal-time durations = %+v, want all zero", transition)
	}
}

func TestReportClassifiesIncompleteAndReorderedLandmarks(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	recorder := New(clock, rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond})
	recorder.ObserveSpeakerAudio("a", []string{"b"}, audio.PCMFrame{Samples: make([]int16, 10)})
	clock.AdvanceTo(10)
	recorder.ObserveSpeechStopped("b")
	recorder.ObserveRuntime("b", Observation{Kind: ObservationInputCommit})
	incomplete, err := Analyze(recorder.Bundle())
	if err != nil {
		t.Fatalf("Analyze incomplete bundle: %v", err)
	}
	if incomplete.EligibleCount != 0 || incomplete.ExcludedCount != 1 || incomplete.Transitions[0].ExclusionReason != RoomLatencyReasonMissingResponseCreate {
		t.Fatalf("incomplete report = %+v, want missing response_create", incomplete)
	}

	base := time.Unix(1700000000, 0).UTC()
	events := []rooms.RoomLatencyEvent{
		{Sequence: 1, Kind: rooms.RoomLatencyEventSpeakerPCM, TransitionID: "reordered", ParticipantID: "a", PeerParticipantID: "b", Timestamp: base, PCMBytes: 20, SampleRateHz: 1000, Channels: 1},
		{Sequence: 2, Kind: rooms.RoomLatencyEventEndOfSpeech, TransitionID: "reordered", ParticipantID: "b", PeerParticipantID: "a", Timestamp: base.Add(10 * time.Millisecond)},
		{Sequence: 3, Kind: rooms.RoomLatencyEventResponseCreate, TransitionID: "reordered", ParticipantID: "b", ResponseID: "r", Timestamp: base.Add(20 * time.Millisecond)},
		{Sequence: 4, Kind: rooms.RoomLatencyEventInputCommit, TransitionID: "reordered", ParticipantID: "b", Timestamp: base.Add(20 * time.Millisecond)},
		{Sequence: 5, Kind: rooms.RoomLatencyEventProviderAudio, TransitionID: "reordered", ParticipantID: "b", ResponseID: "r", Timestamp: base.Add(30 * time.Millisecond)},
		{Sequence: 6, Kind: rooms.RoomLatencyEventPeerAudio, TransitionID: "reordered", ParticipantID: "b", PeerParticipantID: "a", ResponseID: "r", Timestamp: base.Add(40 * time.Millisecond)},
	}
	reordered, err := Analyze(rooms.RoomLatencyBundle{
		SchemaVersion: rooms.RoomLatencyBundleSchemaVersion,
		Format:        rooms.RoomLatencyPCMFormat{SampleRateHz: 1000, Channels: 1, FrameDurationNS: int64(20 * time.Millisecond)},
		Events:        events,
	})
	if err != nil {
		t.Fatalf("Analyze reordered bundle: %v", err)
	}
	if reordered.EligibleCount != 0 || len(reordered.Exclusions) != 1 || reordered.Transitions[0].ExclusionReason != RoomLatencyReasonReorderedLandmarks {
		t.Fatalf("reordered report = %+v, want reordered_landmarks", reordered)
	}
}

func TestRecorderLatencyArtifactRoundTrip(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	recorder := New(clock, rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond})
	recorder.ObserveSpeakerAudio("a", []string{"b"}, audio.PCMFrame{Samples: make([]int16, 24)})
	destination := t.TempDir()
	path := filepath.Join(destination, rooms.RoomLatencyArtifactPath)
	if err := recorder.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}
	bundle, err := ReadBundle(path)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if len(bundle.Events) != len(recorder.Bundle().Events) {
		t.Fatalf("round-trip events = %d, want %d", len(bundle.Events), len(recorder.Bundle().Events))
	}
}
