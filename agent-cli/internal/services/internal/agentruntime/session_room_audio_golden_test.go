package agentruntime

import (
	"bytes"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestCleanTurnTakingRoomReplayFixturePassesAudioProperties(t *testing.T) {
	bundle := loadCleanTurnTakingBundle(t)
	if len(bundle.Participants) != 2 {
		t.Fatalf("participants = %d, want two clean-turn participants", len(bundle.Participants))
	}
	for _, participant := range bundle.Participants {
		planParticipant, ok := bundle.Plan.Participant(participant.ID)
		if !ok {
			t.Fatalf("participant %q is missing from normalized replay plan", participant.ID)
		}
		if planParticipant.RecordedTurnCount < 2 {
			t.Fatalf("participant %q has %d recorded turns, want at least two", participant.ID, planParticipant.RecordedTurnCount)
		}
		if len(participant.Events) < 4 || len(participant.Diagnostics) < 2 {
			t.Fatalf("participant %q sidecars = events:%d diagnostics:%d, want timestamped turns", participant.ID, len(participant.Events), len(participant.Diagnostics))
		}
		for _, stream := range []RoomReplayAudioStream{participant.WAV, participant.Sent, participant.Received} {
			if stream.SampleCount == 0 || len(stream.PCM) == 0 {
				t.Fatalf("participant %q stream %q is empty", participant.ID, stream.StreamID)
			}
			reconstructed := make([]byte, 0, len(stream.PCM))
			for _, delta := range stream.Deltas {
				reconstructed = append(reconstructed, delta.PCM...)
			}
			if stream.Role == "wav" && (!bytes.Equal(reconstructed, stream.PCM) || stream.SampleCount != len(reconstructed)/2) {
				t.Fatalf("participant %q output deltas do not reproduce %d PCM bytes/%d samples", participant.ID, len(stream.PCM), stream.SampleCount)
			}
		}
	}
	if bundle.RoomMix.SampleCount == 0 || !hasGoldenNonZeroSamples(bundle.RoomMix.Samples) {
		t.Fatalf("room mix is empty or silent: samples=%d", bundle.RoomMix.SampleCount)
	}
	if len(bundle.Plan.Timeline) != 8 {
		t.Fatalf("room timeline events = %d, want eight turn boundaries", len(bundle.Plan.Timeline))
	}

	analysis, err := audio.AnalyzePCM16Room(bundle.AnalysisInput(), bundle.AnalysisConfig())
	if err != nil {
		t.Fatalf("analyze clean turn-taking bundle: %v", err)
	}
	if !analysis.Passed() {
		t.Fatalf("clean turn-taking bundle failed: %v", analysis.Failures)
	}
	if len(analysis.Streams) != 7 || len(analysis.Loudness) != 1 || !analysis.Loudness[0].Passed {
		t.Fatalf("analysis streams/loudness = %d/%+v, want seven streams and passing balance", len(analysis.Streams), analysis.Loudness)
	}
	for _, stream := range analysis.Streams {
		if stream.SampleCount == 0 || stream.RMS <= 0 || stream.ClipCount != 0 || len(stream.BoundaryClicks) != 0 {
			t.Fatalf("stream %q clean facts = samples:%d rms:%v clips:%d clicks:%d", stream.StreamID, stream.SampleCount, stream.RMS, stream.ClipCount, len(stream.BoundaryClicks))
		}
		if stream.Edges.FirstAbsValue > 1000 || stream.Edges.LastAbsValue > 1000 || stream.Edges.FinalRMSDBFS > -40 {
			t.Fatalf("stream %q edges = %+v, want clean first/last samples and final frame", stream.StreamID, stream.Edges)
		}
	}
}

func TestCleanTurnTakingRoomReplayFixtureDefectsFail(t *testing.T) {
	tests := []struct {
		name     string
		property string
		streamID string
		mutate   func(*audio.PCM16RoomInput)
	}{
		{
			name:     "clipping",
			property: "clipping",
			streamID: "agent-a:output",
			mutate: func(input *audio.PCM16RoomInput) {
				cleanStream(input, "agent-a:output").Samples[400] = 32700
			},
		},
		{
			name:     "quiet boundary click",
			property: "quiet-boundary-click",
			streamID: "agent-a:output",
			mutate: func(input *audio.PCM16RoomInput) {
				stream := cleanStream(input, "agent-a:output")
				stream.ChunkBoundaries = []audio.ChunkBoundary{{ID: "mutated-quiet-chunk", SampleIndex: 1400}}
				stream.Samples[1399] = 0
				stream.Samples[1400] = 7000
			},
		},
		{
			name:     "dropout",
			property: "dropout",
			streamID: "agent-a:output",
			mutate: func(input *audio.PCM16RoomInput) {
				stream := cleanStream(input, "agent-a:output")
				for index := 200; index < 1000; index++ {
					stream.Samples[index] = 0
				}
			},
		},
		{
			name:     "bad trailing edge",
			property: "trailing-click",
			streamID: "agent-a:output",
			mutate: func(input *audio.PCM16RoomInput) {
				stream := cleanStream(input, "agent-a:output")
				stream.Samples[len(stream.Samples)-1] = 2000
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := loadCleanTurnTakingBundle(t)
			input := bundle.AnalysisInput()
			test.mutate(&input)
			analysis, err := audio.AnalyzePCM16Room(input, bundle.AnalysisConfig())
			if err != nil {
				t.Fatalf("analyze mutated bundle: %v", err)
			}
			failure, ok := firstFailure(analysis.Failures, test.property, test.streamID)
			if !ok {
				t.Fatalf("mutated bundle failures = %v, want %q for %q", analysis.Failures, test.property, test.streamID)
			}
			message := failure.Error()
			for _, want := range []string{"stream=", "participant=", "interval=", "timestamp=", "measured=", "bound="} {
				if !strings.Contains(message, want) {
					t.Errorf("failure %q missing %q", message, want)
				}
			}
			if analysis.Passed() {
				t.Fatalf("mutation %q unexpectedly passed", test.name)
			}
		})
	}
}

func TestCleanTurnTakingRoomReplaySelfCopyControlFails(t *testing.T) {
	bundle := loadCleanTurnTakingBundle(t)
	input := bundle.AnalysisInput()
	sent := cleanStream(&input, "agent-a:sent")
	received := cleanStream(&input, "agent-a:received")
	interval := audio.PCM16TimeInterval{ID: "agent-a-turn-1", Start: 200 * time.Millisecond, End: time.Second}
	if err := assertCleanSelfHearing(*sent, *received, interval, bundle.AnalysisConfig()); err != nil {
		t.Fatalf("clean received stream self-hearing check: %v", err)
	}
	copy(received.Samples, sent.Samples)
	err := assertCleanSelfHearing(*sent, *received, interval, bundle.AnalysisConfig())
	if err == nil {
		t.Fatal("self-copy mutation unexpectedly passed")
	}
	for _, want := range []string{"self-hearing", `stream="agent-a:received"`, `participant="agent-a"`, `source="agent-a:sent"`, `received="agent-a:received"`, `interval="agent-a-turn-1"`, "timestamp=", "measured=", "bound="} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("self-copy diagnostic %q missing %q", err, want)
		}
	}
}

func loadCleanTurnTakingBundle(t *testing.T) RoomReplayAudioBundle {
	t.Helper()
	bundle, err := LoadRoomReplayAudioBundle(cleanTurnTakingFixturePath())
	if err != nil {
		t.Fatalf("load clean turn-taking bundle: %v", err)
	}
	return bundle
}

func cleanTurnTakingFixturePath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata", "room-audio", "clean-turn-taking")
}

func cleanStream(input *audio.PCM16RoomInput, streamID string) *audio.PCM16TimedStream {
	for index := range input.Streams {
		if input.Streams[index].StreamID == streamID {
			return &input.Streams[index]
		}
	}
	panic(fmt.Sprintf("stream %q is not in clean fixture", streamID))
}

func firstFailure(failures []audio.PropertyFailure, property, streamID string) (audio.PropertyFailure, bool) {
	for _, failure := range failures {
		if failure.Property == property && failure.StreamID == streamID {
			return failure, true
		}
	}
	return audio.PropertyFailure{}, false
}

func hasGoldenNonZeroSamples(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

func assertCleanSelfHearing(source, received audio.PCM16TimedStream, interval audio.PCM16TimeInterval, config audio.PCM16RoomAnalysisConfig) error {
	measurement, err := audio.NormalizedPCM16CrossCorrelation(source, received, interval, config.CorrelationLagWindow, config.CorrelationSilenceFloorDBFS)
	if err != nil {
		return err
	}
	if measurement.BestAbsoluteCorrelation < config.MaxSelfCorrelation {
		return nil
	}
	return audio.PropertyFailure{
		Property:         "self-hearing",
		StreamID:         measurement.ReceivedStreamID,
		ParticipantID:    measurement.ReceivedParticipantID,
		SourceStreamID:   measurement.SourceStreamID,
		ReceivedStreamID: measurement.ReceivedStreamID,
		Direction:        source.ParticipantID + "->" + received.ParticipantID,
		Interval:         measurement.IntervalID,
		Timestamp:        measurement.Start,
		Lag:              measurement.BestAbsoluteLag,
		Measured:         measurement.BestAbsoluteCorrelation,
		Comparison:       ">=",
		Bound:            config.MaxSelfCorrelation,
		Unit:             "absolute normalized correlation",
		Detail:           fmt.Sprintf("signed-best-lag=%s absolute-best-lag=%s compared-samples=%d", measurement.BestLag, measurement.BestAbsoluteLag, measurement.ComparedSamples),
	}
}
