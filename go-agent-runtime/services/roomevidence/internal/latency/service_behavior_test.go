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

func TestServiceRecordsAndReportsRoomLatency(t *testing.T) {
	base := time.Unix(100, 0).UTC()
	clock := platformclock.NewDeterministic(base, time.Millisecond)
	format := rooms.AudioFormat{SampleRate: 1000, Channels: 1, FrameDuration: 10 * time.Millisecond}
	service := NewService()
	recorder := service.NewRecorder(clock, format)

	recorder.ObserveSpeakerAudio("noise", nil, audio.PCMFrame{Samples: []int16{1, 2}})
	recorder.ObserveSpeakerBytes("peer", []string{"agent", "agent", "peer", ""}, 20)
	recorder.ObserveSpeakerBytes("", []string{"agent"}, 20)
	clock.AdvanceBy(10 * time.Millisecond)
	recorder.ObserveSpeechStopped("agent")
	clock.AdvanceBy(time.Millisecond)
	recorder.ObserveRuntime("agent", rooms.LatencyObservation{Kind: rooms.LatencyObservationInputCommit})
	clock.AdvanceBy(time.Millisecond)
	recorder.ObserveRuntime("agent", rooms.LatencyObservation{Kind: rooms.LatencyObservationResponseCreate, ResponseID: "response-1"})
	recorder.ObserveRuntime("agent", rooms.LatencyObservation{Kind: rooms.LatencyObservationResponseCreate, ResponseID: "response-1"})
	clock.AdvanceBy(time.Millisecond)
	recorder.ObserveProviderAudio("nobody", "ignored")
	recorder.ObserveProviderAudio("agent", "response-1")
	recorder.ObserveProviderAudio("agent", "response-1")
	clock.AdvanceBy(time.Millisecond)
	recorder.ObservePeerAudio("agent", "peer", audio.PCMFrame{Samples: []int16{3, 4}})
	recorder.ObservePeerBytes("agent", "peer", 20)

	bundle := recorder.Bundle()
	if len(bundle.Events) != 7 {
		t.Fatalf("event count = %d, want 7", len(bundle.Events))
	}
	destination := filepath.Join(t.TempDir(), rooms.RoomLatencyArtifactPath)
	if err := recorder.Write(destination); err != nil {
		t.Fatalf("Write: %v", err)
	}
	decoded, err := service.ReadBundle(destination)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if len(decoded.Events) != len(bundle.Events) || decoded.Format != bundle.Format {
		t.Fatalf("decoded bundle = %+v, want %+v", decoded, bundle)
	}
	for _, report := range []rooms.RoomLatencyReport{mustAnalyze(t, service, decoded), mustReport(t, service, filepath.Dir(destination))} {
		if report.EligibleCount != 1 || report.ExcludedCount != 1 || len(report.Transitions) != 1 {
			t.Fatalf("report counts = eligible %d excluded %d transitions %d", report.EligibleCount, report.ExcludedCount, len(report.Transitions))
		}
		transition := report.Transitions[0]
		if !transition.Eligible || transition.ResponseID != "response-1" || transition.TotalMS != 4 {
			t.Fatalf("transition = %+v", transition)
		}
		if report.Summary.Total.SampleCount != 1 || report.Summary.Total.MedianMS != 4 {
			t.Fatalf("summary = %+v", report.Summary.Total)
		}
	}
}

func TestServiceRejectsInvalidLatencyArtifacts(t *testing.T) {
	service := NewService()
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := service.ReadBundle(missing); err == nil {
		t.Fatal("ReadBundle accepted a missing artifact")
	}
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadBundle(invalid); err == nil {
		t.Fatal("ReadBundle accepted malformed JSON")
	}
	if _, err := service.AnalyzeBundle(rooms.RoomLatencyBundle{SchemaVersion: rooms.RoomLatencyBundleSchemaVersion}); err == nil {
		t.Fatal("AnalyzeBundle accepted an invalid PCM format")
	}
	if _, err := service.Report(t.TempDir()); err == nil {
		t.Fatal("Report accepted a directory without a latency artifact")
	}
	var nilRecorder *Recorder
	if err := nilRecorder.Write(filepath.Join(t.TempDir(), "nil.json")); err != nil {
		t.Fatalf("nil recorder Write: %v", err)
	}
}

func mustAnalyze(t *testing.T, service rooms.LatencyService, bundle rooms.RoomLatencyBundle) rooms.RoomLatencyReport {
	t.Helper()
	report, err := service.AnalyzeBundle(bundle)
	if err != nil {
		t.Fatalf("AnalyzeBundle: %v", err)
	}
	return report
}

func mustReport(t *testing.T, service rooms.LatencyService, destination string) rooms.RoomLatencyReport {
	t.Helper()
	report, err := service.Report(destination)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	return report
}
