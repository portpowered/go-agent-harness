package rooms

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// EvidenceRecordingRequest is the room lifecycle's transport-neutral request
// to the evidence owner. Storage policy remains behind EvidenceService.
type EvidenceRecordingRequest struct {
	Destination     string
	Manifest        Manifest
	AudioFormat     AudioFormat
	Secrets         []string
	StartedAt       time.Time
	Clock           platformclock.Source
	Latency         LatencyService
	LatencyRecorder LatencyRecorder
}

// EvidenceService opens and validates recordings through their owning service.
type EvidenceService interface {
	Open(EvidenceRecordingRequest) (EvidenceRecorder, error)
	ValidateEvidenceOutput(string) error
	CreateFreshRunDirectory(string) (string, error)
}

// EvidenceRecorder is the public recording lifecycle used by room callers.
type EvidenceRecorder interface {
	Destination() string
	StartedAt() time.Time
	AudioFormat() AudioFormat
	Artifacts(string) EvidenceArtifactPaths
	LatencyRecorder() LatencyRecorder
	RecordTimeline(string, string, map[string]string) error
	RecordFinalTimeline(string, string, map[string]string) (time.Time, error)
	RecordProviderErrorTimeline(string, map[string]string) error
	SetParticipantReady(RoomParticipantReady) error
	SetParticipantTerminated(RoomParticipantResult) error
	MarkError(string, string, error)
	RecordSource(string, audio.PCMFrame)
	RecordReceived(string, audio.PCMFrame)
	ObserveSpeakerAudio(string, []string, audio.PCMFrame)
	ObservePeerAudio(string, string, audio.PCMFrame)
	Observe(EvidenceObservation) error
	RecordSessionDiagnostic(EvidenceDiagnosticRecord)
	Error() error
	Health() EvidenceHealth
	Finalize(EvidenceFinalization) (EvidenceResult, error)
	Close() error
}

type EvidenceObservationKind string

const (
	EvidenceObservationTimeline                 EvidenceObservationKind = "timeline"
	EvidenceObservationFinalTimeline            EvidenceObservationKind = "final_timeline"
	EvidenceObservationStreamMessage            EvidenceObservationKind = "stream_message"
	EvidenceObservationLiveEvent                EvidenceObservationKind = "live_event"
	EvidenceObservationProviderError            EvidenceObservationKind = "provider_error"
	EvidenceObservationParticipantReady         EvidenceObservationKind = "participant_ready"
	EvidenceObservationParticipantTerminated    EvidenceObservationKind = "participant_terminated"
	EvidenceObservationSourceAudio              EvidenceObservationKind = "source_audio"
	EvidenceObservationReceivedAudio            EvidenceObservationKind = "received_audio"
	EvidenceObservationSpeakerAudio             EvidenceObservationKind = "speaker_audio"
	EvidenceObservationSpeechStopped            EvidenceObservationKind = "speech_stopped"
	EvidenceObservationProviderAudio            EvidenceObservationKind = "provider_audio"
	EvidenceObservationPeerAudio                EvidenceObservationKind = "peer_audio"
	EvidenceObservationError                    EvidenceObservationKind = "error"
	EvidenceObservationDiagnostic               EvidenceObservationKind = "diagnostic"
	EvidenceObservationDelta                    EvidenceObservationKind = "delta"
	EvidenceObservationParticipantAudio         EvidenceObservationKind = "participant_audio"
	EvidenceObservationSentAudio                EvidenceObservationKind = "sent_audio"
	EvidenceObservationSentStream               EvidenceObservationKind = "sent_stream"
	EvidenceObservationCloseSentSpeechSegment   EvidenceObservationKind = "close_sent_speech_segment"
	EvidenceObservationReceivedParticipantAudio EvidenceObservationKind = "received_participant_audio"
	EvidenceObservationAudioDropped             EvidenceObservationKind = "audio_dropped"
	EvidenceObservationParticipantError         EvidenceObservationKind = "participant_error"
)

// EvidenceObservation is a bounded projection. The evidence service copies
// retained buffers and owns all storage, redaction and failure policy.
type EvidenceObservation struct {
	Kind              EvidenceObservationKind
	ParticipantID     string
	RelatedID         string
	Event             string
	Phase             string
	Fields            map[string]string
	At                time.Time
	PCM               []byte
	TargetIDs         []string
	DroppedBytes      int
	StreamMessage     messages.StreamMessage
	LiveEvent         session.LiveEvent
	AudioFrame        audio.PCMFrame
	ParticipantReady  RoomParticipantReady
	ParticipantResult RoomParticipantResult
	Diagnostic        EvidenceDiagnosticRecord
	Artifact          string
	Err               error
}

type EvidenceFinalization struct {
	Room    RoomResult
	Err     error
	EndedAt time.Time
}

type EvidenceResult struct {
	Room      RoomResult
	Health    EvidenceHealth
	Err       error
	StartedAt time.Time
	EndedAt   time.Time
}

type EvidenceDiagnosticRecord struct {
	ParticipantID string
	Event         string
	Fields        map[string]string
	At            time.Time
}

type EvidenceArtifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
	SentPCM     string `json:"sent_pcm"`
	ReceivedPCM string `json:"received_pcm"`
	Events      string `json:"events"`
	Capture     string `json:"capture,omitempty"`
}

type EvidenceHealth struct {
	Status               *transcript.RecordingStatus
	DegradedArtifacts    map[string]string
	ParticipantStatuses  map[string]*transcript.RecordingStatus
	ParticipantArtifacts map[string]map[string]string
}
