// Package roomevidence exposes the transport-neutral recording and replay
// contract. File naming, writers, clocks, mixing, redaction and integrity
// bookkeeping stay private to its service implementation.
package roomevidence

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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

type RecordingRequest = rooms.EvidenceRecordingRequest

// Service is an inert factory. It performs no filesystem or host discovery
// work until PrepareOutput or Open is called.
type Service interface {
	RecorderService
	OutputService
	ValidateOutput(string) error
	PrepareOutput(string) (string, error)
	LoadPlan(string) (RoomReplayPlan, error)
	ValidateReplayOutput(RoomReplayPlan, string) error
	Load(RoomReplayPlan) (Bundle, error)
	Analyze(Bundle) (Analysis, error)
}

// RecorderService owns the room recording lifecycle behind the evidence
// contract. Room execution code may depend on this smaller role while the
// application graph supplies the complete service.
type RecorderService interface {
	Open(RecordingRequest) (Recorder, error)
}

// OutputService owns validation and fresh-directory creation for recording
// destinations. It is a focused role for the room service boundary.
type OutputService interface {
	ValidateEvidenceOutput(string) error
	CreateFreshRunDirectory(string) (string, error)
}

type LatencyService interface {
	rooms.LatencyService
	NewRuntimeObserver(rooms.LatencyRecorder, string) sessiontrace.RuntimeObserver
}

type Recorder interface {
	Destination() string
	StartedAt() time.Time
	AudioFormat() AudioFormat
	Artifacts(string) ArtifactPaths
	LatencyRecorder() rooms.LatencyRecorder
	RecordTimeline(string, string, map[string]string) error
	RecordFinalTimeline(string, string, map[string]string) (time.Time, error)
	RecordProviderErrorTimeline(string, map[string]string) error
	SetParticipantReady(rooms.RoomParticipantReady) error
	SetParticipantTerminated(rooms.RoomParticipantResult) error
	MarkError(string, string, error)
	RecordSource(string, audio.PCMFrame)
	RecordReceived(string, audio.PCMFrame)
	ObserveSpeakerAudio(string, []string, audio.PCMFrame)
	ObservePeerAudio(string, string, audio.PCMFrame)
	Observe(Observation) error
	RecordSessionDiagnostic(DiagnosticRecord)
	Error() error
	Health() Health
	Finalize(Finalization) (Result, error)
	Close() error
}
type Observation = rooms.EvidenceObservation
type ObservationKind = rooms.EvidenceObservationKind
type Finalization = rooms.EvidenceFinalization
type Result = rooms.EvidenceResult
type DiagnosticRecord = rooms.EvidenceDiagnosticRecord
type ArtifactPaths = rooms.EvidenceArtifactPaths
type Health = rooms.EvidenceHealth

// RunManifest is the stable JSON projection written for one recording. The
// service owns encoding, validation and integrity policy; this shape lets a
// caller inspect the resulting public artifact without importing its writer.
type RunManifest struct {
	SchemaVersion     int                            `json:"schema_version"`
	Finalized         bool                           `json:"finalized"`
	Timing            ManifestTiming                 `json:"timing"`
	Bounds            ManifestBounds                 `json:"bounds"`
	TerminationReason rooms.RoomTerminationReason    `json:"termination_reason"`
	Reason            rooms.RoomTerminationReason    `json:"reason,omitempty"`
	Participants      map[string]ManifestParticipant `json:"participants"`
	TurnCounts        map[string]int                 `json:"turn_counts"`
	AudioFormat       ManifestAudioFormat            `json:"audio_format"`
	RoomMix           string                         `json:"room_mix"`
	RoomTimeline      string                         `json:"room_timeline"`
	RoomLatency       string                         `json:"room_latency,omitempty"`
	Artifacts         map[string]string              `json:"artifacts"`
	ArtifactIntegrity map[string]ArtifactIntegrity   `json:"artifact_integrity,omitempty"`
	RecordingStatus   *transcript.RecordingStatus    `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string              `json:"degraded_artifacts,omitempty"`
	Error             string                         `json:"error,omitempty"`
}

type ManifestTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
	ClockBase string `json:"clock_base"`
}

type ManifestBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}

type ManifestAudioFormat struct {
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	Encoding        string `json:"encoding"`
	SampleWidthBits int    `json:"sample_width_bits"`
	ByteOrder       string `json:"byte_order"`
}

type ManifestParticipant struct {
	ID                     string                             `json:"id"`
	Kind                   rooms.ParticipantKind              `json:"kind"`
	SystemPrompt           string                             `json:"system_prompt"`
	OpeningPrompt          string                             `json:"opening_prompt,omitempty"`
	Provider               string                             `json:"provider"`
	Model                  string                             `json:"model"`
	APIKeyEnv              string                             `json:"api_key_env"`
	Voice                  string                             `json:"voice,omitempty"`
	Tools                  []string                           `json:"tools"`
	BrowserTools           *rooms.BrowserToolsConfig          `json:"browser_tools,omitempty"`
	CompletedTurns         int                                `json:"completed_turns"`
	TerminationReason      rooms.ParticipantTerminationReason `json:"termination_reason"`
	Reason                 rooms.ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                             `json:"termination_trigger"`
	TerminationDisposition string                             `json:"termination_disposition"`
	Classification         string                             `json:"classification"`
	TerminalReason         string                             `json:"terminal_reason"`
	TerminalProvenance     string                             `json:"terminal_provenance"`
	OutputState            string                             `json:"output_state"`
	Connected              bool                               `json:"connected"`
	InputDevice            string                             `json:"input_device,omitempty"`
	OutputDevice           string                             `json:"output_device,omitempty"`
	Error                  string                             `json:"error,omitempty"`
	Artifacts              ArtifactPaths                      `json:"artifacts"`
	RecordingStatus        *transcript.RecordingStatus        `json:"recording_status,omitempty"`
	DegradedArtifacts      map[string]string                  `json:"degraded_artifacts,omitempty"`
}

type ArtifactIntegrity struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type TimelineEntry struct {
	TOffsetMS     float64           `json:"t_offset_ms"`
	TUnixMS       int64             `json:"t_unix_ms"`
	Event         string            `json:"event"`
	Participant   string            `json:"participant,omitempty"`
	ParticipantID string            `json:"participant_id,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

const (
	ObservationTimeline                 = rooms.EvidenceObservationTimeline
	ObservationFinalTimeline            = rooms.EvidenceObservationFinalTimeline
	ObservationStreamMessage            = rooms.EvidenceObservationStreamMessage
	ObservationLiveEvent                = rooms.EvidenceObservationLiveEvent
	ObservationProviderError            = rooms.EvidenceObservationProviderError
	ObservationParticipantReady         = rooms.EvidenceObservationParticipantReady
	ObservationParticipantTerminated    = rooms.EvidenceObservationParticipantTerminated
	ObservationSourceAudio              = rooms.EvidenceObservationSourceAudio
	ObservationReceivedAudio            = rooms.EvidenceObservationReceivedAudio
	ObservationSpeakerAudio             = rooms.EvidenceObservationSpeakerAudio
	ObservationSpeechStopped            = rooms.EvidenceObservationSpeechStopped
	ObservationProviderAudio            = rooms.EvidenceObservationProviderAudio
	ObservationPeerAudio                = rooms.EvidenceObservationPeerAudio
	ObservationError                    = rooms.EvidenceObservationError
	ObservationDiagnostic               = rooms.EvidenceObservationDiagnostic
	ObservationDelta                    = rooms.EvidenceObservationDelta
	ObservationParticipantAudio         = rooms.EvidenceObservationParticipantAudio
	ObservationSentAudio                = rooms.EvidenceObservationSentAudio
	ObservationSentStream               = rooms.EvidenceObservationSentStream
	ObservationCloseSentSpeechSegment   = rooms.EvidenceObservationCloseSentSpeechSegment
	ObservationReceivedParticipantAudio = rooms.EvidenceObservationReceivedParticipantAudio
	ObservationAudioDropped             = rooms.EvidenceObservationAudioDropped
	ObservationParticipantError         = rooms.EvidenceObservationParticipantError
)
