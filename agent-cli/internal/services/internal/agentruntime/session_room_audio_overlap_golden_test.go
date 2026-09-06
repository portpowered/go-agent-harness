package agentruntime

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	overlapParticipantA = "agent-a"
	overlapParticipantB = "agent-b"
)

func TestDeliberateOverlapRoomReplayFixturePassesPeerDeliveryAndSelfHearing(t *testing.T) {
	bundle := loadDeliberateOverlapBundle(t)
	if len(bundle.Participants) != 2 {
		t.Fatalf("participants = %d, want two deliberate-overlap participants", len(bundle.Participants))
	}
	if len(bundle.Overlaps) != 1 {
		t.Fatalf("overlap annotations = %d, want one simultaneous-speech interval", len(bundle.Overlaps))
	}
	overlap := bundle.Overlaps[0]
	if duration := overlap.End - overlap.Start; duration < 5*time.Second {
		t.Fatalf("overlap duration = %s, want at least 5s", duration)
	}
	if overlap.A.SentStreamID == overlap.B.SentStreamID || overlap.A.ReceivedStreamID == overlap.B.ReceivedStreamID {
		t.Fatalf("overlap stream identities are not independent: %+v", overlap)
	}
	manifestData, err := os.ReadFile(filepath.Join(deliberateOverlapFixturePath(), "run-manifest.json"))
	if err != nil {
		t.Fatalf("read deliberate-overlap manifest: %v", err)
	}
	for _, want := range []string{
		"s2s-room-participants-deaf-while-speaking@1ed45336",
		"s2s-room-recording-completeness@649f0cd6",
		"--shape deliberate-overlap --output agent-cli/internal/services/testdata/room-audio/deliberate-overlap",
	} {
		if !bytes.Contains(manifestData, []byte(want)) {
			t.Errorf("manifest is missing provenance/regeneration entry %q", want)
		}
	}

	for _, participant := range bundle.Participants {
		planParticipant, ok := bundle.Plan.Participant(participant.ID)
		if !ok {
			t.Fatalf("participant %q is missing from normalized replay plan", participant.ID)
		}
		if planParticipant.Provider == "" || planParticipant.CapturePath == "" {
			t.Fatalf("participant %q is not provider-bound: provider=%q capture=%q", participant.ID, planParticipant.Provider, planParticipant.CapturePath)
		}
		wantReceivedPath := filepath.ToSlash(filepath.Join("participants", participant.ID, "received.pcm"))
		if participant.Received.Artifact.Path != wantReceivedPath || participant.Received.Role != "received" {
			t.Fatalf("participant %q received artifact = %+v, want provider-bound %q", participant.ID, participant.Received.Artifact, wantReceivedPath)
		}
		if participant.Received.SampleCount != bundle.RoomMix.SampleCount || len(participant.Received.PCM) == 0 {
			t.Fatalf("participant %q received stream samples=%d bytes=%d, want aligned non-empty room stream of %d samples", participant.ID, participant.Received.SampleCount, len(participant.Received.PCM), bundle.RoomMix.SampleCount)
		}
		if len(participant.WAV.Deltas) == 0 {
			t.Fatalf("participant %q has no output deltas", participant.ID)
		}
		reconstructed := make([]byte, 0, len(participant.WAV.PCM))
		for _, delta := range participant.WAV.Deltas {
			reconstructed = append(reconstructed, delta.PCM...)
		}
		if !bytes.Equal(reconstructed, participant.WAV.PCM) || len(reconstructed)/2 != participant.WAV.SampleCount {
			t.Fatalf("participant %q output deltas do not reproduce %d PCM bytes/%d samples", participant.ID, len(participant.WAV.PCM), participant.WAV.SampleCount)
		}
		if participant.Sent.StreamID == participant.Received.StreamID || bytes.Equal(participant.Sent.PCM, participant.Received.PCM) {
			t.Fatalf("participant %q sent and received evidence was not independently delivered", participant.ID)
		}
	}
	if bundle.RoomMix.SampleCount != 8000 || bundle.RoomMix.TimelineEnd != 8*time.Second {
		t.Fatalf("room mix = samples:%d timeline:%s, want 8000 samples and 8s", bundle.RoomMix.SampleCount, bundle.RoomMix.TimelineEnd)
	}
	if got := bundle.Plan.EndedAt.Sub(bundle.Plan.ClockBase); got != 8*time.Second {
		t.Fatalf("room timeline duration = %s, want 8s", got)
	}
	for _, participantID := range []string{overlapParticipantA, overlapParticipantB} {
		foundStart, foundEnd := false, false
		for _, event := range bundle.Plan.Timeline {
			if event.ParticipantID != participantID {
				continue
			}
			if event.OffsetMS == 1000 {
				foundStart = true
			}
			if event.OffsetMS == 6500 {
				foundEnd = true
			}
		}
		if !foundStart || !foundEnd {
			t.Fatalf("room timeline has overlap boundaries for %s: start=%t end=%t", participantID, foundStart, foundEnd)
		}
	}

	analysis, err := audio.AnalyzePCM16Room(bundle.AnalysisInput(), bundle.AnalysisConfig())
	if err != nil {
		t.Fatalf("analyze deliberate overlap bundle: %v", err)
	}
	if !analysis.Passed() {
		t.Fatalf("deliberate overlap bundle failed: %v", analysis.Failures)
	}
	if len(analysis.Overlaps) != 1 || len(analysis.PeerDeliveries) != 2 || len(analysis.SelfHearings) != 2 {
		t.Fatalf("relationship measurements = overlaps:%d peer:%d self:%d, want one/two/two", len(analysis.Overlaps), len(analysis.PeerDeliveries), len(analysis.SelfHearings))
	}
	for _, delivery := range analysis.PeerDeliveries {
		if !delivery.Passed || delivery.BestCorrelation < 0.55 || delivery.BestLag != 20*time.Millisecond {
			t.Errorf("peer delivery = %+v, want >=0.55 at 20ms", delivery)
		}
	}
	for _, self := range analysis.SelfHearings {
		if !self.Passed || self.BestAbsoluteCorrelation >= 0.30 {
			t.Errorf("self-hearing = %+v, want <0.30", self)
		}
	}
	for _, loudness := range analysis.Loudness {
		if !loudness.Passed || loudness.DifferenceDB > 6 {
			t.Errorf("loudness = %+v, want <=6dB", loudness)
		}
	}
	for _, stream := range analysis.Streams {
		if stream.ClipCount != 0 || len(stream.Dropouts) != 0 || len(stream.BoundaryClicks) != 0 || stream.Edges.FirstAbsValue > 1000 || stream.Edges.LastAbsValue > 1000 || stream.Edges.FinalRMSDBFS > -40 {
			t.Errorf("stream %q clean properties = clips:%d dropouts:%d clicks:%d edges:%+v", stream.StreamID, stream.ClipCount, len(stream.Dropouts), len(stream.BoundaryClicks), stream.Edges)
		}
	}
	for _, streamID := range []string{overlapParticipantA + ":output", overlapParticipantB + ":output"} {
		stream := overlapAnalysisStream(analysis.Streams, streamID)
		if len(stream.ImpulseCandidates) == 0 {
			t.Errorf("stream %q has no loud-window impulse candidate", streamID)
		}
	}
}

func TestDeliberateOverlapRoomReplayFixtureDefectsFailWithDirectionAndNumbers(t *testing.T) {
	tests := []struct {
		name      string
		property  string
		streamID  string
		direction string
		mutate    func(*audio.PCM16RoomInput)
	}{
		{
			name:      "missing forward delivery",
			property:  "overlap-delivery",
			streamID:  overlapParticipantB + ":received",
			direction: overlapParticipantA + "->" + overlapParticipantB,
			mutate: func(input *audio.PCM16RoomInput) {
				stream := deliberateOverlapStream(input, overlapParticipantB+":received")
				stream.Samples = make([]int16, len(stream.Samples))
			},
		},
		{
			name:      "self audio substituted for peer",
			property:  "self-hearing",
			streamID:  overlapParticipantB + ":received",
			direction: overlapParticipantB + "->" + overlapParticipantB,
			mutate: func(input *audio.PCM16RoomInput) {
				received := deliberateOverlapStream(input, overlapParticipantB+":received")
				sent := deliberateOverlapStream(input, overlapParticipantB+":sent")
				received.Samples = append([]int16(nil), sent.Samples...)
			},
		},
		{
			name:      "peer shifted beyond routing window",
			property:  "overlap-delivery",
			streamID:  overlapParticipantB + ":received",
			direction: overlapParticipantA + "->" + overlapParticipantB,
			mutate: func(input *audio.PCM16RoomInput) {
				received := deliberateOverlapStream(input, overlapParticipantB+":received")
				sent := deliberateOverlapStream(input, overlapParticipantA+":sent")
				received.Samples = delayPCM16Samples(sent.Samples, 300)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := loadDeliberateOverlapBundle(t)
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
			if failure.Direction != test.direction {
				t.Errorf("failure direction = %q, want %q", failure.Direction, test.direction)
			}
			message := failure.Error()
			for _, want := range []string{"direction=", "interval=", "timestamp=", "measured=", "bound="} {
				if !strings.Contains(message, want) {
					t.Errorf("failure %q missing %q", message, want)
				}
			}
			if !strings.Contains(failure.Detail, "best-lag=") && !strings.Contains(failure.Detail, "absolute-best-lag=") {
				t.Errorf("failure detail %q has no numeric lag evidence", failure.Detail)
			}
			if analysis.Passed() {
				t.Fatalf("mutation %q unexpectedly passed", test.name)
			}
		})
	}
}

func loadDeliberateOverlapBundle(t *testing.T) RoomReplayAudioBundle {
	t.Helper()
	bundle, err := LoadRoomReplayAudioBundle(deliberateOverlapFixturePath())
	if err != nil {
		t.Fatalf("load deliberate-overlap bundle: %v", err)
	}
	return bundle
}

func deliberateOverlapFixturePath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata", "room-audio", "deliberate-overlap")
}

func deliberateOverlapStream(input *audio.PCM16RoomInput, streamID string) *audio.PCM16TimedStream {
	for index := range input.Streams {
		if input.Streams[index].StreamID == streamID {
			return &input.Streams[index]
		}
	}
	panic(fmt.Sprintf("stream %q is not in deliberate-overlap fixture", streamID))
}

func overlapAnalysisStream(streams []audio.PCM16Analysis, streamID string) audio.PCM16Analysis {
	for _, stream := range streams {
		if stream.StreamID == streamID {
			return stream
		}
	}
	panic(fmt.Sprintf("stream %q has no analysis", streamID))
}

func delayPCM16Samples(source []int16, delay int) []int16 {
	result := make([]int16, len(source))
	if delay < 0 || delay >= len(source) {
		return result
	}
	copy(result[delay:], source[:len(source)-delay])
	return result
}
