// Package roomevidence provides the host-neutral room recording contract.
//
// The package describes observations and finalized bundle state only. File
// naming, writers, clocks, mixing, redaction, and integrity bookkeeping are
// private to the service implementation composed by the sibling wire package.
package roomevidence

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	SchemaVersion       = 1
	ManifestPath        = "run-manifest.json"
	TimelinePath        = "room-timeline.jsonl"
	MixPath             = "room-mix.wav"
	LatencyPath         = "room-latency.json"
	AudioEncoding       = "pcm_s16le"
	AudioSampleWidthBit = 16
	AudioByteOrder      = "little"
)

// These aliases keep the consumer-facing recording port to one package. The
// underlying room metadata remains the shared runtime contract; no CLI or
// provider registry is required to construct a bundle.
type Manifest = rooms.Manifest
type Room = rooms.Room
type Participant = rooms.Participant
type ParticipantKind = rooms.ParticipantKind
type AudioFormat = rooms.AudioFormat
type RoomResult = rooms.RoomResult
type RoomParticipantResult = rooms.RoomParticipantResult
type RoomTerminationReason = rooms.RoomTerminationReason
type ParticipantTerminationReason = rooms.ParticipantTerminationReason

const (
	ParticipantKindAgent    = rooms.ParticipantKindAgent
	ParticipantKindHuman    = rooms.ParticipantKindHuman
	ParticipantKindCustomer = rooms.ParticipantKindCustomer

	RoomTerminationStopped            = rooms.RoomTerminationStopped
	RoomTerminationMaxTurnsReached    = rooms.RoomTerminationMaxTurnsReached
	RoomTerminationMaxDurationReached = rooms.RoomTerminationMaxDurationReached
	RoomTerminationFailed             = rooms.RoomTerminationFailed

	ParticipantTerminationEnded        = rooms.ParticipantTerminationEnded
	ParticipantTerminationDisconnected = rooms.ParticipantTerminationDisconnected
	ParticipantTerminationError        = rooms.ParticipantTerminationError
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrInvalidOutput      errorCode = "invalid room evidence output"
	ErrOutputNotEmpty     errorCode = "room evidence output directory is not empty"
	ErrFinalized          errorCode = "room evidence recorder is finalized"
	ErrParticipantUnknown errorCode = "room evidence participant is unknown"
	ErrRecorderClosed     errorCode = "room evidence recorder is closed"
)

// Options contains only normalized, credential-free room metadata. Secrets
// are values used for defensive redaction and never enter the manifest.
type Options struct {
	Destination string
	Manifest    rooms.Manifest
	AudioFormat rooms.AudioFormat
	Secrets     []string
	StartedAt   time.Time
	Clock       platformclock.Source
	Latency     rooms.LatencyService
	// LatencyRecorder lets an orchestrator share one invocation-scoped ledger
	// with its live observations while keeping construction behind the service.
	LatencyRecorder rooms.LatencyRecorder
}

// Service is an inert factory. It performs no filesystem or host discovery
// work until PrepareOutput or Open is called.
type Service interface {
	ValidateOutput(string) error
	PrepareOutput(string) (string, error)
	Open(Options) (Recorder, error)
}

// Recorder owns one room's evidence lifecycle.
type Recorder interface {
	Destination() string
	StartedAt() time.Time
	AudioFormat() rooms.AudioFormat
	Participant(string) ParticipantRecorder
	CapturePath(string) string

	RecordTimeline(string, string, map[string]string) error
	RecordFinalTimeline(string, string, map[string]string) (time.Time, error)
	RecordProviderErrorTimeline(string, map[string]string) error
	SetParticipantReady(rooms.RoomParticipantReady) error

	ObserveSpeakerAudio(string, []string, []byte)
	ObserveSpeechStopped(string)
	ObserveProviderAudio(string, string)
	ObservePeerAudio(string, string, []byte)
	MarkError(string, string, error)
	Error() error
	Health() Health
	ApplyRecordingHealth(*rooms.RoomResult)
	Finalize(rooms.RoomResult, error, time.Time) error
	Close() error
}

// ParticipantRecorder is the observation port for one participant. All
// methods copy caller-owned buffers before retaining or writing them.
type ParticipantRecorder interface {
	ID() string
	Artifacts() ArtifactPaths
	RecordDiagnostic(DiagnosticRecord) error
	ObserveDelta(messages.StreamMessage) error
	ObserveAudio([]byte) error
	ObserveSentAudio([]byte) error
	ObserveSentStream([]byte) error
	CloseSentSpeechSegment() error
	ObserveReceivedAudio([]byte) error
	RecordAudioDropped(string, int) error
	MarkError(string, error) error
}

// DiagnosticRecord is the bounded, transport-neutral diagnostic projection.
type DiagnosticRecord struct {
	Event  string
	Fields map[string]string
	At     time.Time
}

// ArtifactPaths is the stable relative-path inventory for one participant.
type ArtifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
	SentPCM     string `json:"sent_pcm"`
	ReceivedPCM string `json:"received_pcm"`
	Events      string `json:"events"`
	Capture     string `json:"capture,omitempty"`
}

// Health is the recording-only status projection. Runtime termination remains
// owned by the room service and is not changed by a degraded sink.
type Health struct {
	Status               *transcript.RecordingStatus
	DegradedArtifacts    map[string]string
	ParticipantStatuses  map[string]*transcript.RecordingStatus
	ParticipantArtifacts map[string]map[string]string
}
