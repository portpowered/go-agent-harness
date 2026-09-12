package roomaudio

import (
	"errors"
	"testing"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestRoomReplayErrorsRetainStableClassifications(t *testing.T) {
	mismatch := &RoomReplayBundleError{Kind: RoomReplayBundleMismatch, Field: "pcm", Expected: "16", Actual: "8", Err: gateway.ErrReplayMismatch}
	if !errors.Is(mismatch, ErrInvalidRoomReplayBundle) || !errors.Is(mismatch, gateway.ErrReplayMismatch) || errors.Is(mismatch, gateway.ErrReplayIncomplete) {
		t.Fatalf("mismatch classification failed: %v", mismatch)
	}
	incomplete := &RoomReplayBundleError{Kind: RoomReplayBundleIncomplete, Err: providers.ErrReplayIncomplete}
	if !errors.Is(incomplete, ErrRoomReplayBundleIncomplete) || !errors.Is(incomplete, providers.ErrReplayIncomplete) || !errors.Is(incomplete, gateway.ErrReplayIncomplete) {
		t.Fatalf("incomplete classification failed: %v", incomplete)
	}
	if (&RoomReplayBundleError{}).Error() == "" || (*RoomReplayBundleError)(nil).Error() != "<nil>" {
		t.Fatal("bundle error formatting lost its nil/empty contract")
	}
	detail := &RoomReplayDeltaReconstructionError{ParticipantID: "alpha", StreamID: "alpha:output", DeltaID: "d0", DeltaIndex: 0, ByteOffset: 2, ExpectedByte: -1, ActualByte: 1, ExpectedLength: 4, ActualLength: 2, ExpectedSampleCount: 2, ActualSampleCount: 1, Cause: ErrRoomReplayDeltaReconstruction}
	if !errors.Is(detail, ErrRoomReplayDeltaReconstruction) || !errors.Is(detail, ErrInvalidRoomReplayBundle) || detail.Error() == "" {
		t.Fatalf("delta classification failed: %v", detail)
	}
}

func TestRoomReplayBundleDetachesAudioAndAnalysisInputs(t *testing.T) {
	stream := RoomReplayAudioStream{
		PCM16TimedStream: roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{StreamID: "alpha:output", ParticipantID: "alpha", SampleRate: 24000, Samples: []int16{1, 2}, ExpectedSpeech: []streamanalysis.SpeechAnnotation{{Label: "speech", Start: time.Millisecond, End: 2 * time.Millisecond}}}},
		Role:             "wav", PCM: []byte{1, 2}, Deltas: []RoomReplayAudioDelta{{ID: "d0", PCM: []byte{1, 2}}},
	}
	bundle := RoomReplayAudioBundle{
		Participants: []RoomReplayAudioParticipant{{ID: "alpha", WAV: stream, Sent: stream, Received: stream}},
		RoomMix:      RoomReplayAudioStream{PCM16TimedStream: roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{StreamID: "room:mix", Samples: []int16{3}}}},
		Overlaps:     []roomanalysis.PCM16OverlapInterval{{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: "overlap", Start: 0, End: time.Second}}},
	}
	participant, ok := bundle.Participant("alpha")
	if !ok {
		t.Fatal("participant not found")
	}
	participant.WAV.PCM[0] = 9
	participant.WAV.Samples[0] = 9
	participant.WAV.Deltas[0].PCM[0] = 9
	if bundle.Participants[0].WAV.PCM[0] != 1 || bundle.Participants[0].WAV.Samples[0] != 1 || bundle.Participants[0].WAV.Deltas[0].PCM[0] != 1 {
		t.Fatal("Participant returned shared mutable buffers")
	}
	input := bundle.AnalysisInput()
	input.Streams[0].Samples[0] = 7
	if bundle.Participants[0].WAV.Samples[0] != 1 || len(input.Streams) != 4 || len(input.Overlaps) != 1 {
		t.Fatal("AnalysisInput did not detach room streams")
	}
	if _, ok := bundle.Participant("missing"); ok {
		t.Fatal("unknown participant was returned")
	}
}

func TestRoomReplayContractDefaults(t *testing.T) {
	defaults := roomanalysis.DefaultPCM16AnalysisConfig()
	if defaults.FrameDuration <= 0 {
		t.Fatalf("default stream profile = %+v", defaults)
	}
}
