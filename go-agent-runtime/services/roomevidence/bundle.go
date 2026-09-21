// Package roomevidence owns bounded room evidence and audio projection for an admitted room
// replay plan. Filesystem decoding, metadata policy and analysis preparation
// stay behind this contract; hosts supply only the already-admitted plan.
package roomevidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
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

// BundleErrorKind is the stable classification of an audio
// admission failure.
type BundleErrorKind string

const (
	BundleMismatch      BundleErrorKind = "mismatch"
	BundleIncomplete    BundleErrorKind = "incomplete"
	roomReplayNilString                 = "<nil>"
)

// BundleError carries bounded, non-secret context for an admission
// failure. Expected and Actual contain paths, sizes, digests, or classifications
// only; payloads and credentials never cross this boundary.
type BundleError struct {
	Kind     BundleErrorKind
	Field    string
	Artifact string
	Expected string
	Actual   string
	Err      error
}

func (e *BundleError) Error() string {
	if e == nil {
		return roomReplayNilString
	}
	label := string(e.Kind)
	if label == "" {
		label = string(BundleMismatch)
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

func (e *BundleError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is preserves the repository's replay classifications for hosts that use the
// shared gateway/provider error vocabulary.
func (e *BundleError) Is(target error) bool {
	if e == nil {
		return false
	}
	if target == ErrInvalidRoomReplayBundle {
		return e.Kind == BundleMismatch
	}
	if e.Kind == BundleIncomplete {
		return target == ErrRoomReplayBundleIncomplete || target == gateway.ErrReplayIncomplete || target == providers.ErrReplayIncomplete
	}
	return target == gateway.ErrReplayMismatch || target == providers.ErrReplayMismatch
}

// ToleranceProfile is the immutable-by-convention analysis profile
// attached to one replay bundle. A supplied profile may only tighten suite
// defaults.
type ToleranceProfile struct {
	Name         string
	StreamConfig roomanalysis.PCM16AnalysisConfig
	RoomConfig   roomanalysis.PCM16RoomAnalysisConfig
}

// AudioDelta is one decoded audio delta in source JSONL order.
type AudioDelta struct {
	ID          string
	Sequence    int64
	HasSequence bool
	Offset      time.Duration
	HasOffset   bool
	TurnID      string
	LineNumber  int
	PCM         []byte
}

// DeltaReconstructionError points to the first byte where the
// recorded deltas diverge from the admitted WAV payload. Length and sample
// counts make missing and extra suffixes diagnosable without exposing audio
// payloads in the error itself.
type DeltaReconstructionError struct {
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

func (e *DeltaReconstructionError) Error() string {
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

func (e *DeltaReconstructionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(ErrRoomReplayDeltaReconstruction, ErrInvalidRoomReplayBundle, e.Cause)
}

// AudioStream is one detached, mono PCM16 stream with timeline and
// source identity metadata.
type AudioStream struct {
	roomanalysis.PCM16TimedStream
	Role          string
	PCM           []byte
	SampleCount   int
	Artifact      RoomReplayArtifact
	DeltaArtifact RoomReplayArtifact
	Deltas        []AudioDelta
}

// AudioParticipant groups independent output, sent and received
// streams for one participant.
type AudioParticipant struct {
	ID          string
	WAV         AudioStream
	Sent        AudioStream
	Received    AudioStream
	Events      []json.RawMessage
	Diagnostics []json.RawMessage
}

// AudioAnnotation retains generic annotation identity and interval
// while the typed slices on Bundle expose analysis forms.
type AudioAnnotation struct {
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

// Bundle is the validated, detached audio projection of one
// admitted room replay plan.
type Bundle struct {
	Plan         RoomReplayPlan
	Format       RoomReplayPCMFormat
	Tolerances   ToleranceProfile
	Participants []AudioParticipant
	RoomMix      AudioStream
	Annotations  []AudioAnnotation
	Overlaps     []roomanalysis.PCM16OverlapInterval
	BargeIns     []roomanalysis.PCM16BargeInAnnotation
	Loudness     []roomanalysis.PCM16LoudnessInterval
}

// Analysis is the canonical room analysis result produced by Service. Its
// measurements and failure classifications are owned by the service boundary.
type Analysis struct {
	Result roomanalysis.PCM16RoomAnalysis
}
