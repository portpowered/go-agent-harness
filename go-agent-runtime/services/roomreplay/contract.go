// Package roomreplay owns admission of finalized, credential-free room
// replay bundles. Hosts receive immutable projections; parsing and integrity
// policy remain behind the service and its dedicated Wire graph.
package roomreplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	// RoomReplayBundleSchemaVersion is the additive room evidence schema that
	// replay admission understands. Version one remains accepted because the
	// completeness lane evolves the existing room manifest additively.
	RoomReplayBundleSchemaVersion = 2

	// RoomReplayBundleManifestPath is the stable manifest name used by room
	// evidence bundles. Service.Load also accepts this file directly.
	RoomReplayBundleManifestPath = "run-manifest.json"
)

// ParticipantKind identifies the owner of a replay participant without
// importing the room lifecycle package. The room service converts this
// source projection into its own manifest contract.
type ParticipantKind string

const (
	ParticipantKindAgent ParticipantKind = "agent"
	ParticipantKindHuman ParticipantKind = "human"
)

const (
	ArtifactRoleWAV         = "wav"
	ArtifactRoleDiagnostics = "diagnostics"
	ArtifactRoleDeltas      = "deltas"
	ArtifactRoleSentPCM     = "sent_pcm"
	ArtifactRoleReceivedPCM = "received_pcm"
	ArtifactRoleEvents      = "events"
	ArtifactRoleCapture     = "capture"
)

type roomReplaySentinel string

func (e roomReplaySentinel) Error() string { return string(e) }

const (
	// ErrInvalidRoomReplayBundle identifies a bundle that cannot be used as a
	// replay plan.
	ErrInvalidRoomReplayBundle roomReplaySentinel = "invalid room replay bundle"
	// ErrRoomReplayBundleIncomplete identifies missing or truncated bundle data.
	ErrRoomReplayBundleIncomplete roomReplaySentinel = "room replay bundle incomplete"
	// ErrRoomReplaySourceConflict identifies a mixed live/configured replay.
	ErrRoomReplaySourceConflict roomReplaySentinel = "room replay bundle cannot be combined with room config or manifest"
)

// RoomReplayBundleErrorKind is the stable classification of an admission
// failure. Integrity and schema disagreements are mismatches; missing bytes
// and unfinalized captures are incomplete.
type RoomReplayBundleErrorKind string

const (
	RoomReplayBundleMismatch   RoomReplayBundleErrorKind = "mismatch"
	RoomReplayBundleIncomplete RoomReplayBundleErrorKind = "incomplete"
)

// RoomReplayBundleError carries bounded, non-secret context for an admission
// failure. Expected and Actual contain metadata such as paths, sizes, or
// digests, never artifact payloads or credentials.
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
		return "<nil>"
	}
	label := string(e.Kind)
	if label == "" {
		label = string(RoomReplayBundleMismatch)
	}
	message := "room replay bundle " + label
	if e.Field != "" {
		message += " field " + strconvQuote(e.Field)
	}
	if e.Artifact != "" {
		message += " artifact " + strconvQuote(e.Artifact)
	}
	if e.Expected != "" || e.Actual != "" {
		message += fmt.Sprintf(": expected %s, actual %s", e.Expected, e.Actual)
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

// Is preserves the repository's provider and gateway replay classifications
// while retaining the room-specific admission error for callers that need it.
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

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
}

// RoomReplayPCMFormat describes the raw PCM contract declared by a room
// recording. The replay runtime converts the sample rate and channel count to
// the production mixer format after admission.
type RoomReplayPCMFormat struct {
	SampleRate      int `json:"sample_rate"`
	Channels        int `json:"channels"`
	SampleWidthBits int `json:"sample_width_bits"`
	// SampleWidthBit is retained as an input-compatibility alias for callers
	// that used the original singular field before the public shape settled.
	SampleWidthBit int    `json:"sample_width_bit,omitempty"`
	ByteOrder      string `json:"byte_order"`
	Encoding       string `json:"encoding"`
}

// RoomReplayArtifact is one validated, bundle-relative file. AbsolutePath is
// resolved once during admission and is never derived from an untrusted path
// again by the replay runtime.
type RoomReplayArtifact struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Owner        string `json:"owner"`
	Path         string `json:"path"`
	AbsolutePath string `json:"-"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Empty        bool   `json:"empty,omitempty"`
}

// RoomReplayParticipant is the immutable normalized participant projection
// used by later room replay composition. CapturePath is empty only for a
// human participant; provider participants always have a strict session
// capture.
type RoomReplayParticipant struct {
	ID                string               `json:"id"`
	Kind              ParticipantKind      `json:"kind"`
	Provider          string               `json:"provider"`
	Model             string               `json:"model"`
	Voice             string               `json:"voice"`
	OpeningPrompt     string               `json:"opening_prompt"`
	SystemPrompt      string               `json:"system_prompt"`
	CapturePath       string               `json:"-"`
	Capture           RoomReplayArtifact   `json:"capture"`
	Artifacts         []RoomReplayArtifact `json:"artifacts"`
	RecordedTurnCount int                  `json:"recorded_turn_count"`
}

// RoomReplayTimelineEvent is the validated, lossless projection of one
// room-timeline.jsonl line. Raw retains fields added by the recording lane so
// the scheduler can consume them without a second lossy decode.
type RoomReplayTimelineEvent struct {
	Sequence int64 `json:"sequence"`
	OffsetMS int64 `json:"offset_ms"`
	// OffsetNanos retains fractional t_offset_ms values emitted by the room
	// recorder. OffsetMS remains the compatibility projection used by older
	// callers and manifests.
	OffsetNanos   int64           `json:"offset_nanos,omitempty"`
	UnixMS        int64           `json:"unix_ms"`
	Type          string          `json:"type"`
	ParticipantID string          `json:"participant_id"`
	Raw           json.RawMessage `json:"-"`
}

// RoomReplayPlan is a source-independent replay plan. It owns copies of all
// decoded strings, paths, and JSON lines; changing or replacing the source
// files after this function returns cannot mutate the plan. Callers should
// treat the exported slices as read-only.
type RoomReplayPlan struct {
	BundlePath      string
	ManifestPath    string
	SchemaVersion   int
	Finalized       bool
	ClockBase       time.Time
	StartedAt       time.Time
	EndedAt         time.Time
	PCMFormat       RoomReplayPCMFormat
	Participants    []RoomReplayParticipant
	Timeline        []RoomReplayTimelineEvent
	TimelinePath    string
	RoomMixPath     string
	RoomLatencyPath string
	Artifacts       []RoomReplayArtifact
}

var (
	ErrRoomReplayDeltaReconstruction = errors.New("room replay delta reconstruction failed")
	ErrRoomReplayAudioTimeline       = errors.New("room replay audio timeline is inconsistent")
	ErrRoomReplayToleranceProfile    = errors.New("invalid room replay tolerance profile")
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

// Participant returns a copy of a participant projection by stable ID.
func (p RoomReplayPlan) Participant(id string) (RoomReplayParticipant, bool) {
	for _, participant := range p.Participants {
		if participant.ID == id {
			participant.Artifacts = append([]RoomReplayArtifact(nil), participant.Artifacts...)
			participant.Capture = cloneRoomReplayArtifact(participant.Capture)
			return participant, true
		}
	}
	return RoomReplayParticipant{}, false
}

type scheduleError string

func (e scheduleError) Error() string { return string(e) }

const (
	// ErrInvalidRequest identifies an incomplete or inconsistent schedule input.
	ErrInvalidRequest scheduleError = "room replay schedule request is invalid"
	// ErrInvalidFormat identifies a PCM format that cannot produce aligned
	// signed PCM16 frames.
	ErrInvalidFormat scheduleError = "room replay schedule PCM format is invalid"
	// ErrParticipantMissing identifies a target without an admitted participant.
	ErrParticipantMissing scheduleError = "room replay participant is missing"
	// ErrCaptureUnavailable identifies a target capture that cannot be loaded.
	ErrCaptureUnavailable scheduleError = "room replay participant capture is unavailable"
	// ErrSentPCMUnavailable identifies missing or unreadable sent PCM evidence.
	ErrSentPCMUnavailable scheduleError = "room replay sent PCM is unavailable"
	// ErrInvalidPCM identifies malformed or unsupported source PCM.
	ErrInvalidPCM scheduleError = "room replay PCM is invalid"
	// ErrTargetMissing identifies a target required by a schedule but absent
	// from a run request.
	ErrTargetMissing scheduleError = "room replay target is missing"
	// ErrTargetUncontrolled identifies a target missing scheduler callbacks.
	ErrTargetUncontrolled scheduleError = "room replay target is not scheduler-controlled"
	// ErrTargetInactive identifies a target that stopped before a frame release.
	ErrTargetInactive scheduleError = "room replay target is inactive"
	// ErrTargetStopped identifies a target acknowledgement that terminated early.
	ErrTargetStopped scheduleError = "room replay target stopped"
	// ErrScheduleTooLong identifies a replay whose logical frame count exceeds
	// the bounded schedule capacity.
	ErrScheduleTooLong scheduleError = "room replay schedule exceeds the supported frame limit"
)

// PCM16Format is the target cadence used by a deterministic replay schedule.
// Samples are signed, little-endian, interleaved PCM16.
type PCM16Format struct {
	SampleRate    int
	Channels      int
	FrameDuration time.Duration
}

// SourcePCM16Format describes the raw sent-PCM artifact format.
type SourcePCM16Format struct {
	SampleRate      int
	Channels        int
	SampleWidthBits int
	SampleWidthBit  int
	ByteOrder       string
	Encoding        string
}

// ScheduleParticipant identifies admitted capture and sent-PCM evidence for
// one participant. CapturePath is also the completion barrier source.
type Participant struct {
	ID          string
	CapturePath string
	SentPCMPath string
}

// ScheduleTimelineEvent places one participant's speech on logical frames.
type TimelineEvent struct {
	Sequence      int64
	OffsetMS      int64
	OffsetNanos   int64
	Type          string
	ParticipantID string
}

// BuildRequest contains validated, credential-free replay scheduling inputs.
type BuildRequest struct {
	SourceFormat SourcePCM16Format
	TargetFormat PCM16Format
	Participants []Participant
	Timeline     []TimelineEvent
	TargetIDs    []string
}

// Schedule builds and runs a deterministic replay schedule.
type Schedule interface {
	Run(context.Context, RunRequest) error
}

// Target is the narrow adapter-owned control surface for one provider mixer.
type Target struct {
	ID                   string
	Active               func() bool
	Release              func(context.Context, string, []byte) error
	Advance              func(context.Context) error
	AwaitAcknowledgement func(context.Context) error
}

// Waiter identifies a participant completion barrier. A nil Done channel is
// ignored for synthetic targets.
type Waiter struct {
	ID   string
	Done <-chan struct{}
}

// RunRequest supplies target callbacks and final participant barriers.
type RunRequest struct {
	Targets        []Target
	WaitFor        []Waiter
	IsStopping     func() bool
	OnContribution func(Contribution)
}

// Contribution is an immutable source-to-target PCM release notification.
type Contribution struct {
	Frame    int
	SourceID string
	TargetID string
	PCM      []byte
}

// Service admits one bundle per call. Implementations are stateless and must
// return fresh projections, so concurrent hosts cannot share mutable parser or
// filesystem state through the service graph.
type Service interface {
	Load(string) (RoomReplayPlan, error)
	LoadAudioBundle(string) (RoomReplayAudioBundle, error)
	ValidateOutput(RoomReplayPlan, string) error
	Build(context.Context, BuildRequest) (Schedule, error)
}

func cloneRoomReplayArtifact(artifact RoomReplayArtifact) RoomReplayArtifact {
	return artifact
}
