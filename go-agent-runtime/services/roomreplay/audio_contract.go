package roomreplay

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

const (
	ErrRoomReplayDeltaReconstruction roomReplaySentinel = "room replay delta reconstruction failed"
	ErrRoomReplayAudioTimeline       roomReplaySentinel = "room replay audio timeline is inconsistent"
	ErrRoomReplayToleranceProfile    roomReplaySentinel = "invalid room replay tolerance profile"
)

// RoomReplayToleranceProfile contains the fully expanded immutable analysis
// tolerances attached to a replay bundle. A supplied profile may only tighten
// the suite defaults.
type RoomReplayToleranceProfile struct {
	Name         string
	StreamConfig roomanalysis.PCM16AnalysisConfig
	RoomConfig   roomanalysis.PCM16RoomAnalysisConfig
}

// RoomReplayAudioDelta is one decoded audio delta in recorded JSONL order.
// PCM is copied from the raw little-endian PCM16 payload.
type RoomReplayAudioDelta struct {
	ID          string
	Sequence    int64
	HasSequence bool
	Offset      time.Duration
	HasOffset   bool
	TurnID      string
	LineNumber  int
	PCM         []byte
}

// RoomReplayAudioStream is an identity- and time-aware mono PCM16 stream.
// Its sample and metadata slices are fresh values owned by the caller.
type RoomReplayAudioStream struct {
	roomanalysis.PCM16TimedStream
	Role          string
	PCM           []byte
	SampleCount   int
	Artifact      RoomReplayArtifact
	DeltaArtifact RoomReplayArtifact
	Deltas        []RoomReplayAudioDelta
}

// RoomReplayAudioParticipant groups independent output, sent, and received
// streams for one stable participant identity.
type RoomReplayAudioParticipant struct {
	ID          string
	WAV         RoomReplayAudioStream
	Sent        RoomReplayAudioStream
	Received    RoomReplayAudioStream
	Events      []json.RawMessage
	Diagnostics []json.RawMessage
}

// RoomReplayAudioAnnotation preserves the annotation identity and interval
// from the admitted bundle alongside its analysis-ready typed projections.
type RoomReplayAudioAnnotation struct {
	ID                       string
	Kind                     string
	Start                    time.Duration
	End                      time.Duration
	Participants             []string
	SourceParticipantID      string
	TargetParticipantID      string
	InterrupterParticipantID string
	InterruptedParticipantID string
	Raw                      json.RawMessage
}

// RoomReplayAudioBundle is the validated audio projection of a replay plan.
// All file, hash, format, identity, timing, sidecar, tolerance, and delta
// reconstruction decisions are made by Service.LoadAudioBundle.
type RoomReplayAudioBundle struct {
	Plan         RoomReplayPlan
	Format       RoomReplayPCMFormat
	Tolerances   RoomReplayToleranceProfile
	Participants []RoomReplayAudioParticipant
	RoomMix      RoomReplayAudioStream
	Annotations  []RoomReplayAudioAnnotation
	Overlaps     []roomanalysis.PCM16OverlapInterval
	BargeIns     []roomanalysis.PCM16BargeInAnnotation
	Loudness     []roomanalysis.PCM16LoudnessInterval
}

// Participant returns a participant's resolved audio evidence by stable ID.
func (b RoomReplayAudioBundle) Participant(id string) (RoomReplayAudioParticipant, bool) {
	for _, participant := range b.Participants {
		if participant.ID == id {
			return participant, true
		}
	}
	return RoomReplayAudioParticipant{}, false
}

// AnalysisInput converts resolved streams and annotations into the
// side-effect-free audio analyzer input.
func (b RoomReplayAudioBundle) AnalysisInput() roomanalysis.PCM16RoomInput {
	input := roomanalysis.PCM16RoomInput{
		Overlaps: append([]roomanalysis.PCM16OverlapInterval(nil), b.Overlaps...),
		BargeIns: append([]roomanalysis.PCM16BargeInAnnotation(nil), b.BargeIns...),
		Loudness: append([]roomanalysis.PCM16LoudnessInterval(nil), b.Loudness...),
	}
	for _, participant := range b.Participants {
		input.Streams = append(input.Streams,
			cloneRoomReplayTimedStream(participant.WAV.PCM16TimedStream),
			cloneRoomReplayTimedStream(participant.Sent.PCM16TimedStream),
			cloneRoomReplayTimedStream(participant.Received.PCM16TimedStream),
		)
	}
	if b.RoomMix.StreamID != "" {
		input.Streams = append(input.Streams, cloneRoomReplayTimedStream(b.RoomMix.PCM16TimedStream))
	}
	return input
}

// AnalysisConfig returns the fully expanded room profile for audio analysis.
func (b RoomReplayAudioBundle) AnalysisConfig() roomanalysis.PCM16RoomAnalysisConfig {
	return b.Tolerances.RoomConfig
}

func cloneRoomReplayTimedStream(stream roomanalysis.PCM16TimedStream) roomanalysis.PCM16TimedStream {
	stream.Samples = append([]int16(nil), stream.Samples...)
	stream.ExpectedSpeech = append(stream.ExpectedSpeech[:0:0], stream.ExpectedSpeech...)
	stream.ChunkBoundaries = append(stream.ChunkBoundaries[:0:0], stream.ChunkBoundaries...)
	return stream
}

// RoomReplayDeltaReconstructionError identifies the first divergent byte
// between recorded deltas and their corresponding WAV payload.
type RoomReplayDeltaReconstructionError struct {
	ParticipantID       string
	StreamID            string
	DeltaID             string
	DeltaIndex          int
	ByteOffset          int
	ExpectedByte        int
	ActualByte          int
	ExpectedLength      int
	ActualLength        int
	ExpectedSampleCount int
	ActualSampleCount   int
	Cause               error
}

func (e *RoomReplayDeltaReconstructionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	expectedByte := "<missing>"
	if e.ExpectedByte >= 0 {
		expectedByte = fmt.Sprintf("0x%02x", e.ExpectedByte)
	}
	actualByte := "<missing>"
	if e.ActualByte >= 0 {
		actualByte = fmt.Sprintf("0x%02x", e.ActualByte)
	}
	return fmt.Sprintf("%s: participant %q stream %q delta %q (index %d) first divergent byte %d: expected %s, actual %s; expected %d bytes/%d samples, reconstructed %d bytes/%d samples", ErrRoomReplayDeltaReconstruction, e.ParticipantID, e.StreamID, e.DeltaID, e.DeltaIndex, e.ByteOffset, expectedByte, actualByte, e.ExpectedLength, e.ExpectedSampleCount, e.ActualLength, e.ActualSampleCount)
}

func (e *RoomReplayDeltaReconstructionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(ErrRoomReplayDeltaReconstruction, ErrInvalidRoomReplayBundle, e.Cause)
}
