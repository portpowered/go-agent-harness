// Package roomaudio owns the bounded audio projection of an admitted room
// replay plan. Filesystem decoding, metadata policy and analysis preparation
// stay behind this contract; hosts supply only the already-admitted plan.
package roomaudio

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// The plan and artifact shapes are owned by the room admission service. These
// aliases let an embedder pass an admitted plan without importing a CLI
// package or duplicating the room manifest model.
type (
	RoomReplayPlan          = runtimeRooms.RoomReplayPlan
	RoomReplayPCMFormat     = runtimeRooms.RoomReplayPCMFormat
	RoomReplayArtifact      = runtimeRooms.RoomReplayArtifact
	RoomReplayParticipant   = runtimeRooms.RoomReplayParticipant
	RoomReplayTimelineEvent = runtimeRooms.RoomReplayTimelineEvent
	ParticipantKind         = runtimeRooms.ParticipantKind
)

const (
	RoomReplayBundleSchemaVersion = runtimeRooms.RoomReplayBundleSchemaVersion
	RoomReplayBundleManifestPath  = runtimeRooms.RoomReplayBundleManifestPath

	RoomReplayAudioRoleWAV         = "wav"
	RoomReplayAudioRoleDiagnostics = "diagnostics"
	RoomReplayAudioRoleDeltas      = "deltas"
	RoomReplayAudioRoleSentPCM     = "sent_pcm"
	RoomReplayAudioRoleReceivedPCM = "received_pcm"
	RoomReplayAudioRoleEvents      = "events"
)

type roomReplaySentinel string

func (e roomReplaySentinel) Error() string { return string(e) }

const (
	// ErrInvalidRoomReplayBundle identifies a malformed or integrity-inconsistent
	// audio projection input.
	ErrInvalidRoomReplayBundle roomReplaySentinel = "invalid room replay bundle"
	// ErrRoomReplayBundleIncomplete identifies missing or truncated audio data.
	ErrRoomReplayBundleIncomplete roomReplaySentinel = "room replay bundle incomplete"
	// ErrRoomReplayDeltaReconstruction identifies deltas that do not reproduce
	// the admitted WAV payload.
	ErrRoomReplayDeltaReconstruction roomReplaySentinel = "room replay delta reconstruction failed"
	// ErrRoomReplayAudioTimeline identifies an audio artifact or annotation
	// outside the admitted room timeline.
	ErrRoomReplayAudioTimeline roomReplaySentinel = "room replay audio timeline is inconsistent"
	// ErrRoomReplayToleranceProfile identifies malformed or weakened analysis
	// profile input.
	ErrRoomReplayToleranceProfile roomReplaySentinel = "invalid room replay tolerance profile"
)

// RoomReplayBundleErrorKind is the stable classification of an audio
// admission failure.
type RoomReplayBundleErrorKind string

const (
	RoomReplayBundleMismatch   RoomReplayBundleErrorKind = "mismatch"
	RoomReplayBundleIncomplete RoomReplayBundleErrorKind = "incomplete"
	roomReplayNilString                                  = "<nil>"
)

// RoomReplayBundleError carries bounded, non-secret context for an admission
// failure. Expected and Actual contain paths, sizes, digests, or classifications
// only; payloads and credentials never cross this boundary.
type RoomReplayBundleError struct {
	Kind     RoomReplayBundleErrorKind
	Field    string
	Artifact string
	Expected string
	Actual   string
	Err      error
}

func (e *RoomReplayBundleError) Error() string {
	if e == nil {
		return roomReplayNilString
	}
	label := string(e.Kind)
	if label == "" {
		label = string(RoomReplayBundleMismatch)
	}
	message := "room replay bundle " + label
	if e.Field != "" {
		message += " field " + fmt.Sprintf("%q", e.Field)
	}
	if e.Artifact != "" {
		message += " artifact " + fmt.Sprintf("%q", e.Artifact)
	}
	if e.Expected != "" || e.Actual != "" {
		message += ": expected " + e.Expected + ", actual " + e.Actual
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *RoomReplayBundleError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is preserves the repository's replay classifications for hosts that use the
// shared gateway/provider error vocabulary.
func (e *RoomReplayBundleError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == ErrInvalidRoomReplayBundle {
		return e.Kind == RoomReplayBundleMismatch
	}
	if e.Kind == RoomReplayBundleIncomplete {
		return target == ErrRoomReplayBundleIncomplete || target == gateway.ErrReplayIncomplete || target == providers.ErrReplayIncomplete
	}
	return target == gateway.ErrReplayMismatch || target == providers.ErrReplayMismatch
}

// RoomReplayToleranceProfile is the immutable-by-convention analysis profile
// attached to one replay bundle. A supplied profile may only tighten suite
// defaults.
type RoomReplayToleranceProfile struct {
	Name         string
	StreamConfig roomanalysis.PCM16AnalysisConfig
	RoomConfig   roomanalysis.PCM16RoomAnalysisConfig
}

// RoomReplayAudioDelta is one decoded audio delta in source JSONL order.
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

// RoomReplayDeltaReconstructionError points to the first byte where the
// recorded deltas diverge from the admitted WAV payload. Length and sample
// counts make missing and extra suffixes diagnosable without exposing audio
// payloads in the error itself.
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
		return roomReplayNilString
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

// RoomReplayAudioStream is one detached, mono PCM16 stream with timeline and
// source identity metadata.
type RoomReplayAudioStream struct {
	roomanalysis.PCM16TimedStream
	Role          string
	PCM           []byte
	SampleCount   int
	Artifact      RoomReplayArtifact
	DeltaArtifact RoomReplayArtifact
	Deltas        []RoomReplayAudioDelta
}

// RoomReplayAudioParticipant groups independent output, sent and received
// streams for one participant.
type RoomReplayAudioParticipant struct {
	ID          string
	WAV         RoomReplayAudioStream
	Sent        RoomReplayAudioStream
	Received    RoomReplayAudioStream
	Events      []json.RawMessage
	Diagnostics []json.RawMessage
}

// RoomReplayAudioAnnotation retains generic annotation identity and interval
// while the typed slices on RoomReplayAudioBundle expose analysis forms.
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

// RoomReplayAudioBundle is the validated, detached audio projection of one
// admitted room replay plan.
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

// Participant returns a detached participant projection by stable ID.
func (b RoomReplayAudioBundle) Participant(id string) (RoomReplayAudioParticipant, bool) {
	for _, participant := range b.Participants {
		if participant.ID == id {
			return cloneRoomReplayAudioParticipant(participant), true
		}
	}
	return RoomReplayAudioParticipant{}, false
}

// AnalysisInput converts the detached streams and annotations into the
// side-effect-free room analyzer input. Every returned slice and sample view
// is independently writable by the caller.
func (b RoomReplayAudioBundle) AnalysisInput() roomanalysis.PCM16RoomInput {
	input := roomanalysis.PCM16RoomInput{
		Overlaps: append([]roomanalysis.PCM16OverlapInterval(nil), b.Overlaps...),
		BargeIns: append([]roomanalysis.PCM16BargeInAnnotation(nil), b.BargeIns...),
		Loudness: append([]roomanalysis.PCM16LoudnessInterval(nil), b.Loudness...),
	}
	for _, participant := range b.Participants {
		input.Streams = append(input.Streams,
			cloneTimedStream(participant.WAV.PCM16TimedStream),
			cloneTimedStream(participant.Sent.PCM16TimedStream),
			cloneTimedStream(participant.Received.PCM16TimedStream),
		)
	}
	if b.RoomMix.StreamID != "" {
		input.Streams = append(input.Streams, cloneTimedStream(b.RoomMix.PCM16TimedStream))
	}
	return input
}

// AnalysisConfig returns a value copy of the fully expanded room profile.
func (b RoomReplayAudioBundle) AnalysisConfig() roomanalysis.PCM16RoomAnalysisConfig {
	return b.Tolerances.RoomConfig
}

// Service loads the audio projection for an already admitted room plan.
// Implementations are stateless and return a fresh detached bundle per call.
type Service interface {
	Load(RoomReplayPlan) (RoomReplayAudioBundle, error)
}

func cloneTimedStream(stream roomanalysis.PCM16TimedStream) roomanalysis.PCM16TimedStream {
	stream.Samples = append([]int16(nil), stream.Samples...)
	stream.ExpectedSpeech = append([]streamanalysis.SpeechAnnotation(nil), stream.ExpectedSpeech...)
	stream.ChunkBoundaries = append([]streamanalysis.ChunkBoundary(nil), stream.ChunkBoundaries...)
	return stream
}

func cloneRoomReplayAudioParticipant(participant RoomReplayAudioParticipant) RoomReplayAudioParticipant {
	participant.WAV = cloneRoomReplayAudioStream(participant.WAV)
	participant.Sent = cloneRoomReplayAudioStream(participant.Sent)
	participant.Received = cloneRoomReplayAudioStream(participant.Received)
	participant.Events = cloneRawMessages(participant.Events)
	participant.Diagnostics = cloneRawMessages(participant.Diagnostics)
	return participant
}

func cloneRoomReplayAudioStream(stream RoomReplayAudioStream) RoomReplayAudioStream {
	stream.PCM = append([]byte(nil), stream.PCM...)
	stream.Deltas = append([]RoomReplayAudioDelta(nil), stream.Deltas...)
	for index := range stream.Deltas {
		stream.Deltas[index].PCM = append([]byte(nil), stream.Deltas[index].PCM...)
	}
	stream.PCM16TimedStream = cloneTimedStream(stream.PCM16TimedStream)
	return stream
}

func cloneRawMessages(values []json.RawMessage) []json.RawMessage {
	cloned := make([]json.RawMessage, len(values))
	for index, value := range values {
		cloned[index] = append(json.RawMessage(nil), value...)
	}
	return cloned
}
